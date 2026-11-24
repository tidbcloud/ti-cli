package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	payloadBytes, err := json.Marshal(entries)
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
		if !strings.Contains(apperr.MessageFor(err), "/workspace/README.md") {
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
		if !strings.Contains(apperr.MessageFor(err), filepath.Join(dir, "README.md")) {
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

// The pinned companion refuses a directory destination when the source has no
// file name (stdin), so ti must say so itself instead of passing through a
// companion usage dump.
func TestDrive9CopyStdinToDirectoryRequiresName(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	writeFakeStatSpec(t, map[string]bool{":/workspace": true})

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:   dataProfile(),
		FromStdin: true,
		ToRemote:  "/workspace",
	})
	if apperr.CodeFor(err) != "fs.copy_target_requires_name" {
		t.Fatalf("copy error = %v, want fs.copy_target_requires_name", err)
	}
	assertNoCompanionCopy(t, recordPath)
}

// A trailing separator is the companion's "destination is a directory" spelling
// and it joins <target>/<base(source)> without a stat, so the probe must join the
// same way.
func TestDrive9CopyTrailingSeparatorDirectoryDestination(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)

	t.Run("remote inner path absent is allowed", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{})
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/workspace/",
		}); err != nil {
			t.Fatalf("copy into slash-terminated directory failed: %v", err)
		}
	})

	t.Run("remote inner path present is rejected", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{":/workspace/README.md": false})
		_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/workspace/",
		})
		if apperr.CodeFor(err) != "fs.target_exists" {
			t.Fatalf("copy error = %v, want fs.target_exists", err)
		}
	})
}

// A symlink that resolves to a directory is a directory destination for the
// companion (it stats through the link), so the probe joins as well.
func TestDrive9CopyLocalSymlinkToDirectoryJoinsSourceName(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	root := t.TempDir()
	dir := filepath.Join(root, "real-dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link-dir")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/README.md",
		ToLocal:    link,
	})
	if apperr.CodeFor(err) != "fs.target_exists" {
		t.Fatalf("copy error = %v, want fs.target_exists", err)
	}
	if !strings.Contains(apperr.MessageFor(err), filepath.Join(link, "README.md")) {
		t.Fatalf("error %q does not name the joined path", apperr.MessageFor(err))
	}
}

// Two sources must be rejected like two destinations: with both set, the route
// and the argv spelling used to disagree about which source ran.
func TestDrive9CopyRejectsTwoSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CopyFileOptions
	}{
		{name: "local and remote", opts: CopyFileOptions{Profile: dataProfile(), FromLocal: "report.md", FromRemote: "/a.txt", ToRemote: "/c.txt"}},
		{name: "stdin and remote", opts: CopyFileOptions{Profile: dataProfile(), FromStdin: true, FromRemote: "/a.txt", ToLocal: "/tmp/dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), tc.opts)
			if apperr.CodeFor(err) != "fs.invalid_copy_flags" {
				t.Fatalf("copy error = %v, want fs.invalid_copy_flags", err)
			}
			assertNoCompanionCopy(t, recordPath)
		})
	}
}

// The root destination is a directory destination: it must join the source name
// instead of being treated as a file that exists.
func TestDrive9CopyRootDestinationJoinsSourceName(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)

	t.Run("inner path absent is allowed", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{":/": true})
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/",
		}); err != nil {
			t.Fatalf("copy into root failed: %v", err)
		}
	})

	t.Run("inner path present is rejected", func(t *testing.T) {
		writeFakeStatSpec(t, map[string]bool{":/": true, ":/README.md": false})
		_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/README.md",
			ToRemote:   "/",
		})
		if apperr.CodeFor(err) != "fs.target_exists" {
			t.Fatalf("copy error = %v, want fs.target_exists", err)
		}
	})
}

// The local root is a directory like any other: the companion joins
// filepath.Join("/", base) and writes /<name>, so the probe must look there.
// Trimming the separators first made the joined path relative, which probed the
// working directory instead.
func TestDrive9CopyIntoLocalRootProbesTheRootedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a drive root is not a writable directory target on windows")
	}
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	// The file name carries the test binary's pid so a stale /<name> cannot make
	// the assertion order-dependent.
	name := fmt.Sprintf("ti-root-probe-%d.md", os.Getpid())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, name), []byte("decoy"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/" + name,
		ToLocal:    "/",
	}); err != nil {
		t.Fatalf("copy into the local root failed: %v (the probe looked at %q, not %q)", err, name, "/"+name)
	}
	var copies [][]string
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
			copies = append(copies, call.Args)
		}
	}
	if len(copies) != 1 {
		t.Fatalf("companion copies = %#v, want the delegated copy into /", copies)
	}
	if !containsArg(copies[0], "/") {
		t.Fatalf("companion argv = %#v, want the destination to stay /", copies[0])
	}
}

// A trailing-separator destination names a directory even when the path it names
// is occupied by a file, because the companion joins without stat'ing the target:
// the only stat the probe may issue is for the joined child path.
func TestDrive9CopyTrailingSeparatorOnAFilePathJoins(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	writeFakeStatSpec(t, map[string]bool{":/workspace": false})

	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/README.md",
		ToRemote:   "/workspace/",
	}); err != nil {
		t.Fatalf("trailing-separator destination failed: %v", err)
	}
	var stats []string
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "stat"}) {
			for _, arg := range call.Args {
				if strings.HasPrefix(arg, ":/") {
					stats = append(stats, arg)
				}
			}
		}
	}
	want := ":/workspace/README.md"
	if len(stats) != 1 || stats[0] != want {
		t.Fatalf("probe stats = %#v, want exactly %q (the joined child, not the directory)", stats, want)
	}
}

// The route shape is validated in one place, so the command builder and the
// service agree. Table-test the shapes that used to slip through one of them.
func TestValidateDrive9CopyRoute(t *testing.T) {
	base := CopyFileOptions{Profile: dataProfile()}
	for _, tc := range []struct {
		name string
		opts CopyFileOptions
		want string
	}{
		{name: "two destinations", opts: CopyFileOptions{Profile: base.Profile, FromRemote: "/a", ToRemote: "/b", ToLocal: "/tmp/c"}, want: "fs.ambiguous_copy_target"},
		{name: "stdout plus a destination", opts: CopyFileOptions{Profile: base.Profile, FromRemote: "/a", ToRemote: "/b", ToStdout: true}, want: "fs.ambiguous_copy_target"},
		{name: "two sources with stdout", opts: CopyFileOptions{Profile: base.Profile, FromStdin: true, FromRemote: "/a", ToStdout: true}, want: "fs.invalid_copy_flags"},
		{name: "two sources", opts: CopyFileOptions{Profile: base.Profile, FromLocal: "a.md", FromRemote: "/a", ToRemote: "/c"}, want: "fs.invalid_copy_flags"},
		{name: "no destination", opts: CopyFileOptions{Profile: base.Profile, FromRemote: "/a"}, want: "fs.invalid_copy_flags"},
		{name: "remote to remote", opts: CopyFileOptions{Profile: base.Profile, FromRemote: "/a", ToRemote: "/b"}, want: ""},
		{name: "stdin to remote", opts: CopyFileOptions{Profile: base.Profile, FromStdin: true, ToRemote: "/b"}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDrive9CopyRoute(tc.opts)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validation error = %v, want nil", err)
				}
				return
			}
			if apperr.CodeFor(err) != tc.want {
				t.Fatalf("validation error = %v, want %s", err, tc.want)
			}
		})
	}
}

// A symlink to a file is an existing target: the companion would write through it.
func TestDrive9CopyRemoteToLocalRejectsSymlinkToFile(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	root := t.TempDir()
	file := filepath.Join(root, "real.txt")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(file, link); err != nil {
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

// A stat failure other than "not found" must surface as the shared local-stat
// error code instead of being treated as an absent target: a parent that is a
// regular file gives ENOTDIR.
func TestDrive9CopyRemoteToLocalStatErrorSurfacesAsStatLocalFile(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/workspace/source.txt",
		ToLocal:    filepath.Join(parent, "child.txt"),
	})
	if apperr.CodeFor(err) != "fs.stat_local_file" {
		t.Fatalf("copy error = %v, want fs.stat_local_file", err)
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
		Profile:   dataProfile(),
		FromLocal: filepath.Join(t.TempDir(), "src-dir"),
		ToRemote:  "/workspace/src-dir",
		Recursive: true,
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
