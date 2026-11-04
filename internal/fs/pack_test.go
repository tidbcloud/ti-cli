package fs

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("safe symlink %q was removed: info=%v err=%v", path, info, statErr)
		}
		got, readErr := os.Readlink(path)
		if readErr != nil || got != target {
			t.Fatalf("Readlink(%q) = %q err=%v, want %q", path, got, readErr, target)
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

// TestAuditUnpackedOverlaySymlinksCatchesEscapingLinkChain covers the review
// finding that lexical containment alone misses a chain: `s -> ..` plus
// `a -> s/../outside` passes the lexical check for each target, but following
// the links reaches localRoot/outside.
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
// review finding that the audit must not delete links the archive did not
// install.
func TestAuditUnpackedOverlaySymlinksPreservesUnchangedPreExistingLinks(t *testing.T) {
	localRoot := t.TempDir()
	other := filepath.Join(localRoot, "overlay", "other")
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "item.txt"), []byte("packed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A user-created link outside the archive's paths, and one that is already
	// absolute before this command runs.
	preexisting := map[string]string{
		filepath.Join(other, "tool"):   "/usr/bin/tool",
		filepath.Join(other, "escape"): "../../../../../../home/victim/.ssh",
	}
	for path, target := range preexisting {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	before, err := scanOverlaySymlinks(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(preexisting) {
		t.Fatalf("snapshot = %#v", before)
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
	// A new escaping link is still rejected while the pre-existing link stays.
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
// covers the review finding that comparing the target text is not enough: an
// unchanged `a -> s/../outside` was confined while `s` was a directory, and the
// archive turns `s` into a permitted link, so `a` now escapes.
func TestAuditUnpackedOverlaySymlinksRejectsUnchangedLinkWhoseDependencyMoved(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(filepath.Join(repo, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("s/../outside", filepath.Join(repo, "a")); err != nil {
		t.Fatal(err)
	}
	before, err := scanOverlaySymlinks(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("snapshot = %#v", before)
	}
	// The archive replaces the directory `s` with a link to the overlay root.
	if err := os.RemoveAll(filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "s")); err != nil {
		t.Fatal(err)
	}
	err = auditUnpackedOverlaySymlinks(localRoot, before)
	if err == nil {
		t.Fatal("link that became unsafe through an installed dependency was accepted")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "a")); !os.IsNotExist(statErr) {
		t.Fatalf("unchanged escaping link %q was left behind: %v", "a", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "s")); statErr != nil {
		t.Fatalf("confined link %q was removed: %v", "s", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksMatchesFilesystemCaseFolding covers the review
// finding that map-key lookups do not follow a case-insensitive volume, where an
// archive can install `S -> ..` and reach it as `s`.
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
	err := auditUnpackedOverlaySymlinks(localRoot, nil)
	if err == nil {
		t.Fatal("case-variant chain was accepted")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "a")); !os.IsNotExist(statErr) {
		t.Fatalf("case-variant escaping link %q was left behind: %v", "a", statErr)
	}
}

// TestOverlayLinkGraphFoldsSnapshotNamesPerDirectory covers the review finding
// that snapshot lookups must follow each directory's own folding rule: the
// overlay root can fold names while its parent does not, and an entry can be
// reached through a different spelling than the one the archive installed.
func TestOverlayLinkGraphFoldsSnapshotNamesPerDirectory(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-run graph: `repo/t -> ..` and the overlay root contains `S`; the
	// overlay root folds names, `repo` does not.
	snapshot := map[string]string{
		filepath.Clean(filepath.Join(repo, "t")):        "..",
		filepath.Clean(filepath.Join(overlayRoot, "S")): "deep/dir",
	}
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink("t/s/../../outside", linkA); err != nil {
		t.Fatal(err)
	}
	probe := func(dir string) (bool, bool) {
		if filepath.Clean(dir) == filepath.Clean(overlayRoot) {
			return true, true
		}
		return false, true
	}
	graph := newOverlayLinkGraph(snapshot, probe)
	confined, ambiguous := overlayLinkConfined(localRoot, overlayRoot, graph, linkA, "t/s/../../outside")
	if !confined || ambiguous {
		t.Fatalf("folded snapshot resolution = (confined=%v ambiguous=%v), want (true, false)", confined, ambiguous)
	}
	sensitive := newOverlayLinkGraph(snapshot, func(string) (bool, bool) { return false, true })
	confined, ambiguous = overlayLinkConfined(localRoot, overlayRoot, sensitive, linkA, "t/s/../../outside")
	if confined || ambiguous {
		t.Fatalf("case-sensitive snapshot resolution = (confined=%v ambiguous=%v), want (false, false)", confined, ambiguous)
	}
}

// TestOverlayLinkConfinedFlagsAmbiguousCaseMatches pins the fail-closed signal:
// when a case variant matches but the directory's folding rule is unknown, the
// answer must be reported as ambiguous instead of silently deciding.
func TestOverlayLinkConfinedFlagsAmbiguousCaseMatches(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]string{filepath.Clean(filepath.Join(repo, "S")): ".."}
	target := "s/x/../../../outside"
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	unknown := newOverlayLinkGraph(snapshot, func(string) (bool, bool) { return false, false })
	if _, ambiguous := overlayLinkConfined(localRoot, overlayRoot, unknown, linkA, target); !ambiguous {
		t.Fatal("unknown directory folding did not mark a case-variant match as ambiguous")
	}
	known := newOverlayLinkGraph(snapshot, func(string) (bool, bool) { return false, true })
	if _, ambiguous := overlayLinkConfined(localRoot, overlayRoot, known, linkA, target); ambiguous {
		t.Fatal("known case-sensitive directory reported an ambiguous authoritative match")
	}
}

// TestOverlayDirCaseFoldingProbesInsideTheDirectory keeps the detection helper
// honest and read-only: it inspects the directory's own entries, so it needs no
// write access, and it reports unknown instead of guessing.
func TestOverlayDirCaseFoldingProbesInsideTheDirectory(t *testing.T) {
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
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	folds, known = overlayDirCaseFolding(dir)
	if !known || folds != wantFolds {
		t.Fatalf("read-only probe = (folds=%v known=%v), want (folds=%v known=true)", folds, known, wantFolds)
	}
	empty := filepath.Join(parent, "empty")
	if err := os.MkdirAll(empty, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(empty, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root can always create the probe file, so the unknown path cannot be exercised")
	}
	if _, known := overlayDirCaseFolding(empty); known {
		t.Fatal("unwritable empty directory was classified instead of reported unknown")
	}
}

// TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown pins the
// review finding: an inconclusive folding answer must not exempt an unchanged
// link whose pre-run resolution is ambiguous.
func TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown(t *testing.T) {
	localRoot := t.TempDir()
	repo := filepath.Join(localRoot, "overlay", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(repo, "S")); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink("s/x/../../../outside", linkA); err != nil {
		t.Fatal(err)
	}
	before, err := scanOverlaySymlinks(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	// The archive installs `repo/s`; combined with the unknown folding rule this
	// makes the pre-run answer ambiguous while the live resolution escapes. On a
	// case-insensitive volume `repo/s` already names `repo/S`, so EEXIST is fine.
	if err := os.Symlink("..", filepath.Join(repo, "s")); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	previous := overlayCaseFoldingProbe
	overlayCaseFoldingProbe = func(string) (bool, bool) { return false, false }
	t.Cleanup(func() { overlayCaseFoldingProbe = previous })
	err = auditUnpackedOverlaySymlinks(localRoot, before)
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
	before, err := scanOverlaySymlinks(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	previous := overlayCaseFoldingProbe
	overlayCaseFoldingProbe = func(string) (bool, bool) { return false, true }
	t.Cleanup(func() { overlayCaseFoldingProbe = previous })
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err != nil {
		t.Fatalf("pre-existing unconfined link was not preserved: %v", err)
	}
	if _, statErr := os.Lstat(preexisting); statErr != nil {
		t.Fatalf("pre-existing link was removed: %v", statErr)
	}
}

// TestOverlayLinkGraphCanonicalizesAncestorCaseAliases covers the review finding
// that a case alias in a regular ancestor component (repo vs Repo) must resolve
// the way the folding ancestor directory does, not through the final directory's
// rule.
func TestOverlayLinkGraphCanonicalizesAncestorCaseAliases(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "Repo")
	work := filepath.Join(overlayRoot, "A")
	for _, dir := range []string{filepath.Join(repo, "deep", "dir"), work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("..", filepath.Join(work, "t")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("deep/dir", filepath.Join(repo, "S")); err != nil {
		t.Fatal(err)
	}
	target := "t/repo/S/../../../outside"
	linkA := filepath.Join(work, "a")
	if err := os.Symlink(target, linkA); err != nil {
		t.Fatal(err)
	}
	snapshot, err := scanOverlaySymlinks(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Only the overlay root folds; the subdirectories are case-sensitive.
	probe := func(dir string) (bool, bool) {
		return filepath.Clean(dir) == filepath.Clean(overlayRoot), true
	}
	graph := newOverlayLinkGraph(snapshot, probe)
	confined, ambiguous := overlayLinkConfined(localRoot, overlayRoot, graph, linkA, target)
	if !confined || ambiguous {
		t.Fatalf("ancestor case alias resolution = (confined=%v ambiguous=%v), want (true, false)", confined, ambiguous)
	}
}

// TestOverlayDirCaseFoldingIgnoresHardLinkPairs covers the review finding that a
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
