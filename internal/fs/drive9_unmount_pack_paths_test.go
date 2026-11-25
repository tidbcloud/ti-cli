package fs

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

// mountWithPackPaths mounts through the fake companion and returns the mount
// path plus the recorded calls file.
func mountWithPackPaths(t *testing.T, packPaths []string, opts ...func(*MountFileSystemOptions)) (Service, string, string) {
	t.Helper()
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	mountOpts := MountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
		Driver:    "fuse",
		PackPaths: packPaths,
	}
	for _, apply := range opts {
		apply(&mountOpts)
	}
	if _, err := service.MountFileSystem(context.Background(), mountOpts); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	return service, mountPath, recordPath
}

// packPathArgs returns every value following a --pack-path flag, in order.
func packPathArgs(args []string) []string {
	var out []string
	for i, arg := range args {
		if arg == "--pack-path" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func umountArgs(t *testing.T, recordPath string) []string {
	t.Helper()
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "umount" {
			return call.Args
		}
	}
	t.Fatalf("no umount call recorded")
	return nil
}

// TestMountRecordsPackPathsInLocator pins that --pack-path survives the mount
// on the locator, because the companion mount cannot receive it directly.
func TestMountRecordsPackPathsInLocator(t *testing.T) {
	service, mountPath, _ := mountWithPackPaths(t, []string{"/src", " node_modules ", "/src", ""})
	home, err := service.homeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read locator: %v", err)
	}
	want := []string{"/src", "node_modules"}
	if !reflect.DeepEqual(locator.PackPaths, want) {
		t.Fatalf("locator pack paths = %#v, want %#v", locator.PackPaths, want)
	}
}

// TestUnmountForwardsPackPathsToCompanion is the regression this change exists
// for: the paths must reach `drive9 umount`, which is what feeds auto-pack.
func TestUnmountForwardsPackPathsToCompanion(t *testing.T) {
	service, mountPath, recordPath := mountWithPackPaths(t, []string{"/src", "node_modules"})
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	want := []string{"/src", "node_modules"}
	if got := packPathArgs(umountArgs(t, recordPath)); !reflect.DeepEqual(got, want) {
		t.Fatalf("umount --pack-path = %#v, want %#v", got, want)
	}
}

// TestUnmountSkipsPackPathsWhenAutoPackDisabled keeps --no-auto-pack
// authoritative: no pack flags may be sent when the user opted out.
func TestUnmountSkipsPackPathsWhenAutoPackDisabled(t *testing.T) {
	service, mountPath, recordPath := mountWithPackPaths(t, []string{"/src"})
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:    dataProfile(),
		MountPath:  mountPath,
		NoAutoPack: true,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none", got)
	}
}

// TestUnmountWithoutPackPathsSendsNoFlag covers the common mount: no recorded
// paths means no flags and no behaviour change for existing users.
func TestUnmountWithoutPackPathsSendsNoFlag(t *testing.T) {
	service, mountPath, recordPath := mountWithPackPaths(t, nil)
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   dataProfile(),
		MountPath: mountPath,
	}); err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if got := packPathArgs(umountArgs(t, recordPath)); len(got) != 0 {
		t.Fatalf("umount --pack-path = %#v, want none", got)
	}
}
