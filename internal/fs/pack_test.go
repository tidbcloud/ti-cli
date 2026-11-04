package fs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func writePackSymlinkEntry(t *testing.T, localRoot, name, linkname string) error {
	t.Helper()
	var dirTimes []packDirTime
	hdr := &tar.Header{
		Name:     name,
		Typeflag: tar.TypeSymlink,
		Linkname: linkname,
		Mode:     0o777,
	}
	return extractPackEntry(localRoot, hdr, bytes.NewReader(nil), &dirTimes)
}

// TestExtractPackEntryKeepsSafeRelativeSymlinkTargets keeps pack round-trips
// working for links that stay inside the local overlay.
func TestExtractPackEntryKeepsSafeRelativeSymlinkTargets(t *testing.T) {
	cases := []struct{ name, linkname, want string }{
		{"entries/README.link", "README.md", "README.md"},
		{"entries/dir/README.link", "../README.md", "../README.md"},
		{"entries/dir/README.link", "./sibling", "./sibling"},
	}
	for _, tc := range cases {
		localRoot := t.TempDir()
		if err := writePackSymlinkEntry(t, localRoot, tc.name, tc.linkname); err != nil {
			t.Fatalf("extractPackEntry(%q -> %q) error = %v", tc.name, tc.linkname, err)
		}
		rel, err := packEntryRel(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		target, err := safeOverlayTarget(localRoot, rel)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%q is not a symlink: %v", target, info.Mode())
		}
		got, err := os.Readlink(target)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("Readlink(%q) = %q, want %q", target, got, tc.want)
		}
	}
}

// TestExtractPackEntryRejectsEscapingSymlinkTargets covers the reported finding:
// pack unpack accepted absolute and ".." symlink targets, letting remote archive
// content plant links that escape the local overlay.
func TestExtractPackEntryRejectsEscapingSymlinkTargets(t *testing.T) {
	cases := []struct{ name, linkname string }{
		{"entries/link", "/etc/passwd"},
		{"entries/link", "/"},
		{"entries/link", ".."},
		{"entries/link", "../victim"},
		{"entries/dir/link", "../../victim"},
		{"entries/dir/link", "../../../../../../../home/victim/.ssh"},
		{"entries/link", "C:\\Windows\\System32"},
		{"entries/link", "\\\\server\\share"},
		{"entries/link", ""},
		{"entries/link", "sub/\x00victim"},
		{"entries/link", "dir\\..\\..\\victim"},
	}
	for _, tc := range cases {
		localRoot := t.TempDir()
		victim := filepath.Join(filepath.Dir(localRoot), "victim")
		err := writePackSymlinkEntry(t, localRoot, tc.name, tc.linkname)
		if err == nil {
			t.Fatalf("pack symlink %q -> %q extracted without an error", tc.name, tc.linkname)
		}
		if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
			t.Fatalf("pack symlink %q -> %q error code = %q: %v", tc.name, tc.linkname, code, err)
		}
		if _, statErr := os.Lstat(victim); !os.IsNotExist(statErr) {
			t.Fatalf("pack symlink %q -> %q created something outside the overlay", tc.name, tc.linkname)
		}
	}
}

func TestValidatePackSymlinkTargetAllowsOverlayRootTarget(t *testing.T) {
	localRoot := t.TempDir()
	if err := validatePackSymlinkTarget(localRoot, "a/b/link", "../.."); err != nil {
		t.Fatalf("target resolving to the overlay root rejected: %v", err)
	}
	if err := validatePackSymlinkTarget(localRoot, "a/b/link", "../../.."); err == nil {
		t.Fatal("target escaping the overlay root accepted")
	}
}

// snapshotEntries builds an overlay snapshot for tests without touching the
// filesystem, so folding rules and pre-run spellings can be pinned exactly.
func snapshotEntries(entries map[string]overlayEntry) *overlayTreeSnapshot {
	return newOverlayTreeSnapshot(entries)
}

// TestAuditUnpackedOverlaySymlinksRemovesEscapingLinks covers the enforcement
// that runs on the public unpack path, where the ti-drive9 companion performs
// the extraction: escaping links are removed and the command fails closed.
func TestAuditUnpackedOverlaySymlinksRemovesEscapingLinks(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	content := filepath.Join(repo, "item.txt")
	if err := os.WriteFile(content, []byte("packed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	safeLinks := map[string]string{
		filepath.Join(repo, "inside"):  "item.txt",
		filepath.Join(repo, "sibling"): "../sibling.txt",
	}
	for path, target := range safeLinks {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	unsafeLinks := map[string]string{
		filepath.Join(repo, "absolute"): "/etc/passwd",
		filepath.Join(repo, "escape"):   "../../../../../../home/victim/.ssh",
	}
	for path, target := range unsafeLinks {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	err := auditUnpackedOverlaySymlinks(localRoot, nil)
	if err == nil {
		t.Fatal("escaping symlinks were accepted")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	for path := range unsafeLinks {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("unsafe symlink %q was left behind: %v", path, statErr)
		}
	}
	for path, target := range safeLinks {
		got, readErr := os.Readlink(path)
		if readErr != nil || got != target {
			t.Fatalf("safe symlink %q was removed: got=%q err=%v", path, got, readErr)
		}
	}
	if _, statErr := os.Stat(content); statErr != nil {
		t.Fatalf("benign unpacked content was lost: %v", statErr)
	}
}

func TestAuditUnpackedOverlaySymlinksAcceptsSafeAndAbsentOverlays(t *testing.T) {
	if err := auditUnpackedOverlaySymlinks("", nil); err == nil {
		t.Fatal("empty local root silently disabled the audit")
	}
	missing := filepath.Join(t.TempDir(), "no-overlay")
	if err := auditUnpackedOverlaySymlinks(missing, nil); err != nil {
		t.Fatalf("missing overlay rejected: %v", err)
	}
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "item.txt"), []byte("packed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("item.txt", filepath.Join(repo, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, nil); err != nil {
		t.Fatalf("safe overlay rejected: %v", err)
	}
	symlinkedOverlay := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(symlinkedOverlay, "overlay")); err != nil {
		t.Skipf("symlinks are not supported: %v", err)
	}
	if err := auditUnpackedOverlaySymlinks(symlinkedOverlay, nil); err == nil {
		t.Fatal("symlinked overlay accepted")
	}
}

// TestAuditUnpackedOverlaySymlinksCatchesEscapingLinkChain covers the finding
// that lexical containment misses a chain: `s -> ..` plus
// `a -> s/../outside` passes each single-link check, but following the links
// reaches localRoot/outside.
func TestAuditUnpackedOverlaySymlinksCatchesEscapingLinkChain(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("s/../outside", filepath.Join(repo, "a")); err != nil {
		t.Fatal(err)
	}
	err := auditUnpackedOverlaySymlinks(localRoot, nil)
	if err == nil {
		t.Fatal("escaping symlink chain was accepted")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "a")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping chain link %q was left behind: %v", "a", statErr)
	}
	// The intermediate hop resolves to the overlay root itself, which the policy
	// keeps; only the link that escapes is removed.
	if _, statErr := os.Lstat(filepath.Join(repo, "s")); statErr != nil {
		t.Fatalf("in-overlay chain link %q was removed: %v", "s", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksPreservesUnchangedPreExistingLinks covers the
// finding that the audit must not delete links the archive did not install.
func TestAuditUnpackedOverlaySymlinksPreservesUnchangedPreExistingLinks(t *testing.T) {
	localRoot := t.TempDir()
	other := filepath.Join(localRoot, "overlay", "other")
	repo := filepath.Join(localRoot, "overlay", "repo")
	for _, dir := range []string{other, repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "item.txt"), []byte("packed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preexisting := map[string]string{
		filepath.Join(other, "tool"):   "/usr/bin/tool",
		filepath.Join(other, "escape"): "../../../../../../home/victim/.ssh",
	}
	for path, target := range preexisting {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	before, err := scanOverlayTree(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("item.txt", filepath.Join(repo, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err != nil {
		t.Fatalf("unchanged pre-existing links rejected: %v", err)
	}
	for path, target := range preexisting {
		got, readErr := os.Readlink(path)
		if readErr != nil || got != target {
			t.Fatalf("pre-existing link %q was modified: got=%q err=%v", path, got, readErr)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(repo, "new-escape")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err == nil {
		t.Fatal("new escaping link was accepted")
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "new-escape")); !os.IsNotExist(statErr) {
		t.Fatalf("new escaping link was left behind: %v", statErr)
	}
	for path := range preexisting {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("pre-existing link %q was removed by the new violation: %v", path, statErr)
		}
	}
}

// TestAuditUnpackedOverlaySymlinksRejectsUnchangedLinkWhoseDependencyMoved
// covers the finding that comparing the target text is not enough: an unchanged
// `a -> s/../outside` was confined while `s` was a directory, and the archive
// turns `s` into a permitted link, so `a` now escapes.
func TestAuditUnpackedOverlaySymlinksRejectsUnchangedLinkWhoseDependencyMoved(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(filepath.Join(repo, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink("s/../outside", linkA); err != nil {
		t.Fatal(err)
	}
	before, err := scanOverlayTree(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err == nil {
		t.Fatal("link that became unsafe through an installed dependency was accepted")
	}
	if _, statErr := os.Lstat(linkA); !os.IsNotExist(statErr) {
		t.Fatalf("unchanged escaping link %q was left behind: %v", "a", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksRejectsReplacedDirectorySpelling covers the
// review finding that the pre-run decision must use the snapshot's own names and
// semantics: the archive replaces `O/Repo` with a case-sensitive `O/repo`, and
// canonicalizing the ancestor against the live tree would wrongly conclude that
// the unchanged link was already escaping.
func TestAuditUnpackedOverlaySymlinksRejectsReplacedDirectorySpelling(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	work := filepath.Join(overlayRoot, "A")
	repo := filepath.Join(overlayRoot, "repo")
	for _, dir := range []string{filepath.Join(work), repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := "t/repo/S/../../../outside"
	linkA := filepath.Join(work, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(work, "t")); err != nil {
		t.Fatal(err)
	}
	// Post-run: the archive replaced the root with lowercase `repo`, whose own
	// lookup is case-sensitive, and installed `S -> .`.
	if err := os.Symlink(".", filepath.Join(repo, "S")); err != nil {
		t.Fatal(err)
	}
	// Pre-run snapshot: folding overlay root, case-sensitive `O/Repo`.
	entries := map[string]overlayEntry{
		filepath.Clean(overlayRoot):                                       {kind: overlayEntryDir, folds: true, known: true},
		filepath.Clean(work):                                              {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(work, "t")):                          {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                                             {kind: overlayEntrySymlink, target: target},
		filepath.Clean(filepath.Join(overlayRoot, "Repo")):                {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "Repo", "deep")):        {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "Repo", "deep", "dir")): {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "Repo", "S")):           {kind: overlayEntrySymlink, target: "deep/dir"},
	}
	before := snapshotEntries(entries)
	err := auditUnpackedOverlaySymlinks(localRoot, before)
	if err == nil {
		t.Fatal("replaced directory spelling exempted an unchanged escaping link")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(linkA); !os.IsNotExist(statErr) {
		t.Fatalf("unchanged escaping link %q was left behind: %v", "a", statErr)
	}
}

// TestOverlayLinkConfinedSnapshotUsesStoredSpellingAndFolding pins the snapshot
// resolver: a folded directory canonicalizes a case-variant component to the
// stored spelling, including an ancestor.
func TestOverlayLinkConfinedSnapshotUsesStoredSpellingAndFolding(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	work := filepath.Join(overlayRoot, "A")
	repo := filepath.Join(overlayRoot, "Repo")
	for _, dir := range []string{work, repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := "t/repo/S/../../deep"
	linkA := filepath.Join(work, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	entries := map[string]overlayEntry{
		filepath.Clean(overlayRoot):                        {kind: overlayEntryDir, folds: true, known: true},
		filepath.Clean(work):                               {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(repo):                               {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(work, "t")):           {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                              {kind: overlayEntrySymlink, target: target},
		filepath.Clean(filepath.Join(repo, "S")):           {kind: overlayEntrySymlink, target: "deep/dir"},
		filepath.Clean(filepath.Join(repo, "deep")):        {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(repo, "deep", "dir")): {kind: overlayEntryDir, folds: false, known: true},
	}
	before := snapshotEntries(entries)
	confined, ambiguous := overlayLinkConfinedSnapshot(localRoot, overlayRoot, before, linkA, target)
	if !confined || ambiguous {
		t.Fatalf("snapshot resolution = (confined=%v ambiguous=%v), want (true, false)", confined, ambiguous)
	}
	// With a case-sensitive overlay root the stored `Repo` is not reachable as
	// `repo`, so a deeper target escapes.
	escapingTarget := "t/repo/S/../../../deep"
	if err := os.Remove(linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escapingTarget, linkA); err != nil {
		t.Fatal(err)
	}
	entries[filepath.Clean(overlayRoot)] = overlayEntry{kind: overlayEntryDir, folds: false, known: true}
	entries[filepath.Clean(linkA)] = overlayEntry{kind: overlayEntrySymlink, target: escapingTarget}
	sensitive := snapshotEntries(entries)
	confined, ambiguous = overlayLinkConfinedSnapshot(localRoot, overlayRoot, sensitive, linkA, escapingTarget)
	if confined || ambiguous {
		t.Fatalf("case-sensitive snapshot resolution = (confined=%v ambiguous=%v), want (false, false)", confined, ambiguous)
	}
}

// TestOverlayLinkConfinedSnapshotFlagsAmbiguousCaseMatches pins the fail-closed
// signal for a directory whose folding rule is unknown.
func TestOverlayLinkConfinedSnapshotFlagsAmbiguousCaseMatches(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "s/x/../../../outside"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	before := snapshotEntries(map[string]overlayEntry{
		filepath.Clean(overlayRoot):              {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(repo):                     {kind: overlayEntryDir, folds: false, known: false},
		filepath.Clean(filepath.Join(repo, "S")): {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                    {kind: overlayEntrySymlink, target: target},
	})
	if _, ambiguous := overlayLinkConfinedSnapshot(localRoot, overlayRoot, before, linkA, target); !ambiguous {
		t.Fatal("unknown directory folding did not mark a case-variant match as ambiguous")
	}
}

// TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown pins the
// review finding: an inconclusive folding answer must not exempt an unchanged
// link whose pre-run resolution is ambiguous.
func TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "s/x/../../../outside"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	before := snapshotEntries(map[string]overlayEntry{
		filepath.Clean(overlayRoot):              {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(repo):                     {kind: overlayEntryDir, folds: false, known: false},
		filepath.Clean(filepath.Join(repo, "S")): {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                    {kind: overlayEntrySymlink, target: target},
	})
	err := auditUnpackedOverlaySymlinks(localRoot, before)
	if err == nil {
		t.Fatal("unknown folding exempted an unchanged link with an ambiguous pre-run resolution")
	}
	if _, statErr := os.Lstat(linkA); !os.IsNotExist(statErr) {
		t.Fatalf("ambiguous unchanged link was left installed: %v", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksPreservesKnownPreExistingEscape keeps the
// preservation guarantee for a link that is definitively unconfined before and
// after the run.
func TestAuditUnpackedOverlaySymlinksPreservesKnownPreExistingEscape(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	preexisting := filepath.Join(repo, "escape")
	if err := os.Symlink("/etc/passwd", preexisting); err != nil {
		t.Fatal(err)
	}
	before, err := scanOverlayTree(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err != nil {
		t.Fatalf("pre-existing unconfined link was not preserved: %v", err)
	}
	if _, statErr := os.Lstat(preexisting); statErr != nil {
		t.Fatalf("pre-existing link was removed: %v", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksMatchesFilesystemCaseFolding covers a
// case-insensitive volume when one is available.
func TestAuditUnpackedOverlaySymlinksMatchesFilesystemCaseFolding(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(repo, "case-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "CASE-PROBE")); err != nil {
		t.Skip("filesystem is case-sensitive; case folding cannot be exercised here")
	}
	if err := os.Symlink("..", filepath.Join(repo, "S")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("s/../outside", filepath.Join(repo, "a")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, nil); err == nil {
		t.Fatal("case-variant chain was accepted")
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "a")); !os.IsNotExist(statErr) {
		t.Fatalf("case-variant escaping link %q was left behind: %v", "a", statErr)
	}
}

// TestOverlayDirCaseFoldingProbesInsideTheDirectory keeps the detection helper
// honest and read-only: it inspects the directory's own entries, so it needs no
// write access, and it reports unknown instead of guessing.
func TestOverlayDirCaseFoldingInspectsOnlyExistingEntries(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "overlay")
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Lstat(filepath.Join(dir, "REPO"))
	wantFolds := statErr == nil
	folds, known := overlayDirCaseFolding(dir)
	if !known || folds != wantFolds {
		t.Fatalf("probe = (folds=%v known=%v), want (folds=%v known=true)", folds, known, wantFolds)
	}
	// The classification must not need write access and must not leave anything
	// behind: the audit may not change directory metadata.
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	folds, known = overlayDirCaseFolding(dir)
	if !known || folds != wantFolds {
		t.Fatalf("read-only probe = (folds=%v known=%v), want (folds=%v known=true)", folds, known, wantFolds)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("probe changed the directory: before=%v after=%v", before, after)
	}
	empty := filepath.Join(parent, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, known := overlayDirCaseFolding(empty); known {
		t.Fatal("directory without a usable entry was classified instead of reported unknown")
	}
}

// TestOverlayDirCaseFoldingIgnoresHardLinkPairs covers the finding that a
// same-inode case-variant pair is not proof of case-insensitive lookup.
func TestOverlayDirCaseFoldingIgnoresHardLinkPairs(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(dir, ".aa")
	upper := filepath.Join(dir, ".AA")
	if err := os.Link(target, lower); err != nil {
		t.Skipf("hard links are not supported: %v", err)
	}
	if err := os.Link(target, upper); err != nil {
		t.Skipf("case-insensitive volume: the case variant names the same entry (%v)", err)
	}
	folds, known := overlayDirCaseFolding(dir)
	if !known {
		t.Fatalf("probe was inconclusive for %q", dir)
	}
	if folds {
		t.Fatal("hard-linked case-variant entries were treated as case folding")
	}
}

// TestOverlayLinkConfinedSnapshotUsesUnicodeFolding covers the review finding
// that a folded filesystem compares with Unicode case folding, not lowercasing:
// U+017F (long s) matches ASCII `S`, so the snapshot must find that entry.
func TestOverlayLinkConfinedSnapshotUsesUnicodeFolding(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "t/\u017f/../../deep"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	entries := map[string]overlayEntry{
		filepath.Clean(overlayRoot):                               {kind: overlayEntryDir, folds: true, known: true},
		filepath.Clean(repo):                                      {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(repo, "t")):                  {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                                     {kind: overlayEntrySymlink, target: target},
		filepath.Clean(filepath.Join(overlayRoot, "S")):           {kind: overlayEntrySymlink, target: "deep/dir"},
		filepath.Clean(filepath.Join(overlayRoot, "deep")):        {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "deep", "dir")): {kind: overlayEntryDir, folds: false, known: true},
	}
	before := snapshotEntries(entries)
	confined, ambiguous := overlayLinkConfinedSnapshot(localRoot, overlayRoot, before, linkA, target)
	if !confined || ambiguous {
		t.Fatalf("unicode folding resolution = (confined=%v ambiguous=%v), want (true, false)", confined, ambiguous)
	}
}

// TestScanOverlayTreePreservesDirectoryMtimes covers the review finding that
// snapshotting must not mutate directory metadata: an empty directory keeps the
// mtime the archive restored, both before and after the audit.
func TestScanOverlayTreePreservesDirectoryMtimes(t *testing.T) {
	localRoot := t.TempDir()
	empty := filepath.Join(localRoot, "overlay", "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1_600_000_000, 0)
	if err := os.Chtimes(empty, want, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(want) {
		t.Fatalf("setup mtime = %v, want %v", info.ModTime(), want)
	}
	before, err := scanOverlayTree(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err != nil {
		t.Fatalf("audit of a symlink-free overlay failed: %v", err)
	}
	info, err = os.Stat(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(want) {
		t.Fatalf("scanning or auditing changed the directory mtime: got %v want %v", info.ModTime(), want)
	}
}

// TestOverlayLinkConfinedSnapshotFlagsFullFoldAliasAsAmbiguous covers the review
// finding that ext4 folding also accepts full folds and canonical decomposition
// (U+00DF/`ss`), which Go cannot reproduce: an unmatched component in a folding
// directory must be ambiguous, never definitively absent.
func TestOverlayLinkConfinedSnapshotFlagsFullFoldAliasAsAmbiguous(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "t/\u00df/../../outside"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	before := snapshotEntries(map[string]overlayEntry{
		filepath.Clean(overlayRoot):                               {kind: overlayEntryDir, folds: true, known: true},
		filepath.Clean(repo):                                      {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(repo, "t")):                  {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                                     {kind: overlayEntrySymlink, target: target},
		filepath.Clean(filepath.Join(overlayRoot, "SS")):          {kind: overlayEntrySymlink, target: "deep/dir"},
		filepath.Clean(filepath.Join(overlayRoot, "deep")):        {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "deep", "dir")): {kind: overlayEntryDir, folds: false, known: true},
	})
	if _, ambiguous := overlayLinkConfinedSnapshot(localRoot, overlayRoot, before, linkA, target); !ambiguous {
		t.Fatal("full-fold alias miss was treated as definitively absent")
	}
}

// TestAuditUnpackedOverlaySymlinksRejectsFullFoldAliasEscape is the public
// regression for the same finding: the unchanged link is removed even though the
// snapshot cannot name the alias the filesystem would follow.
func TestAuditUnpackedOverlaySymlinksRejectsFullFoldAliasEscape(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	target := "t/\u00df/../../outside"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "t")); err != nil {
		t.Fatal(err)
	}
	// Post-run: the archive replaced `O/SS` with `SS -> .`.
	if err := os.Symlink(".", filepath.Join(overlayRoot, "SS")); err != nil {
		t.Fatal(err)
	}
	before := snapshotEntries(map[string]overlayEntry{
		filepath.Clean(overlayRoot):                               {kind: overlayEntryDir, folds: true, known: true},
		filepath.Clean(repo):                                      {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(repo, "t")):                  {kind: overlayEntrySymlink, target: ".."},
		filepath.Clean(linkA):                                     {kind: overlayEntrySymlink, target: target},
		filepath.Clean(filepath.Join(overlayRoot, "SS")):          {kind: overlayEntrySymlink, target: "deep/dir"},
		filepath.Clean(filepath.Join(overlayRoot, "deep")):        {kind: overlayEntryDir, folds: false, known: true},
		filepath.Clean(filepath.Join(overlayRoot, "deep", "dir")): {kind: overlayEntryDir, folds: false, known: true},
	})
	err := auditUnpackedOverlaySymlinks(localRoot, before)
	if err == nil {
		t.Fatal("full-fold alias escape was exempted")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(linkA); !os.IsNotExist(statErr) {
		t.Fatalf("unchanged escaping link %q was left behind: %v", "a", statErr)
	}
}
