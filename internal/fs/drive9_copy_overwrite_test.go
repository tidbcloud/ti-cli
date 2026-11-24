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
// which replaces the target in one rename. These cases pin the argv sequence and
// the error the user sees after each failure, so a later change cannot quietly
// go back to asking the server for something it refuses, or to inferring whether
// the staging file exists from one removal error.

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

// newStatefulCopyService wires a service to the fake companion in stateful mode:
// the fake keeps the set of remote paths it created, so a test can say "the
// staging file was never created", "it was created and the cleanup was refused",
// or "the rename applied but its answer was lost".
func newStatefulCopyService(t *testing.T, existing ...string) (Service, string, string) {
	t.Helper()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	state := t.TempDir()
	t.Setenv("TI_FAKE_DRIVE9_STATE_DIR", state)
	if len(existing) > 0 {
		if err := os.WriteFile(filepath.Join(state, "paths"), []byte(strings.Join(existing, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return testCompanionService(t.TempDir(), companion), recordPath, state
}

func TestDrive9CopyServerSideOverwriteStagesAndMoves(t *testing.T) {
	t.Run("existing file destination", func(t *testing.T) {
		service, recordPath, _ := newStatefulCopyService(t, "/dst.txt")
		result, err := service.CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/a.txt",
			ToRemote:   "/dst.txt",
			Overwrite:  true,
		})
		if err != nil {
			t.Fatalf("overwrite copy failed: %v", err)
		}
		if result.Status != "copied" || result.TargetPath != "/dst.txt" {
			t.Fatalf("result = %+v, want status copied and target /dst.txt", result)
		}
		calls := copyCalls(t, recordPath)
		stage, move := tempNameFor(t, calls)
		if stage == "" || !strings.HasPrefix(stage, ":/dst.txt.ti-tmp-") {
			t.Fatalf("staged copy target = %q, want a sibling of :/dst.txt", stage)
		}
		if move != ":/dst.txt" {
			t.Fatalf("move target = %q, want :/dst.txt", move)
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

	t.Run("existing file inside a directory destination", func(t *testing.T) {
		service, recordPath, _ := newStatefulCopyService(t, "/dir/a.txt")
		t.Setenv("TI_FAKE_DRIVE9_STAT_SPEC", `{":/dir": true}`)
		if _, err := service.CopyFile(context.Background(), CopyFileOptions{
			Profile:    dataProfile(),
			FromRemote: "/src/a.txt",
			ToRemote:   "/dir",
			Overwrite:  true,
		}); err != nil {
			t.Fatalf("overwrite copy into a directory failed: %v", err)
		}
		stage, move := tempNameFor(t, copyCalls(t, recordPath))
		if !strings.HasPrefix(stage, ":/dir/a.txt.ti-tmp-") || move != ":/dir/a.txt" {
			t.Fatalf("stage = %q move = %q, want the resolved inner path", stage, move)
		}
	})
}

func TestDrive9CopyServerSideOverwriteWithoutExistingTargetCopiesOnce(t *testing.T) {
	service, recordPath, _ := newStatefulCopyService(t)
	t.Setenv("TI_FAKE_DRIVE9_STAT_SPEC", `{":/dir": true}`)
	if _, err := service.CopyFile(context.Background(), CopyFileOptions{
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

// The move failed and the staging file is still there, so the rename did not
// apply; removing it leaves the move error as the one the caller sees.
func TestDrive9CopyServerSideOverwriteCleansUpAfterFailedMove(t *testing.T) {
	service, recordPath, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_MV_FAIL", "1")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if err == nil {
		t.Fatal("copy succeeded although the move failed")
	}
	if !strings.Contains(apperr.MessageFor(err), "fs mv: backend unavailable") {
		t.Fatalf("copy error = %v, want the move error itself", err)
	}
	if apperr.CodeFor(err) == "fs.copy_staging_left_behind" || apperr.CodeFor(err) == "fs.copy_outcome_unknown" {
		t.Fatalf("copy error = %v, want the move error when the staging file was removed", err)
	}
	stage, _ := tempNameFor(t, copyCalls(t, recordPath))
	removed := false
	for _, args := range copyCalls(t, recordPath) {
		if args[1] == "rm" && args[len(args)-1] == stage {
			removed = true
		}
	}
	if stage == "" || !removed {
		t.Fatalf("calls = %v, want the staged path %q removed", copyCalls(t, recordPath), stage)
	}
}

// A token without delete makes the service refuse `rm` before it looks the path
// up, so a copy that created nothing must not be reported as a leftover: the
// cleanup asks the service whether the staging file exists first.
func TestDrive9CopyServerSideOverwriteNoLeftoverWhenNothingWasCreated(t *testing.T) {
	service, _, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_CP_STAGED_FAIL", "1")
	t.Setenv("TI_FAKE_DRIVE9_DELETE_DENIED", "1")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/missing.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if err == nil {
		t.Fatal("copy succeeded although it could not be staged")
	}
	if apperr.CodeFor(err) != "fs.companion_failed" {
		t.Fatalf("copy error = %v, want the staged copy failure alone", err)
	}
}

// The staging file was created and the cleanup was refused, so the leftover has
// to be named with the command that removes it.
func TestDrive9CopyServerSideOverwriteNamesTheStagingPathWhenCleanupFails(t *testing.T) {
	service, recordPath, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_MV_FAIL", "1")
	t.Setenv("TI_FAKE_DRIVE9_DELETE_DENIED", "1")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
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
	if !strings.Contains(message, "needs delete on that path") {
		t.Fatalf("error %q does not say the delete command needs delete", message)
	}
}

// The staged copy may have created the node before it failed; the error stays the
// copy's unless the cleanup leaves the file behind.
func TestDrive9CopyServerSideOverwriteCleansUpAfterFailedStagedCopy(t *testing.T) {
	service, _, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_CP_STAGED_FAIL", "after-create")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if err == nil {
		t.Fatal("copy succeeded although the staged copy failed")
	}
	if apperr.CodeFor(err) != "fs.companion_failed" {
		t.Fatalf("copy error = %v, want the staged copy failure alone", err)
	}
}

func TestDrive9CopyServerSideOverwriteNamesStagingAfterFailedStagedCopy(t *testing.T) {
	service, recordPath, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_CP_STAGED_FAIL", "after-create")
	t.Setenv("TI_FAKE_DRIVE9_DELETE_DENIED", "1")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
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
	if stage == "" || !strings.Contains(apperr.MessageFor(err), stage) {
		t.Fatalf("error %q does not name the staging path %q", apperr.MessageFor(err), stage)
	}
}

// The rename may be applied before its answer is lost: then the staging file is
// already gone, the destination already holds the new bytes, and the error has to
// say the outcome is unknown instead of pointing at a delete.
func TestDrive9CopyServerSideOverwriteAppliedMoveLostAnswer(t *testing.T) {
	service, _, _ := newStatefulCopyService(t, "/dst.txt")
	t.Setenv("TI_FAKE_DRIVE9_MV_APPLIED_FAIL", "1")

	_, err := service.CopyFile(context.Background(), CopyFileOptions{
		Profile:    dataProfile(),
		FromRemote: "/src/a.txt",
		ToRemote:   "/dst.txt",
		Overwrite:  true,
	})
	if apperr.CodeFor(err) != "fs.copy_outcome_unknown" {
		t.Fatalf("copy error = %v, want fs.copy_outcome_unknown", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "/dst.txt") || !strings.Contains(message, "/src/a.txt") {
		t.Fatalf("error %q does not name both paths", message)
	}
	if !strings.Contains(message, "re-run with --overwrite") {
		t.Fatalf("error %q does not give the caller something to do", message)
	}
	if strings.Contains(message, "delete-file") {
		t.Fatalf("error %q points at a delete for a staging path that is already gone", message)
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

// A local path spelled with a location scheme, or with the prefix the companion
// reads as a drive9 path, is resolved by the companion, not by ti, so the
// literal-path probe would decide about a path the copy never touches.
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
