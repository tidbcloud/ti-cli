package fswrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/version"
)

func setLocalBuild(t *testing.T, local bool) {
	t.Helper()
	info := version.Info{InstallSource: "archive"}
	if local {
		info.InstallSource = "local"
	}
	previous := testControlsAllowed
	testControlsAllowed = info.TestControlsAllowed
	t.Cleanup(func() { testControlsAllowed = previous })
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// companionOverride sets the hidden companion override to an executable in a
// fresh directory and empties PATH, so an accidental PATH lookup cannot satisfy
// the resolution.
func companionOverride(t *testing.T) string {
	t.Helper()
	override := filepath.Join(t.TempDir(), "override-drive9")
	writeExecutable(t, override)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv(envCompanionBin, override)
	return override
}

func TestLocateCompanionIgnoresDrive9OverrideInReleaseBuilds(t *testing.T) {
	companionOverride(t)
	setLocalBuild(t, false)
	path, err := (Runner{HomeDir: t.TempDir()}).locateCompanion()
	if apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("expected fs.companion_missing, got path %q, err %v", path, err)
	}
}

func TestLocateCompanionUsesDrive9OverrideInLocalBuilds(t *testing.T) {
	override := companionOverride(t)
	setLocalBuild(t, true)
	path, err := (Runner{HomeDir: t.TempDir()}).locateCompanion()
	if err != nil || path != override {
		t.Fatalf("expected TI_DRIVE9_BIN %q, got %q, %v", override, path, err)
	}
}

func TestLocateCompanionNeedsOptInEvenInLocalBuilds(t *testing.T) {
	companionOverride(t)
	setLocalBuild(t, true)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "")
	path, err := (Runner{HomeDir: t.TempDir()}).locateCompanion()
	if apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("expected fs.companion_missing, got path %q, err %v", path, err)
	}
}

func TestLocateCompanionKeepsExplicitCompanionPath(t *testing.T) {
	companionOverride(t)
	setLocalBuild(t, false)
	explicit := filepath.Join(t.TempDir(), "ti-drive9")
	writeExecutable(t, explicit)
	path, err := (Runner{CompanionPath: explicit}).locateCompanion()
	if err != nil || path != explicit {
		t.Fatalf("expected CompanionPath %q, got %q, %v", explicit, path, err)
	}
}

// TestLocateCompanionIgnoresPATH pins the reported issue: the companion receives
// the TiDB Cloud private key, the FS API key and vault tokens, so a valid
// ti-drive9 earlier on PATH must never be executed.
func TestLocateCompanionIgnoresPATH(t *testing.T) {
	pathDir := t.TempDir()
	writeExecutable(t, filepath.Join(pathDir, companionBinaryName("ti-drive9")))
	writeExecutable(t, filepath.Join(pathDir, companionBinaryName("drive9")))
	t.Setenv("PATH", pathDir)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv(envCompanionBin, "")
	allowTestCompanionOverride(t)

	path, err := (Runner{HomeDir: t.TempDir()}).locateCompanion()
	if apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("PATH companion was used: path %q, err %v", path, err)
	}
}
