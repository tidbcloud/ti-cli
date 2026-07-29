package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixInstallerDefaultsToTDCBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell installer is not supported on Windows")
	}
	script := filepath.Join("..", "scripts", "install.sh")
	syntax := exec.Command("sh", "-n", script)
	if output, err := syntax.CombinedOutput(); err != nil {
		t.Fatalf("install.sh syntax check failed: %v\n%s", err, output)
	}

	home := t.TempDir()
	cmd := exec.Command("sh", script, "--dry-run")
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --dry-run failed: %v\n%s", err, output)
	}
	got := string(output)
	installDir := filepath.Join(home, ".tdc", "bin")
	for _, want := range []string{
		"target: " + filepath.Join(installDir, "tdc"),
		"companion_target: " + filepath.Join(installDir, "tdc-drive9"),
		`path_export: export PATH="` + installDir + `:$PATH"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("installer dry-run should contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/usr/local/bin") {
		t.Fatalf("installer dry-run should not target /usr/local/bin:\n%s", got)
	}
}

func TestInstallersDoNotEscalatePrivileges(t *testing.T) {
	shellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shell := string(shellBytes)
	if strings.Contains(shell, "sudo") || strings.Contains(shell, "/usr/local/bin") {
		t.Fatalf("install.sh must remain user-owned and privilege-free")
	}

	powerShellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	powerShell := string(powerShellBytes)
	if !strings.Contains(powerShell, `$DefaultInstallDir = Join-Path (Join-Path $HOME ".tdc") "bin"`) {
		t.Fatalf("install.ps1 should default to $HOME\\.tdc\\bin")
	}
}

func TestInstallersExplainTelemetryControls(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "scripts", "install.sh"),
		filepath.Join("..", "scripts", "install.ps1"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		for _, want := range []string{
			"anonymous command usage and reliability telemetry",
			"~/.tdc/.preferences",
			"[telemetry]",
			"enabled = false",
			"TDC_TELEMETRY=off tdc ...",
		} {
			if !strings.Contains(strings.ToLower(content), strings.ToLower(want)) {
				t.Fatalf("%s does not contain telemetry notice text %q", path, want)
			}
		}
	}
}

func TestReleaseBuildConfiguresProductTelemetryEndpoint(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	const assignment = "internal/version.telemetryEndpoint=https://tdc-telemetry.tidbcloud.com/v1/telemetry/batch"
	if !strings.Contains(config, assignment) {
		t.Fatalf("GoReleaser config does not set the production telemetry endpoint %q", assignment)
	}
	if strings.Contains(config, "internal/version.telemetryEndpoint=http://") {
		t.Fatal("GoReleaser telemetry endpoint must use HTTPS")
	}
	if strings.Contains(config, "sslip.io") {
		t.Fatal("GoReleaser telemetry endpoint must not use a temporary sslip.io hostname")
	}
}
