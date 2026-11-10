package fswrap

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

func TestDrive9EnvironmentSanitizesTISecrets(t *testing.T) {
	t.Setenv("TIDB_CLOUD_PUBLIC_KEY", "ambient-public")
	t.Setenv("TIDB_CLOUD_PRIVATE_KEY", "ambient-private")
	t.Setenv("TI_FS_TOKEN", "ambient-fs-token")
	t.Setenv("TI_VAULT_TOKEN", "ambient-vault-token")
	t.Setenv("TDC_PUBLIC_KEY", "legacy-public")
	t.Setenv("TDC_PRIVATE_KEY", "legacy-private")
	t.Setenv("TDC_FS_TOKEN", "legacy-fs-token")
	t.Setenv("TDC_VAULT_TOKEN", "legacy-vault-token")
	t.Setenv("DRIVE9_API_KEY", "ambient-drive9-token")
	profile := &config.Profile{
		Name:                  "default",
		FSResourceName:        "workspace",
		FSPlacementRegionCode: "aws-us-east-1",
		FSCloudProvider:       "aws",
		FSRegionCode:          "us-east-1",
		FSAPIKey:              "selected-fs-token",
		TiDBCloudPublicKey:    "selected-public",
		TiDBCloudPrivateKey:   "selected-private",
	}
	runner := Runner{Resolver: endpoints.Resolver{FSBaseURLs: map[endpoints.ProviderRegion]string{
		{Provider: "aws", Region: "us-east-1"}: "https://fs.test",
	}}}

	env, err := runner.drive9Env(t.TempDir(), RunOptions{
		Profile:         profile,
		IncludeFSAPIKey: true,
		VaultToken:      "selected-vault-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(env)
	for _, key := range []string{
		"TIDB_CLOUD_PUBLIC_KEY", "TIDB_CLOUD_PRIVATE_KEY", "TI_FS_TOKEN", "TI_VAULT_TOKEN",
		"TDC_PUBLIC_KEY", "TDC_PRIVATE_KEY", "TDC_FS_TOKEN", "TDC_VAULT_TOKEN",
	} {
		if _, ok := values[key]; ok {
			t.Fatalf("%s leaked into companion environment", key)
		}
	}
	if values["DRIVE9_API_KEY"] != "selected-fs-token" || values["DRIVE9_VAULT_TOKEN"] != "selected-vault-token" {
		t.Fatalf("selected credentials were not mapped correctly: %#v", values)
	}
	if _, ok := values["DRIVE9_PUBLIC_KEY"]; ok {
		t.Fatalf("data-plane environment included TiDB Cloud public key")
	}
	if _, ok := values["DRIVE9_PRIVATE_KEY"]; ok {
		t.Fatalf("data-plane environment included TiDB Cloud private key")
	}

	env, err = runner.drive9Env(t.TempDir(), RunOptions{Profile: profile, IncludeTIKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	values = environmentMap(env)
	if values["DRIVE9_PUBLIC_KEY"] != "selected-public" || values["DRIVE9_PRIVATE_KEY"] != "selected-private" {
		t.Fatalf("create/delete credentials were not mapped correctly: %#v", values)
	}
	if _, ok := values["DRIVE9_API_KEY"]; ok {
		t.Fatalf("provision environment unexpectedly included FS token")
	}
}

func environmentMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func writeCompanionFixture(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// allowTestCompanionOverride simulates a local build that opted into hidden
// test controls. TI_DRIVE9_BIN is ignored otherwise, so tests that exercise the
// override must enable the same seam the binary uses.
func allowTestCompanionOverride(t *testing.T) {
	t.Helper()
	previous := testControlsAllowed
	testControlsAllowed = func() bool { return true }
	t.Cleanup(func() { testControlsAllowed = previous })
}

// TestLocateCompanionRejectsUntrustedCandidates covers the reported secret
// exposure: the companion receives the TiDB Cloud private key, the FS API key and
// vault tokens, so a relative, writable, or foreign-owned binary must never be
// executed.
func TestLocateCompanionRejectsUntrustedCandidates(t *testing.T) {
	allowTestCompanionOverride(t)
	home := t.TempDir()
	runner := Runner{HomeDir: home}
	t.Setenv("PATH", t.TempDir())

	t.Setenv(envCompanionBin, "./ti-drive9")
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
		t.Fatalf("relative TI_DRIVE9_BIN error = %v", err)
	}

	// POSIX mode bits are not enforced on Windows (see owner_other.go), so the
	// mode-based rejections below only run where they carry meaning.
	if runtime.GOOS != "windows" {
		writable := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o777)
		t.Setenv(envCompanionBin, writable)
		if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
			t.Fatalf("world-writable companion error = %v", err)
		}
	}

	nonExecutable := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o644)
	t.Setenv(envCompanionBin, nonExecutable)
	if runtime.GOOS != "windows" {
		if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
			t.Fatalf("non-executable companion error = %v", err)
		}
	}
}

// TestValidateCompanionBinaryRequiresAbsolutePath covers the shared guard
// directly: implicit candidates are always absolute, so only a direct caller can
// reach this branch.
func TestValidateCompanionBinaryRequiresAbsolutePath(t *testing.T) {
	if err := validateCompanionBinary("ti-drive9"); !errors.Is(err, errCompanionPathNotAbsolute) {
		t.Fatalf("relative path error = %v, want errCompanionPathNotAbsolute", err)
	}
}

// TestLocateCompanionUsesTrustedBinary ensures a valid absolute override works.
func TestLocateCompanionUsesTrustedBinary(t *testing.T) {
	allowTestCompanionOverride(t)
	home := t.TempDir()
	runner := Runner{HomeDir: home}

	trusted := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o755)
	t.Setenv(envCompanionBin, trusted)
	path, err := runner.locateCompanion()
	if err != nil {
		t.Fatal(err)
	}
	if path != trusted {
		t.Fatalf("resolved %q, want %q", path, trusted)
	}

	// A candidate on PATH is never accepted, even when it passes the file checks.
	pathDir := t.TempDir()
	writeCompanionFixture(t, pathDir, "ti-drive9", 0o755)
	t.Setenv(envCompanionBin, "")
	t.Setenv("PATH", pathDir)
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("PATH companion error = %v", err)
	}
}

// TestLocateCompanionAcceptsOwnGroupWritableInstall documents the umask-002
// compatibility case: a binary written 0775 inside the user's own directory (and
// group) is accepted, because otherwise every default install would fail.
func TestLocateCompanionAcceptsOwnGroupWritableInstall(t *testing.T) {
	allowTestCompanionOverride(t)
	home := t.TempDir()
	runner := Runner{HomeDir: home}
	path := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o775)
	t.Setenv(envCompanionBin, path)
	resolved, err := runner.locateCompanion()
	if err != nil {
		t.Fatalf("group-writable install in the user's own group rejected: %v", err)
	}
	if resolved != path {
		t.Fatalf("resolved %q, want %q", resolved, path)
	}
}

// TestLocateCompanionRejectsUnrelatedGroupWritableBinary exercises the
// unrelated-group branch: the mode check runs before the executable check, and
// the fixture is a valid executable so only the group rule can reject it.
func TestLocateCompanionRejectsUnrelatedGroupWritableBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("group ownership is not enforced on Windows")
	}
	allowTestCompanionOverride(t)
	previous := currentProcessGroup
	currentProcessGroup = func(uint32) bool { return false }
	t.Cleanup(func() { currentProcessGroup = previous })

	runner := Runner{HomeDir: t.TempDir()}
	path := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o775)
	t.Setenv(envCompanionBin, path)
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
		t.Fatalf("group-writable companion outside the caller's groups error = %v", err)
	}
}

// TestLocateCompanionRejectsDirectoryOwnedByAnotherUser covers the directory
// ownership rule: the parent must belong to the current user or root.
func TestLocateCompanionRejectsDirectoryOwnedByAnotherUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory ownership is not enforced on Windows")
	}
	allowTestCompanionOverride(t)
	previous := fileOwnerUID
	fileOwnerUID = func(info os.FileInfo) (uint32, bool) {
		if info.IsDir() {
			return 65534, true
		}
		return uint32(os.Getuid()), true
	}
	t.Cleanup(func() { fileOwnerUID = previous })

	runner := Runner{HomeDir: t.TempDir()}
	path := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o755)
	t.Setenv(envCompanionBin, path)
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
		t.Fatalf("directory owned by another uid error = %v", err)
	}
}

// TestLocateCompanionRejectsWritableDirectory rejects a binary another account
// could replace through its parent directory.
func TestLocateCompanionRejectsWritableDirectory(t *testing.T) {
	allowTestCompanionOverride(t)
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced on Windows")
	}
	home := t.TempDir()
	runner := Runner{HomeDir: home}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	writeCompanionFixture(t, dir, "ti-drive9", 0o755)
	t.Setenv(envCompanionBin, filepath.Join(dir, "ti-drive9"))
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
		t.Fatalf("binary in a world-writable directory error = %v", err)
	}
}

// TestLocateCompanionResolvesSymlinkedBinary rejects a link in a trusted
// directory that points at a binary another account could replace.
func TestLocateCompanionResolvesSymlinkedBinary(t *testing.T) {
	allowTestCompanionOverride(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	runner := Runner{HomeDir: t.TempDir()}
	target := writeCompanionFixture(t, t.TempDir(), "ti-drive9", 0o755)
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "ti-drive9")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCompanionBin, link)
	// The link itself lives in a safe directory, so only the resolved target's
	// directory reveals that another account could swap the binary.
	if _, err := runner.locateCompanion(); err != nil {
		t.Fatalf("symlinked companion in a trusted directory rejected: %v", err)
	}

	untrusted := t.TempDir()
	if err := os.Chmod(untrusted, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(untrusted, 0o755) })
	swapTarget := writeCompanionFixture(t, untrusted, "ti-drive9", 0o755)
	swapLink := filepath.Join(t.TempDir(), "ti-drive9")
	if err := os.Symlink(swapTarget, swapLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCompanionBin, swapLink)
	if _, err := runner.locateCompanion(); apperr.CodeFor(err) != "fs.companion_untrusted" {
		t.Fatalf("symlink to a world-writable directory error = %v", err)
	}
}
