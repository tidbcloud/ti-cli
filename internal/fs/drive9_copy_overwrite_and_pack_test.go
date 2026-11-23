package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// The companion's `fs cp` overwrites every drive9 destination unconditionally,
// so ti owns the --overwrite=false contract. These cases pin the shapes that
// the original upload-only probe missed, and the directory-destination rule the
// companion applies (dir/<base(source)>).

func writeFakeStatSpec(t *testing.T, entries map[string]bool) {
	t.Helper()
	spec := map[string]bool{}
	for path, isDir := range entries {
		spec[path] = isDir
	}
	payloadBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(payloadBytes)
	t.Setenv("TI_FAKE_DRIVE9_STAT_SPEC", payload)
}

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
	assertNoCompanionCopy(t, recordPath)
}

func TestDrive9CopyRemoteToRemoteWithoutOverwriteAllowsAbsentTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_STAT_NOT_FOUND", "1")

	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToRemote:   "/workspace/fresh.txt",
	}); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "fs", "cp")
}

// A directory destination receives dir/<base(source)>; the directory itself is
// never replaced, so only the inner path decides.
func TestDrive9CopyIntoExistingRemoteDirectoryProbesTheInnerPath(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)

	t.Run("inner path absent is allowed", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{":/workspace": true})
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/workspace",
		}); err != nil {
			t.Fatalf("copy into existing directory failed: %v", err)
		}
	})

	t.Run("inner path present is rejected with the replaced path", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{":/workspace": true, ":/workspace/README.md": false})
		_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/workspace",
		})
		if apperr.CodeFor(err) != "fs.target_exists" {
			t.Fatalf("copy error = %v, want fs.target_exists", err)
		}
		if !containsText(apperr.MessageFor(err), "/workspace/README.md") {
			t.Fatalf("error %q does not name the replaced path", apperr.MessageFor(err))
		}
	})
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
	assertNoCompanionCopy(t, recordPath)
}

func TestDrive9CopyRemoteToLocalWithoutOverwriteAllowsAbsentTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	target := filepath.Join(t.TempDir(), "fresh.txt")

	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToLocal:    target,
	}); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "fs", "cp")
}

func TestDrive9CopyIntoExistingLocalDirectoryProbesTheInnerPath(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	dir := t.TempDir()

	t.Run("inner path absent is allowed", func(t *testing.T) {
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/workspace/README.md",
			ToLocal:    dir,
		}); err != nil {
			t.Fatalf("copy into existing directory failed: %v", err)
		}
	})

	t.Run("inner path present is rejected", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/workspace/README.md",
			ToLocal:    dir,
		})
		if apperr.CodeFor(err) != "fs.target_exists" {
			t.Fatalf("copy error = %v, want fs.target_exists", err)
		}
		if !containsText(apperr.MessageFor(err), filepath.Join(dir, "README.md")) {
			t.Fatalf("error %q does not name the replaced path", apperr.MessageFor(err))
		}
	})
}

// A dangling symlink still exists: os.Create would write through it, so the
// guard must use Lstat.
func TestDrive9CopyRemoteToLocalRejectsDanglingSymlinkTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling.txt")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToLocal:    link,
	})
	if apperr.CodeFor(err) != "fs.target_exists" {
		t.Fatalf("copy error = %v, want fs.target_exists", err)
	}
	assertNoCompanionCopy(t, recordPath)
}

// `fs stat` cannot see inside a layer and the layer write is an upsert, so
// --overwrite does not govern it: the probe must not pretend to guard it.
func TestDrive9CopyLayerDestinationSkipsOverwriteProbe(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	// Default stat reports the target as existing.
	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToRemote:   "/workspace/source.txt",
		LayerID:    "layer-1",
	}); err != nil {
		t.Fatalf("layer copy failed: %v", err)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "stat"}) {
			t.Fatalf("layer copy still probed with fs stat: %#v", call.Args)
		}
	}
	requireFakeDrive9Call(t, recordPath, "fs", "cp")
}

func TestDrive9CopyRejectsTwoDestinations(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToRemote:   "/workspace/a.txt",
		ToLocal:    filepath.Join(t.TempDir(), "a.txt"),
	})
	if apperr.CodeFor(err) != "fs.ambiguous_copy_target" {
		t.Fatalf("copy error = %v, want fs.ambiguous_copy_target", err)
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

func assertNoCompanionCopy(t *testing.T, recordPath string) {
	t.Helper()
	// The guard runs before the companion, so a rejected copy records no call
	// at all; only assert on the record when the companion actually ran.
	if _, err := os.Stat(recordPath); err != nil {
		return
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
			t.Fatalf("blocked copy still invoked companion: %#v", call.Args)
		}
	}
}

func containsText(haystack, needle string) bool {
	return needle == "" || strings.Contains(haystack, needle)
}
