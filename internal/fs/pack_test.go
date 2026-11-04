package fs

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

// TestAuditUnpackedOverlaySymlinksRemovesEscapingLinks covers the public unpack
// invariant: after the companion extracts a pack, escaping links are removed and
// the command fails closed.
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
	err := auditUnpackedOverlaySymlinks(localRoot)
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

// TestAuditUnpackedOverlaySymlinksCatchesEscapingLinkChain covers a chain where
// each single target looks lexical-containment-safe but following the links
// leaves the overlay.
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
	err := auditUnpackedOverlaySymlinks(localRoot)
	if err == nil {
		t.Fatal("escaping symlink chain was accepted")
	}
	if _, statErr := os.Lstat(filepath.Join(repo, "a")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping chain link %q was left behind: %v", "a", statErr)
	}
	// The intermediate hop only points at the overlay root, which the policy
	// keeps; the link that escapes is the only one removed.
	if _, statErr := os.Lstat(filepath.Join(repo, "s")); statErr != nil {
		t.Fatalf("in-overlay chain link %q was removed: %v", "s", statErr)
	}
}

// TestAuditUnpackedOverlaySymlinksTreatsOverlayAsTiOwned pins the scope decision:
// the overlay is ti-managed state, so an escaping link is unsafe regardless of
// which run created it, and the audit does not try to reconstruct history.
func TestAuditUnpackedOverlaySymlinksTreatsOverlayAsTiOwned(t *testing.T) {
	localRoot := t.TempDir()
	other := filepath.Join(localRoot, "overlay", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	preexisting := filepath.Join(other, "tool")
	if err := os.Symlink("/usr/bin/tool", preexisting); err != nil {
		t.Fatal(err)
	}
	err := auditUnpackedOverlaySymlinks(localRoot)
	if err == nil {
		t.Fatal("pre-existing escaping link was accepted")
	}
	if _, statErr := os.Lstat(preexisting); !os.IsNotExist(statErr) {
		t.Fatalf("escaping link was left in the ti-managed overlay: %v", statErr)
	}
	if err := os.Symlink("target", filepath.Join(other, "relative")); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot); err != nil {
		t.Fatalf("in-overlay link rejected: %v", err)
	}
}

func TestAuditUnpackedOverlaySymlinksAcceptsSafeAndAbsentOverlays(t *testing.T) {
	if err := auditUnpackedOverlaySymlinks(""); err == nil {
		t.Fatal("empty local root silently disabled the audit")
	}
	missing := filepath.Join(t.TempDir(), "no-overlay")
	if err := auditUnpackedOverlaySymlinks(missing); err != nil {
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
	if err := auditUnpackedOverlaySymlinks(localRoot); err != nil {
		t.Fatalf("safe overlay rejected: %v", err)
	}
	symlinkedOverlay := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(symlinkedOverlay, "overlay")); err != nil {
		t.Skipf("symlinks are not supported: %v", err)
	}
	if err := auditUnpackedOverlaySymlinks(symlinkedOverlay); err == nil {
		t.Fatal("symlinked overlay accepted")
	}
}

// TestAuditUnpackedOverlaySymlinksDoesNotMutateSafeDirectories covers the mtime
// contract: auditing a symlink-free overlay must not create, delete, or touch
// anything, so an empty directory keeps its archived timestamp.
func TestAuditUnpackedOverlaySymlinksDoesNotMutateSafeDirectories(t *testing.T) {
	localRoot := t.TempDir()
	empty := filepath.Join(localRoot, "overlay", "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1_600_000_000, 0)
	if err := os.Chtimes(empty, want, want); err != nil {
		t.Fatal(err)
	}
	if err := auditUnpackedOverlaySymlinks(localRoot); err != nil {
		t.Fatalf("audit of a symlink-free overlay failed: %v", err)
	}
	info, err := os.Stat(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(want) {
		t.Fatalf("audit changed the directory mtime: got %v want %v", info.ModTime(), want)
	}
}

// TestAuditUnpackedOverlaySymlinksCleansUpBeforeReportingWalkErrors covers the
// review finding that a traversal failure must not leave a detected escape
// installed: the collected links are removed even though the walk is incomplete,
// and the command still fails closed.
func TestAuditUnpackedOverlaySymlinksCleansUpBeforeReportingWalkErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions, so the traversal failure cannot be exercised")
	}
	localRoot := t.TempDir()
	overlayRoot := filepath.Join(localRoot, "overlay")
	repo := filepath.Join(overlayRoot, "a")
	unreadable := filepath.Join(overlayRoot, "z")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(repo, "escape")
	if err := os.Symlink("/etc/passwd", escape); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	err := auditUnpackedOverlaySymlinks(localRoot)
	if err == nil {
		t.Fatal("incomplete audit was reported as success")
	}
	if code := apperr.CodeFor(err); code != "fs.unpack_audit_incomplete" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Lstat(escape); !os.IsNotExist(statErr) {
		t.Fatalf("detected escape survived an incomplete walk: %v", statErr)
	}
	if !strings.Contains(err.Error(), "removed 1 unsafe symlink") {
		t.Fatalf("error does not report the cleanup: %v", err)
	}
}
