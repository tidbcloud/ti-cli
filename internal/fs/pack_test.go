package fs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

// TestOverlayLinkGraphFoldsSnapshotNames covers the review finding that the
// pre-unpack snapshot used case-sensitive map keys while live resolution used
// the volume: on a case-insensitive volume both must agree. The snapshot graph is
// built synthetically so the difference is visible on any filesystem.
func TestOverlayLinkGraphFoldsSnapshotNames(t *testing.T) {
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// The pre-run state, as the snapshot recorded it: `repo/t -> ..` and
	// `S -> deep/dir`, referenced through the lower-case spelling used by `a`.
	snapshot := map[string]string{
		filepath.Clean(filepath.Join(repo, "t")):        "..",
		filepath.Clean(filepath.Join(overlayRoot, "S")): "deep/dir",
	}
	linkA := filepath.Join(repo, "a")
	if err := os.Symlink("t/s/../../outside", linkA); err != nil {
		t.Fatal(err)
	}
	folded := newOverlayLinkGraph(snapshot, true)
	if !overlayLinkConfined(localRoot, overlayRoot, folded, linkA, "t/s/../../outside") {
		t.Fatal("case-folded snapshot resolution reported a confined pre-run link as escaping")
	}
	exact := newOverlayLinkGraph(snapshot, false)
	if overlayLinkConfined(localRoot, overlayRoot, exact, linkA, "t/s/../../outside") {
		t.Fatal("exact-case snapshot resolution followed the case-variant link")
	}
}

// TestOverlayCaseFoldingProbesWithoutWriting keeps the case-detection helper
// honest and read-only: it must agree with the filesystem it probes, and it must
// not need write access to the overlay (review finding: a 0555 overlay still has
// to be classified, not silently treated as case-sensitive).
func TestOverlayCaseFoldingProbesWithoutWriting(t *testing.T) {
	parent := t.TempDir()
	overlayRoot := filepath.Join(parent, "overlay")
	if err := os.MkdirAll(overlayRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Lstat(filepath.Join(parent, strings.ToUpper("overlay")))
	wantFolds := statErr == nil
	folds, known := overlayCaseFolding(overlayRoot)
	if !known {
		t.Fatalf("case folding probe was inconclusive for %q", overlayRoot)
	}
	if folds != wantFolds {
		t.Fatalf("folds = %v, want %v", folds, wantFolds)
	}
	if err := os.Chmod(overlayRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(overlayRoot, 0o755) })
	folds, known = overlayCaseFolding(overlayRoot)
	if !known || folds != wantFolds {
		t.Fatalf("read-only overlay probe = (%v, %v), want (%v, true)", folds, known, wantFolds)
	}
}

// TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown pins the
// review finding: an inconclusive probe must not reinstate the exemption for an
// unchanged link that is unconfined now.
func TestAuditUnpackedOverlaySymlinksFailsClosedWhenCaseFoldingIsUnknown(t *testing.T) {
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
	overlayCaseFoldingProbe = func(string) (bool, bool) { return false, false }
	t.Cleanup(func() { overlayCaseFoldingProbe = previous })
	err = auditUnpackedOverlaySymlinks(localRoot, before)
	if err == nil {
		t.Fatal("unknown case folding still exempted an unconfined unchanged link")
	}
	if code := apperr.CodeFor(err); code != "fs.unsafe_pack_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(preexisting); !os.IsNotExist(statErr) {
		t.Fatalf("unconfined unchanged link was left installed: %v", statErr)
	}
	// With a known case-sensitive volume the same link is pre-existing local
	// state and must be preserved.
	if err := os.Symlink("/etc/passwd", preexisting); err != nil {
		t.Fatal(err)
	}
	overlayCaseFoldingProbe = func(string) (bool, bool) { return false, true }
	if err := auditUnpackedOverlaySymlinks(localRoot, before); err != nil {
		t.Fatalf("pre-existing unconfined link was not preserved on a known volume: %v", err)
	}
	if _, statErr := os.Lstat(preexisting); statErr != nil {
		t.Fatalf("pre-existing link was removed: %v", statErr)
	}
}
