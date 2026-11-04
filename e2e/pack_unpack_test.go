package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFSUnpackFileSystemRejectsEscapingSymlinksFromCompanion is the public-path
// regression for the pack symlink finding: the tar extraction runs in the
// ti-drive9 companion, so the shipped command must verify the overlay the
// companion produced and fail closed instead of leaving an escaping link behind.
func TestFSUnpackFileSystemRejectsEscapingSymlinksFromCompanion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion and symlink fixtures are covered by unit tests on Windows")
	}
	bin := tiBinary(t)
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	home := t.TempDir()
	token := drive9TestToken("tenant-pack-symlink")
	baseEnv := []string{
		"HOME=" + home,
		"TI_PROFILE=default",
		"TI_REGION_CODE=aws-us-east-1",
		"TI_DRIVE9_BIN=" + companion,
		"TI_FS_TOKEN=" + token,
		"TI_LOGGING=off",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
	}
	unpackArgs := func(localRoot string) []string {
		return []string{"fs", "unpack-file-system", "--local-root", localRoot, "--remote-root", "/workspace", "--mount-profile", "portable"}
	}

	for _, target := range []string{"/etc/passwd", "../../../../../../home/victim/.ssh"} {
		target := target
		t.Run("target-"+target, func(t *testing.T) {
			localRoot := t.TempDir()
			env := append([]string{}, baseEnv...)
			env = append(env,
				"FAKE_DRIVE9_UNPACK_SYMLINK_REL=repo/escape",
				"FAKE_DRIVE9_UNPACK_SYMLINK_TARGET="+target,
			)
			result := runTIWithInput(t, bin, "", env, unpackArgs(localRoot)...)
			result.wantExitCode(1)
			result.wantStderrContains("unsafe symlink")
			linkPath := filepath.Join(localRoot, "overlay", "repo", "escape")
			if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
				t.Fatalf("escaping symlink %q was left behind: %v", linkPath, err)
			}
			if _, err := os.Stat(filepath.Join(localRoot, "overlay", "repo", "item.txt")); err != nil {
				t.Fatalf("benign unpacked content was lost: %v", err)
			}
		})
	}

	t.Run("safe-relative-link-still-unpacks", func(t *testing.T) {
		localRoot := t.TempDir()
		env := append([]string{}, baseEnv...)
		env = append(env,
			"FAKE_DRIVE9_UNPACK_SYMLINK_REL=repo/inside",
			"FAKE_DRIVE9_UNPACK_SYMLINK_TARGET=item.txt",
		)
		result := runTIWithInput(t, bin, "", env, unpackArgs(localRoot)...)
		result.wantExitCode(0)
		result.wantStdoutContains(`"status": "unpacked"`)
		linkPath := filepath.Join(localRoot, "overlay", "repo", "inside")
		if info, err := os.Lstat(linkPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("safe relative symlink was not preserved: info=%v err=%v", info, err)
		}
	})
}
