package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// The server's copy inserts a new node at the destination, so --overwrite has to
// be ti's: copy to a sibling temporary path and move it onto the destination,
// which replaces the target in one rename. These cases pin the argv sequence so
// a later change cannot quietly go back to asking the server for something it
// refuses.

func copyCalls(t *testing.T, recordPath string) [][]string {
	t.Helper()
	var calls [][]string
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "fs" {
			calls = append(calls, call.Args)
		}
	}
	return calls
}

func tempNameFor(t *testing.T, calls [][]string) (string, string) {
	t.Helper()
	var stage, move string
	for _, args := range calls {
		switch args[1] {
		case "cp":
			stage = args[len(args)-1]
		case "mv":
			move = args[len(args)-1]
		}
	}
	return stage, move
}

func TestDrive9CopyServerSideOverwriteStagesAndMoves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   CopyFileOptions
		spec   map[string]bool
		target string
	}{
		{
			name:   "existing file destination",
			opts:   CopyFileOptions{Profile: dataProfile(), FromRemote: "/src/a.txt", ToRemote: "/dst.txt", Overwrite: true},
			spec:   map[string]bool{":/dst.txt": false},
			target: ":/dst.txt",
		},
		{
			name:   "existing file inside a directory destination",
			opts:   CopyFileOptions{Profile: dataProfile(), FromRemote: "/src/a.txt", ToRemote: "/dir", Overwrite: true},
			spec:   map[string]bool{":/dir": true, ":/dir/a.txt": false},
			target: ":/dir/a.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			writeFakeStatSpec(t, tc.spec)

			result, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("overwrite copy failed: %v", err)
			}
			// The result reports the destination the caller named; the resolved
			// path is what the staging calls use.
			if result.Status != "copied" || result.TargetPath != strings.TrimPrefix(tc.opts.ToRemote, ":") {
				t.Fatalf("result = %+v, want status copied and target %q", result, tc.opts.ToRemote)
			}
			calls := copyCalls(t, recordPath)
			stage, move := tempNameFor(t, calls)
			if stage == "" || !strings.HasPrefix(stage, tc.target+".ti-tmp-") {
				t.Fatalf("staged copy target = %q, want a sibling of %q", stage, tc.target)
			}
			if move != tc.target {
				t.Fatalf("move target = %q, want %q", move, tc.target)
			}
			sawMove := false
			for _, args := range calls {
				if args[1] == "mv" {
					sawMove = true
					if args[2] != stage {
						t.Fatalf("move source = %q, want the staged path %q", args[2], stage)
					}
				}
			}
			if !sawMove {
				t.Fatalf("calls = %v, want a move onto the destination", calls)
			}
		})
	}
}

func TestDrive9CopyServerSideOverwriteWithoutExistingTargetCopiesOnce(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	writeFakeStatSpec(t, map[string]bool{":/dir": true})

	if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dir",
		Overwrite:  true,
	}); err != nil {
		t.Fatalf("overwrite copy failed: %v", err)
	}
	for _, args := range copyCalls(t, recordPath) {
		if args[1] == "mv" {
			t.Fatalf("calls = %v, want no staging move when the destination is absent", copyCalls(t, recordPath))
		}
	}
}

func TestDrive9CopyServerSideOverwriteRejectsDirectoryTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	writeFakeStatSpec(t, map[string]bool{":/dir": true, ":/dir/a.txt": true})

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dir",
		Overwrite:  true,
	})
	if apperr.CodeFor(err) != "fs.remote_copy_target_is_directory" {
		t.Fatalf("copy error = %v, want fs.remote_copy_target_is_directory", err)
	}
	for _, args := range copyCalls(t, recordPath) {
		if args[1] == "cp" || args[1] == "mv" {
			t.Fatalf("calls = %v, want no write when the destination is a directory", copyCalls(t, recordPath))
		}
	}
}

func TestDrive9CopyServerSideOverwriteNamesTheStagingPathWhenCleanupFails(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_MV_FAIL", "1")
	t.Setenv("TI_FAKE_DRIVE9_RM_FAIL", "1")
	writeFakeStatSpec(t, map[string]bool{":/dst.txt": false})

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if apperr.CodeFor(err) != "fs.copy_staging_left_behind" {
		t.Fatalf("copy error = %v, want fs.copy_staging_left_behind", err)
	}
	stage, _ := tempNameFor(t, copyCalls(t, recordPath))
	stage = strings.TrimPrefix(stage, ":")
	message := apperr.MessageFor(err)
	if stage == "" || !strings.Contains(message, stage) {
		t.Fatalf("error %q does not name the staging path %q", message, stage)
	}
	if !strings.Contains(message, "ti fs delete-file --path "+stage) {
		t.Fatalf("error %q does not name the command that removes the staging file", message)
	}
	if !strings.Contains(message, ".ti-tmp-") {
		t.Fatalf("error %q does not show the staging suffix", message)
	}
}

// A staged copy that fails may still have created the node, so the cleanup runs
// and the copy error stays the one the caller sees.
func TestDrive9CopyServerSideOverwriteCleansUpAfterFailedStagedCopy(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_CP_STAGED_FAIL", "1")
	writeFakeStatSpec(t, map[string]bool{":/dst.txt": false})

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if err == nil {
		t.Fatal("copy succeeded although the staged copy failed")
	}
	if apperr.CodeFor(err) == "fs.copy_staging_left_behind" {
		t.Fatalf("copy error = %v, want the copy failure rather than a cleanup note", err)
	}
	calls := copyCalls(t, recordPath)
	for _, args := range calls {
		if args[1] == "mv" {
			t.Fatalf("calls = %v, want no move after a failed staged copy", calls)
		}
	}
	staged, _ := tempNameFor(t, calls)
	removed := false
	for _, args := range calls {
		if args[1] == "rm" && args[len(args)-1] == staged {
			removed = true
		}
	}
	if staged == "" || !removed {
		t.Fatalf("calls = %v, want the staging path %q removed", calls, staged)
	}
}

// --overwrite on a shape that cannot be replaced must be refused, not staged
// onto itself.
func TestDrive9CopyServerSideOverwriteRefusesSelfCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CopyFileOptions
		spec map[string]bool
	}{
		{
			name: "same path",
			opts: CopyFileOptions{Profile: dataProfile(), FromRemote: "/a.txt", ToRemote: "/a.txt", Overwrite: true},
			spec: map[string]bool{":/a.txt": false},
		},
		{
			name: "directory destination resolving to the source",
			opts: CopyFileOptions{Profile: dataProfile(), FromRemote: "/dir/a.txt", ToRemote: "/dir", Overwrite: true},
			spec: map[string]bool{":/dir": true, ":/dir/a.txt": false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			writeFakeStatSpec(t, tc.spec)
			_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), tc.opts)
			if apperr.CodeFor(err) != "fs.target_exists" {
				t.Fatalf("copy error = %v, want fs.target_exists", err)
			}
			if !strings.Contains(apperr.MessageFor(err), "is the source path") {
				t.Fatalf("error %q does not name the collision", apperr.MessageFor(err))
			}
			for _, args := range copyCalls(t, recordPath) {
				if args[1] == "cp" || args[1] == "mv" {
					t.Fatalf("calls = %v, want no write for a self copy", copyCalls(t, recordPath))
				}
			}
		})
	}
}

func TestDrive9CopyServerSideOverwriteCleansUpAfterFailedMove(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_MV_FAIL", "1")
	writeFakeStatSpec(t, map[string]bool{":/dst.txt": false})

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if err == nil {
		t.Fatal("copy succeeded although the move failed")
	}
	stage, _ := tempNameFor(t, copyCalls(t, recordPath))
	if stage == "" {
		t.Fatalf("calls = %v, want a staged copy before the move", copyCalls(t, recordPath))
	}
	removed := false
	for _, args := range copyCalls(t, recordPath) {
		if args[1] == "rm" && args[len(args)-1] == stage {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("calls = %v, want the staged path removed after the failed move", copyCalls(t, recordPath))
	}
}

// Uploads and downloads replace natively, so they must keep the single
// companion call and never pay for the staging dance.
func TestDrive9CopyUploadAndDownloadOverwriteStaySingleCall(t *testing.T) {
	t.Run("upload", func(t *testing.T) {
		companion, recordPath := buildFakeDrive9(t)
		t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
		writeFakeStatSpec(t, map[string]bool{":/dst.txt": false})
		local := filepath.Join(t.TempDir(), "a.txt")
		if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:   dataProfile(),
			FromLocal: local,
			ToRemote:  "/dst.txt",
			Overwrite: true,
		}); err != nil {
			t.Fatalf("upload overwrite failed: %v", err)
		}
		assertSingleCopyCall(t, recordPath)
	})

	t.Run("download", func(t *testing.T) {
		companion, recordPath := buildFakeDrive9(t)
		t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
		writeFakeStatSpec(t, map[string]bool{":/src.txt": false})
		target := filepath.Join(t.TempDir(), "out.txt")
		if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src.txt",
			ToLocal:    target,
			Overwrite:  true,
		}); err != nil {
			t.Fatalf("download overwrite failed: %v", err)
		}
		assertSingleCopyCall(t, recordPath)
	})
}

func assertSingleCopyCall(t *testing.T, recordPath string) {
	t.Helper()
	copies, moves := 0, 0
	for _, args := range copyCalls(t, recordPath) {
		switch args[1] {
		case "cp":
			copies++
		case "mv":
			moves++
		}
	}
	if copies != 1 || moves != 0 {
		t.Fatalf("calls = %v, want exactly one cp and no mv", copyCalls(t, recordPath))
	}
}

// A local path spelled with a location scheme is resolved by the companion, not
// by ti, so the literal-path probe would decide about a path the copy never
// touches. Reject the spelling instead.
func TestDrive9CopyRejectsSchemeSpelledLocalPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CopyFileOptions
	}{
		{name: "to-local file scheme", opts: CopyFileOptions{Profile: dataProfile(), FromRemote: "/a.txt", ToLocal: "file:///tmp/out.txt"}},
		{name: "to-local bare scheme", opts: CopyFileOptions{Profile: dataProfile(), FromRemote: "/a.txt", ToLocal: "file:/tmp/out.txt"}},
		{name: "from-local file scheme", opts: CopyFileOptions{Profile: dataProfile(), FromLocal: "file:///tmp/a.txt", ToRemote: "/out.txt"}},
		{name: "to-local drive9 spelling", opts: CopyFileOptions{Profile: dataProfile(), FromRemote: "/a.txt", ToLocal: ":/out.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Validation runs before any companion or mount lookup, so a service
			// with no companion suffices here.
			_, err := Service{}.CopyFile(context.Background(), tc.opts)
			if apperr.CodeFor(err) != "fs.local_path_scheme_unsupported" {
				t.Fatalf("copy error = %v, want fs.local_path_scheme_unsupported", err)
			}
		})
	}
}

// Only the server-side replace retargets a copy, and only a remote to remote
// copy can be retargeted; the guard keeps a future caller from staging an
// upload or a download by accident.
func TestDrive9CopyArgsForTargetRejectsNonRemoteRoutes(t *testing.T) {
	local := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := drive9CopyArgsForTarget(CopyFileOptions{Profile: dataProfile(), FromLocal: local, ToRemote: "/dst.txt"}, "/dst.txt.ti-tmp-0000")
	if apperr.CodeFor(err) != "fs.invalid_copy_flags" {
		t.Fatalf("retargeted upload error = %v, want fs.invalid_copy_flags", err)
	}
}
