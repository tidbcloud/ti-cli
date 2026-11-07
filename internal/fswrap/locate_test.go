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

// companionOverrides sets both companion switches, with an override binary and
// a bare drive9 on an otherwise empty PATH, and no installed ti-drive9.
func companionOverrides(t *testing.T) (override, standalone string) {
	t.Helper()
	dir := t.TempDir()
	override = filepath.Join(dir, "override-drive9")
	writeExecutable(t, override)
	pathDir := t.TempDir()
	standalone = filepath.Join(pathDir, companionBinaryName("drive9"))
	writeExecutable(t, standalone)
	t.Setenv("PATH", pathDir)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv(envCompanionBin, override)
	t.Setenv(envAllowDrive9, "1")
	return override, standalone
}

func TestLocateCompanionIgnoresDrive9OverridesInReleaseBuilds(t *testing.T) {
	companionOverrides(t)
	setLocalBuild(t, false)
	path, err := (Runner{}).locateCompanion()
	if apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("expected fs.companion_missing, got path %q, err %v", path, err)
	}
}

func TestLocateCompanionUsesDrive9OverridesInLocalBuilds(t *testing.T) {
	override, standalone := companionOverrides(t)
	setLocalBuild(t, true)
	path, err := (Runner{}).locateCompanion()
	if err != nil || path != override {
		t.Fatalf("expected TI_DRIVE9_BIN %q, got %q, %v", override, path, err)
	}

	t.Setenv(envCompanionBin, "")
	path, err = (Runner{}).locateCompanion()
	if err != nil || path != standalone {
		t.Fatalf("expected the standalone drive9 %q, got %q, %v", standalone, path, err)
	}
}

func TestLocateCompanionNeedsOptInEvenInLocalBuilds(t *testing.T) {
	companionOverrides(t)
	setLocalBuild(t, true)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "")
	path, err := (Runner{}).locateCompanion()
	if apperr.CodeFor(err) != "fs.companion_missing" {
		t.Fatalf("expected fs.companion_missing, got path %q, err %v", path, err)
	}
}

func TestLocateCompanionKeepsExplicitCompanionPath(t *testing.T) {
	companionOverrides(t)
	setLocalBuild(t, false)
	explicit := filepath.Join(t.TempDir(), "ti-drive9")
	writeExecutable(t, explicit)
	path, err := (Runner{CompanionPath: explicit}).locateCompanion()
	if err != nil || path != explicit {
		t.Fatalf("expected CompanionPath %q, got %q, %v", explicit, path, err)
	}
}
