package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// The companion's `fs cp` overwrites every drive9 destination unconditionally,
// so ti owns the --overwrite=false contract for each source/destination shape.
// These cases pin the two shapes that used to fall through the probe.

func TestDrive9CopyRemoteToRemoteWithoutOverwriteRejectsExistingTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	// The fake companion's `fs stat` succeeds by default, i.e. the target exists.

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToRemote:   "/workspace/existing.txt",
	})
	if apperr.CodeFor(err) != "fs.target_exists" {
		t.Fatalf("copy error = %v, want fs.target_exists", err)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
			t.Fatalf("blocked copy still invoked companion: %#v", call.Args)
		}
	}
}

func TestDrive9CopyRemoteToRemoteWithoutOverwriteAllowsAbsentTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_STAT_NOT_FOUND", "1")

	result, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToRemote:   "/workspace/fresh.txt",
	})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if result.Status != "copied" {
		t.Fatalf("copy status = %q, want copied", result.Status)
	}
	requireFakeDrive9Call(t, recordPath, "fs", "cp")
}

func TestDrive9CopyRemoteToLocalWithoutOverwriteRejectsExistingTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	target := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(target, []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToLocal:    target,
	})
	if apperr.CodeFor(err) != "fs.target_exists" {
		t.Fatalf("copy error = %v, want fs.target_exists", err)
	}
	// The guard runs before the companion, so a rejected copy records no call at
	// all; only assert on the record when the companion actually ran.
	if _, statErr := os.Stat(recordPath); statErr == nil {
		for _, call := range readFakeDrive9Calls(t, recordPath) {
			if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
				t.Fatalf("blocked copy still invoked companion: %#v", call.Args)
			}
		}
	}
}

func TestDrive9CopyRemoteToLocalWithoutOverwriteAllowsAbsentTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	target := filepath.Join(t.TempDir(), "fresh.txt")

	result, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToLocal:    target,
	})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if result.Status != "copied" {
		t.Fatalf("copy status = %q, want copied", result.Status)
	}
}

// A recursive copy merges into an existing destination directory by contract,
// so the probe must not turn that into fs.target_exists.
func TestDrive9CopyRecursiveWithoutOverwriteSkipsTargetProbe(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	// `fs stat` would report the target as existing if the probe ran.
	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/src-dir",
		ToRemote:   "/workspace/src-dir.copy",
		Recursive:  true,
	}); err != nil {
		t.Fatalf("recursive copy failed: %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "fs", "cp")
}

// --pack-path is what makes the portable profile's automatic pack include a
// local overlay path; before this it was echoed back to the caller but never
// forwarded, so the archive unpacked in the next sandbox was incomplete.
func TestDrive9MountFileSystemForwardsPackPaths(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	mountPath := filepath.Join(t.TempDir(), "packed")
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:      dataProfile(),
		MountPath:    mountPath,
		RemotePath:   "/workspace",
		Driver:       "fuse",
		MountProfile: "portable",
		PackPaths:    []string{"/src", "node_modules"},
	}); err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	mountCall := requireFakeDrive9CallWithoutArg(t, recordPath, "--help", "mount")
	var forwarded []string
	for i, arg := range mountCall.Args {
		if arg == "--pack-path" && i+1 < len(mountCall.Args) {
			forwarded = append(forwarded, mountCall.Args[i+1])
		}
	}
	want := []string{"/src", "node_modules"}
	if len(forwarded) != len(want) || forwarded[0] != want[0] || forwarded[1] != want[1] {
		t.Fatalf("forwarded --pack-path = %#v, want %#v (argv %#v)", forwarded, want, mountCall.Args)
	}
}
