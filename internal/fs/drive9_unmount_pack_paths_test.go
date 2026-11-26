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
func mountWithPackPaths(t *testing.T, packPaths []string) (Service, string, string, string) {
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
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src", " node_modules ", "/src", ""})
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

// TestMountRecordsRootWithoutAutoUnpack covers the state that made --pack-path
// inert: --no-auto-unpack means nothing unpacks, so the audit decision has no
// root, but the mount still packs at unmount and needs the overlay root to verify
// the recorded paths.
func TestMountRecordsRootWithoutAutoUnpack(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
		MountProfile: "portable", PackPaths: []string{"/src"}, NoAutoUnpack: true,
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read locator: %v", err)
	}
	if strings.TrimSpace(locator.LocalRoot) == "" {
		t.Fatalf("locator local root is empty, so unmount cannot verify --pack-path")
	}
	seedOverlay(t, locator.LocalRoot, "/src")

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountCallArgs(t, recordPath)); !reflect.DeepEqual(got, []string{"/src"}) {
		t.Fatalf("umount --pack-path = %#v, want /src", got)
	}
	for _, check := range result.Checks {
		if check.Name == "pack_path" {
			t.Fatalf("unexpected pack_path check: %#v", result.Checks)
		}
	}
}

// TestUnmountForwardsOnlyPathsStillInTheOverlay is the regression this change
// exists for: a path that never made it into the overlay is skipped with a
// check instead of failing the pack after the mount is detached.
func TestUnmountForwardsOnlyPathsStillInTheOverlay(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src", "node_modules"})
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
	// The text output is the only place a plain `ti fs umount` user sees the
	// skip, so pin it: dropping the renderer would hide the path silently.
	human := result.Human()
	if !strings.Contains(human, "pack_path: skipped") {
		t.Fatalf("text output does not report the skipped path:\n%s", human)
	}
	if strings.Contains(human, "drain: drained") {
		t.Fatalf("text output repeats the drain status as a check:\n%s", human)
	}
}

// TestUnmountSkipsReplayWithoutRecordedLocalRoot covers a locator that cannot
// verify its paths: replaying blindly could fail after the detach.
func TestUnmountSkipsReplayWithoutRecordedLocalRoot(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, []string{"/src"})
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
	for _, check := range result.Checks {
		if check.Name == "pack_path" && check.Status == "skipped" && strings.Contains(check.Message, "did not record the local overlay root it packs from") {
			return
		}
	}
	t.Fatalf("skipped pack_path check does not name the missing overlay root: %#v", result.Checks)
}

// TestUnmountLegacyLocatorSendsNoPackFlag keeps locators written before this
// change on the exact same argv.
func TestUnmountLegacyLocatorSendsNoPackFlag(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, nil)
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
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"})
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
// the mount may already be detached, so a bare retry is not the only option. The
// drain really ran here, so the warning that the writes are safe is earned.
func TestUnmountFailureAfterDrainNamesTheRecovery(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"})
	seedOverlay(t, localRoot, "/src")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")
	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if apperr.CodeFor(err) != "fs.unmount_failed_after_drain" {
		t.Fatalf("unmount error = %v, want fs.unmount_failed_after_drain", err)
	}
	if code := apperr.ExitCodeFor(err); code != 1 {
		t.Fatalf("exit code = %d, want 1: a failed unmount is not an authentication failure", code)
	}
	for _, want := range []string{"--no-auto-pack", "may already be detached"} {
		if !strings.Contains(apperr.MessageFor(err), want) {
			t.Fatalf("unmount error %q missing %q", apperr.MessageFor(err), want)
		}
	}
	// The mount is still listed, so dropping its record would orphan it.
	if !strings.Contains(apperr.MessageFor(err), "would orphan") {
		t.Fatalf("unmount error %q must warn before recommending --ignore-absent for a listed mount", apperr.MessageFor(err))
	}
	if strings.Contains(apperr.MessageFor(err), "to drop the mount record") {
		t.Fatalf("unmount error %q recommends dropping the record of a listed mount", apperr.MessageFor(err))
	}
}

// TestUnmountFailureAfterDetachedMountOffersIgnoreAbsent covers the other half of
// the same advice: once the kernel no longer lists the mount, dropping the record
// is the way out and the message must say so.
func TestUnmountFailureAfterDetachedMountOffersIgnoreAbsent(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"})
	seedOverlay(t, localRoot, "/src")
	service.mountPointActive = func(string) (bool, error) { return false, nil }
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")
	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	})
	if apperr.CodeFor(err) != "fs.unmount_failed_after_drain" {
		t.Fatalf("unmount error = %v, want fs.unmount_failed_after_drain", err)
	}
	if !strings.Contains(apperr.MessageFor(err), "--ignore-absent` to drop the mount record") {
		t.Fatalf("unmount error %q must lead with --ignore-absent for a detached mount", apperr.MessageFor(err))
	}
	if strings.Contains(apperr.MessageFor(err), "would orphan") {
		t.Fatalf("unmount error %q argues against the flag it should recommend for a detached mount", apperr.MessageFor(err))
	}
}

// TestUnmountForceFailureKeepsTheCompanionError covers --force, where the drain
// gate is skipped: the message must not claim a drain that never ran, and the
// exit code must stay the companion's own.
func TestUnmountForceFailureKeepsTheCompanionError(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"})
	seedOverlay(t, localRoot, "/src")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")
	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
		Force:     true,
	})
	if apperr.CodeFor(err) != "fs.companion_failed" {
		t.Fatalf("unmount error = %v, want the companion's own fs.companion_failed", err)
	}
	if strings.Contains(apperr.MessageFor(err), "drain succeeded") {
		t.Fatalf("--force skipped the drain, but the error claims it succeeded: %q", apperr.MessageFor(err))
	}
	if code := apperr.ExitCodeFor(err); code != 1 {
		t.Fatalf("exit code = %d, want the companion's 1", code)
	}
	home, homeErr := service.homeDir()
	if homeErr != nil {
		t.Fatalf("home dir: %v", homeErr)
	}
	if _, _, readErr := mountlocator.Read(home, mountPath); readErr != nil {
		t.Fatalf("locator must survive a failed --force unmount: %v", readErr)
	}
}

// TestUnmountForceWithMissingOverlayPathSucceeds covers --force with a path the
// overlay no longer holds: the skip must happen before the strict umount.
func TestUnmountForceWithMissingOverlayPathSucceeds(t *testing.T) {
	service, mountPath, recordPath, _ := mountWithPackPaths(t, []string{"/gone"})
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
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"})
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
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"})
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
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src", "node_modules"})
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

// TestDryRunKeepsReplayForAnExplicitArchive pins the dry-run copy of the replay
// rule: --no-auto-pack alone suppresses the automatic pack, an explicit
// --pack-archive-path still packs, so the recorded paths are still replayed.
func TestDryRunKeepsReplayForAnExplicitArchive(t *testing.T) {
	service, mountPath, _, localRoot := mountWithPackPaths(t, []string{"/src"})
	seedOverlay(t, localRoot, "/src")

	autoPackCheck := func(t *testing.T, opts UnmountFileSystemOptions) dryrun.Check {
		t.Helper()
		opts.Profile = dataProfile()
		opts.MountPath = mountPath
		result, err := service.DryRunUnmountFileSystem(context.Background(), "ti fs unmount-file-system", opts)
		if err != nil {
			t.Fatalf("dry run failed: %v", err)
		}
		for _, check := range result.Checks {
			if check.Name == "auto_pack_paths" {
				return check
			}
		}
		t.Fatalf("dry run missing auto_pack_paths check: %#v", result.Checks)
		return dryrun.Check{}
	}

	if got := autoPackCheck(t, UnmountFileSystemOptions{NoAutoPack: true}); got.Status != "skipped" {
		t.Fatalf("auto_pack_paths = %#v, want skipped with only --no-auto-pack", got)
	}
	if got := autoPackCheck(t, UnmountFileSystemOptions{NoAutoPack: true, PackArchivePath: "/exports/workspace.tar.gz"}); got.Status != "passed" {
		t.Fatalf("auto_pack_paths = %#v, want replayed beside the explicit archive", got)
	} else if !strings.Contains(got.Message, "/src") {
		t.Fatalf("auto_pack_paths = %#v, want the replayed path named", got)
	}
}

// TestUnmountReplaysPackPathsForAnExplicitArchive covers the interaction the
// review named: --no-auto-pack suppresses the automatic archive, but an
// explicit --pack-archive-path still packs, so the recorded paths must ride
// along with it.
func TestUnmountReplaysPackPathsForAnExplicitArchive(t *testing.T) {
	service, mountPath, recordPath, localRoot := mountWithPackPaths(t, []string{"/src"})
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
