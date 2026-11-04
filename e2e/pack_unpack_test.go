package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func buildFakeCompanion(t *testing.T) string {
	t.Helper()
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	return companion
}

// TestFSUnpackFileSystemRejectsEscapingSymlinksFromCompanion is the public-path
// regression for the pack symlink finding: the tar extraction runs in the
// ti-drive9 companion, so the shipped command must resolve the effective local
// overlay, snapshot it, and then fail closed on whatever the companion wrote.
func TestFSUnpackFileSystemRejectsEscapingSymlinksFromCompanion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion and symlink fixtures are covered by unit tests on Windows")
	}
	bin := tiBinary(t)
	companion := buildFakeCompanion(t)
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

	t.Run("escaping-link-chain", func(t *testing.T) {
		localRoot := t.TempDir()
		env := append([]string{}, baseEnv...)
		env = append(env, "FAKE_DRIVE9_UNPACK_CHAIN=1")
		result := runTIWithInput(t, bin, "", env, unpackArgs(localRoot)...)
		result.wantExitCode(1)
		result.wantStderrContains("unsafe symlink")
		if linkPath := filepath.Join(localRoot, "overlay", "repo", "a"); true {
			if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
				t.Fatalf("escaping chain link %q was left behind: %v", linkPath, err)
			}
		}
		// `s -> ..` resolves to the overlay root itself and is kept; only the
		// link that escapes through it is removed.
		if linkPath := filepath.Join(localRoot, "overlay", "repo", "s"); true {
			if _, err := os.Lstat(linkPath); err != nil {
				t.Fatalf("in-overlay chain link %q was removed: %v", linkPath, err)
			}
		}
	})

	t.Run("partial-failure-still-cleans", func(t *testing.T) {
		localRoot := t.TempDir()
		env := append([]string{}, baseEnv...)
		env = append(env,
			"FAKE_DRIVE9_UNPACK_SYMLINK_REL=repo/escape",
			"FAKE_DRIVE9_UNPACK_SYMLINK_TARGET=/etc/passwd",
			"FAKE_DRIVE9_UNPACK_FAIL=1",
		)
		result := runTIWithInput(t, bin, "", env, unpackArgs(localRoot)...)
		result.wantExitCode(1)
		result.wantStderrContains("partial install")
		linkPath := filepath.Join(localRoot, "overlay", "repo", "escape")
		if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
			t.Fatalf("escaping symlink survived a failed unpack: %v", err)
		}
	})

	t.Run("pre-existing-links-preserved", func(t *testing.T) {
		localRoot := t.TempDir()
		preexisting := filepath.Join(localRoot, "overlay", "other", "tool")
		if err := os.MkdirAll(filepath.Dir(preexisting), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/usr/bin/tool", preexisting); err != nil {
			t.Fatal(err)
		}
		env := append([]string{}, baseEnv...)
		result := runTIWithInput(t, bin, "", env, unpackArgs(localRoot)...)
		result.wantExitCode(0)
		result.wantStdoutContains(`"status": "unpacked"`)
		got, err := os.Readlink(preexisting)
		if err != nil || got != "/usr/bin/tool" {
			t.Fatalf("pre-existing link was modified: got=%q err=%v", got, err)
		}
	})

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

	t.Run("mount-without-local-root-fails-closed", func(t *testing.T) {
		env := append([]string{}, baseEnv...)
		result := runTIWithInput(t, bin, "", env, "fs", "unpack-file-system", "--mount-path", filepath.Join(home, "mnt"), "--remote-root", "/workspace")
		result.wantExitCode(1)
		result.wantStderrContains("cannot determine the local overlay root")
	})

	t.Run("missing-local-root-is-usage-error", func(t *testing.T) {
		env := append([]string{}, baseEnv...)
		result := runTIWithInput(t, bin, "", env, "fs", "unpack-file-system", "--remote-root", "/workspace")
		result.wantExitCode(2)
		result.wantStderrContains("--local-root")
	})
}
