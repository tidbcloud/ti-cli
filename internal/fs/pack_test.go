package fs

import (
	"archive/tar"
	"bytes"
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
	if err := auditUnpackedOverlaySymlinks(""); err != nil {
		t.Fatalf("empty local root rejected: %v", err)
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
