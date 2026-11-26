package fs

import (
	"path/filepath"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// TestMountPackArchivePathMapping pins the replica of the companion's
// resolvePackPath with literal expected strings, because seedOverlay builds its
// fixture with the same helpers the replay check uses: without literal vectors a
// drift between ti and the companion would satisfy both sides.
func TestMountPackArchivePathMapping(t *testing.T) {
	for _, tc := range []struct {
		remoteRoot string
		raw        string
		want       string
	}{
		{"/", "/src", "/src"},
		{"/", "/", "/"},
		{"", "/src", "/src"},
		{"/workspace", "/workspace", "/"},
		{"/workspace", "/workspace/", "/"},
		{"/workspace", "/workspace/src", "/src"},
		{"/workspace", "/workspace/src/deep", "/src/deep"},
		{"/workspace/", "/workspace/src", "/src"},
		{" /workspace ", " /workspace/src ", "/src"},
		// A sibling with the root as a prefix is not under it.
		{"/workspace", "/workspacex/src", "/workspacex/src"},
		// An absolute value outside the mount root is already mount-local.
		{"/workspace", "/other/src", "/other/src"},
		// A relative value always resolves under the mount root.
		{"/workspace", "src", "/src"},
		{"/", "src", "/src"},
	} {
		if got := mountPackArchivePath(tc.remoteRoot, tc.raw); got != tc.want {
			t.Fatalf("mountPackArchivePath(%q, %q) = %q, want %q", tc.remoteRoot, tc.raw, got, tc.want)
		}
	}
}

// TestMountPackOverlayPathMapping pins where the companion packs from: the local
// root's overlay directory plus the archive path.
func TestMountPackOverlayPathMapping(t *testing.T) {
	root := filepath.Join("/cache", "root-abc")
	for _, tc := range []struct {
		archivePath string
		want        string
	}{
		{"/", filepath.Join(root, "overlay")},
		{"/src", filepath.Join(root, "overlay", "src")},
		{"/src/deep", filepath.Join(root, "overlay", "src", "deep")},
	} {
		if got := mountPackOverlayPath(root, tc.archivePath); got != tc.want {
			t.Fatalf("mountPackOverlayPath(%q, %q) = %q, want %q", root, tc.archivePath, got, tc.want)
		}
	}
}

// TestMountRejectsDotSegmentsInAbsolutePackPaths covers the values the previous
// check skipped: an absolute value is still mapped onto an archive path, so "."
// and ".." are refused there too, or ti would replay a value the companion
// rejects inside the pack that runs after the detach.
func TestMountRejectsDotSegmentsInAbsolutePackPaths(t *testing.T) {
	for _, bad := range []string{"/a/../b", "/a/./b", "/..", "/.", "/a/.."} {
		t.Run(bad, func(t *testing.T) {
			_, err := normalizeMountOptions(MountFileSystemOptions{
				Profile:      dataProfile(),
				Driver:       "fuse",
				MountProfile: "portable",
				PackPaths:    []string{bad},
			})
			if apperr.CodeFor(err) != "fs.invalid_pack_path" {
				t.Fatalf("normalize error = %v, want fs.invalid_pack_path for %q", err, bad)
			}
		})
	}
}

// TestMountAcceptsAbsolutePackPathShapes the pack itself accepts keeps the valid
// forms working: the root, repeated slashes below it, and a trailing slash.
func TestMountAcceptsAbsolutePackPathShapes(t *testing.T) {
	for _, good := range []string{"/", "/a//b", "/src/", "/src/deep"} {
		t.Run(good, func(t *testing.T) {
			if _, err := normalizeMountOptions(MountFileSystemOptions{
				Profile:      dataProfile(),
				Driver:       "fuse",
				MountProfile: "portable",
				PackPaths:    []string{good},
			}); err != nil {
				t.Fatalf("normalize rejected %q: %v", good, err)
			}
		})
	}
}
