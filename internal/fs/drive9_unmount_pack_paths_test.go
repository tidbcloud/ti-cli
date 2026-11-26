package fs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

// mountWithPackPaths mounts through the fake companion with a real local
// overlay root and returns the service, mount path, calls file and local root.
func mountWithPackPaths(t *testing.T, packPaths []string, tweak func(*MountFileSystemOptions)) (Service, string, string, string) {
	t.Helper()
	home := t.TempDir()
	localRoot := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	opts := MountFileSystemOptions{
		Profile:      dataProfile(),
		MountPath:    mountPath,
		Driver:       "fuse",
		MountProfile: "portable",
		LocalRoot:    localRoot,
		PackPaths:    packPaths,
	}
	if tweak != nil {
		tweak(&opts)
	}
	if _, err := service.MountFileSystem(context.Background(), opts); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	return service, mountPath, recordPath, localRoot
}

// seedOverlay creates the archived path inside the local overlay the companion
// packs, mirroring <local-root>/overlay/<path>.
func seedOverlay(t *testing.T, localRoot, packPath string) {
	t.Helper()
	overlayPath := mountPackOverlayPath(localRoot, mountPackArchivePath("/", packPath))
	if err := os.MkdirAll(overlayPath, 0o755); err != nil {
		t.Fatalf("seed overlay %s: %v", overlayPath, err)
	}
}

func packPathArgs(args []string) []string {
	var out []string
	for i, arg := range args {
		if arg == "--pack-path" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func umountCallArgs(t *testing.T, recordPath string) []string {
	t.Helper()
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "umount" {
			return call.Args
		}
	}
	t.Fatalf("no umount call recorded")
	return nil
}

func hasRuntimeCheck(checks []MountRuntimeCheck, name, status string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

// TestMountRejectsPackPathOnWebDAV keeps the failing case out of unmount: a
// WebDAV mount has no local overlay, so the automatic pack has nothing to read.
func TestMountRejectsPackPathOnWebDAV(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:      dataProfile(),
		MountPath:    filepath.Join(t.TempDir(), "mnt"),
		Driver:       "webdav",
		MountProfile: "portable",
		PackPaths:    []string{"/src"},
	})
	if apperr.CodeFor(err) != "fs.pack_path_requires_fuse" {
		t.Fatalf("mount error = %v, want fs.pack_path_requires_fuse", err)
	}
	if _, statErr := os.Stat(recordPath); statErr == nil {
		// A rejected mount must never reach the companion, so nothing may be
		// recorded; an absent file is the expected outcome.
		if calls := readFakeDrive9Calls(t, recordPath); len(calls) != 0 {
			t.Fatalf("a rejected mount must not reach the companion: %#v", calls)
		}
	}
}

// TestMountRejectsPackPathFormsThePackRefuses mirrors the companion's
// joinArchivePath so the failure happens before anything is mounted.
func TestMountRejectsPackPathFormsThePackRefuses(t *testing.T) {
	for _, bad := range []string{".", "..", "a//b", "a/./b", `a\b`} {
		t.Run(bad, func(t *testing.T) {
			_, err := normalizeMountOptions(MountFileSystemOptions{
				Profile:      dataProfile(),
				Driver:       "fuse",
				MountProfile: "portable",
				PackPaths:    []string{bad},
			})
			if apperr.CodeFor(err) != "fs.invalid_pack_path" {
				t.Fatalf("normalize error = %v, want fs.invalid_pack_path", err)
			}
		})
	}
}

// TestMountRecordsPackPathsOnLocator pins the normalized values and the local
// root that unmount needs to verify them.
func TestMountRecordsPackPathsOnLocator(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src", " node_modules ", "/src", ""}, nil)
	home, err := service.homeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read locator: %v", err)
	}
	wantPack := []string{"/src", "node_modules"}
	if !reflect.DeepEqual(locator.PackPaths, wantPack) {
		t.Fatalf("locator pack paths = %#v, want %#v", locator.PackPaths, wantPack)
	}
	if locator.LocalRoot != localRoot {
		t.Fatalf("locator local root = %q, want %q", locator.LocalRoot, localRoot)
	}
}

// TestUnmountForwardsOnlyPathsStillInTheOverlay is the regression this change
// exists for: a path that never made it into the overlay is skipped with a
// check instead of failing the pack after the mount is detached.
func TestUnmountForwardsOnlyPathsStillInTheOverlay(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src", "node_modules"}, nil)
	seedOverlay(t, localRoot, "/src")

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); !reflect.DeepEqual(got, []string{"/src"}) {
		t.Fatalf("umount --pack-path = %#v, want only /src", got)
	}
	if !hasRuntimeCheck(result.Checks, "pack_path", "skipped") {
		t.Fatalf("missing skipped pack_path check: %#v", result.Checks)
	}
}

// TestUnmountSkipsReplayWithoutRecordedLocalRoot covers a locator that cannot
// verify its paths: replaying blindly could fail after the detach.
func TestUnmountSkipsReplayWithoutRecordedLocalRoot(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, []string{"/src"}, nil)
	home, err := service.homeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	// Rewrite the locator the mount wrote, dropping the local root: the paths
	// can no longer be verified, so replay must be skipped instead of failing
	// the pack after the detach.
	locator, _, readErr := mountlocator.Read(home, mountPath)
	if readErr != nil {
		t.Fatalf("read locator: %v", readErr)
	}
	locator.LocalRoot = ""
	if _, writeErr := mountlocator.Write(home, locator); writeErr != nil {
		t.Fatalf("write locator: %v", writeErr)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none without a recorded local root", got)
	}
	if !hasRuntimeCheck(result.Checks, "pack_path", "skipped") {
		t.Fatalf("missing skipped pack_path check: %#v", result.Checks)
	}
}

// TestUnmountLegacyLocatorSendsNoPackFlag keeps locators written before this
// change on the exact same argv.
func TestUnmountLegacyLocatorSendsNoPackFlag(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, nil, nil)
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none", got)
	}
}

// TestUnmountSkipsPackPathsWhenAutoPackDisabled keeps --no-auto-pack
// authoritative.
func TestUnmountSkipsPackPathsWhenAutoPackDisabled(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"}, nil)
	seedOverlay(t, localRoot, "/src")
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:    dataProfile(),
		MountPath:  mountPath,
		NoAutoPack: true,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none", got)
	}
}

// TestUnmountFailureAfterDrainNamesTheRecovery keeps the failure actionable:
// the mount may already be detached, so a bare retry is not the only option.
func TestUnmountFailureAfterDrainNamesTheRecovery(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"}, nil)
	seedOverlay(t, localRoot, "/src")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")
	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if apperr.CodeFor(err) != "fs.unmount_failed_after_drain" {
		t.Fatalf("unmount error = %v, want fs.unmount_failed_after_drain", err)
	}
	for _, want := range []string{"--no-auto-pack", "--ignore-absent", "may already be detached"} {
		if !strings.Contains(apperr.MessageFor(err), want) {
			t.Fatalf("unmount error %q missing %q", apperr.MessageFor(err), want)
		}
	}
}

// TestUnmountForceWithMissingOverlayPathSucceeds covers --force with a path the
// overlay no longer holds: the skip must happen before the strict umount.
func TestUnmountForceWithMissingOverlayPathSucceeds(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, []string{"/gone"}, nil)
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
		Force:     true,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none", got)
	}
}

// TestMergeMountPackPathsDropsBlanksAndDuplicates pins the one normalizer the
// mount pipeline owns.
func TestMergeMountPackPathsDropsBlanksAndDuplicates(t *testing.T) {
	got := mergeMountPackPaths([]string{" /a ", "", "/a", "/b", "  "})
	want := []string{"/a", "/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeMountPackPaths = %#v, want %#v", got, want)
	}
}

// TestUnmountKeepsLocatorWhenThePackFails keeps the retry path: the mount may
// already be detached, so the record must survive for a --no-auto-pack retry.
func TestUnmountKeepsLocatorWhenThePackFails(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"}, nil)
	seedOverlay(t, localRoot, "/src")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	}); apperr.CodeFor(err) != "fs.unmount_failed_after_drain" {
		t.Fatalf("unmount error = %v, want fs.unmount_failed_after_drain", err)
	}
	home, err := service.homeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	if _, _, readErr := mountlocator.Read(home, mountPath); readErr != nil {
		t.Fatalf("locator must survive a failed unmount: %v", readErr)
	}
}

// TestUnmountSkipsReplayForANonFUSELocator covers a locator written with paths
// but a WebDAV driver: the mount has no overlay, so nothing may be replayed.
func TestUnmountSkipsReplayForANonFUSELocator(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"}, nil)
	seedOverlay(t, localRoot, "/src")
	home, err := service.homeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	locator, _, readErr := mountlocator.Read(home, mountPath)
	if readErr != nil {
		t.Fatalf("read locator: %v", readErr)
	}
	locator.Driver = "webdav"
	if _, writeErr := mountlocator.Write(home, locator); writeErr != nil {
		t.Fatalf("write locator: %v", writeErr)
	}
	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none for a WebDAV locator", got)
	}
	if !hasRuntimeCheck(result.Checks, "pack_path", "skipped") {
		t.Fatalf("missing skipped pack_path check: %#v", result.Checks)
	}
}

// TestDryRunUnmountReportsPackReplay keeps --dry-run honest about the automatic
// pack: it reports exactly what normal execution would replay or skip.
func TestDryRunUnmountReportsPackReplay(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src", "node_modules"}, nil)
	seedOverlay(t, localRoot, "/src")
	result, err := service.DryRunUnmountFileSystem(context.Background(), "ti fs unmount-file-system", UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	var pack *dryrun.Check
	for i := range result.Checks {
		if result.Checks[i].Name == "auto_pack_paths" {
			pack = &result.Checks[i]
		}
	}
	if pack == nil {
		t.Fatalf("dry run missing auto_pack_paths check: %#v", result.Checks)
	}
	if pack.Status != "passed" || !strings.Contains(pack.Message, "/src") {
		t.Fatalf("auto_pack_paths = %#v, want /src replayed", *pack)
	}
	if !strings.Contains(pack.Message, "node_modules") || !strings.Contains(pack.Message, "not in the local overlay") {
		t.Fatalf("auto_pack_paths = %#v, want the skipped path named", *pack)
	}
}

// TestUnmountReplaysPackPathsForAnExplicitArchive covers the interaction the
// review named: --no-auto-pack suppresses the automatic archive, but an
// explicit --pack-archive-path still packs, so the recorded paths must ride
// along with it.
func TestUnmountReplaysPackPathsForAnExplicitArchive(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"}, nil)
	seedOverlay(t, localRoot, "/src")
	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:         dataProfile(),
		MountPath:       mountPath,
		NoAutoPack:      true,
		PackArchivePath: "/exports/workspace.tar.gz",
	})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	args := umountCallArgs(t, recordPath)
	if got := packPathArgs(args); !reflect.DeepEqual(got, []string{"/src"}) {
		t.Fatalf("umount --pack-path = %#v, want /src beside the explicit archive", got)
	}
	if !hasArg(args, "--pack") {
		t.Fatalf("umount argv missing the explicit archive: %#v", args)
	}
	for _, check := range result.Checks {
		if check.Name == "pack_path" && check.Status == "skipped" {
			t.Fatalf("explicit archive replay must not be skipped: %#v", result.Checks)
		}
	}
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
