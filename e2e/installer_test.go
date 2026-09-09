package e2e

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixInstallerDefaultsToTIBin(t *testing.T) {
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
	installDir := filepath.Join(home, ".ti", "bin")
	for _, want := range []string{
		"target: " + filepath.Join(installDir, "ti"),
		"companion_target: " + filepath.Join(installDir, "ti-drive9"),
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

func TestUnixInstallerInstallDirectoryCompatibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell installer is not supported on Windows")
	}
	script := filepath.Join("..", "scripts", "install.sh")
	home := t.TempDir()
	legacyDir := filepath.Join(home, "legacy-bin")
	legacy := exec.Command("sh", script, "--dry-run")
	legacy.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TDC_INSTALL_DIR=" + legacyDir}
	legacyOutput, err := legacy.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy install variable failed: %v\n%s", err, legacyOutput)
	}
	if !strings.Contains(string(legacyOutput), "target: "+filepath.Join(legacyDir, "ti")) {
		t.Fatalf("legacy install variable was not used:\n%s", legacyOutput)
	}

	conflict := exec.Command("sh", script, "--dry-run", "--install-dir", filepath.Join(home, "explicit"))
	conflict.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"TI_INSTALL_DIR=" + filepath.Join(home, "canonical"),
		"TDC_INSTALL_DIR=" + filepath.Join(home, "legacy"),
	}
	conflictOutput, err := conflict.CombinedOutput()
	if err == nil {
		t.Fatalf("conflicting install variables should fail:\n%s", conflictOutput)
	}
	if !strings.Contains(string(conflictOutput), "TI_INSTALL_DIR and deprecated TDC_INSTALL_DIR contain different values") {
		t.Fatalf("unexpected conflict output:\n%s", conflictOutput)
	}
}

func TestInstallersMigrateBeforeCreatingNewHome(t *testing.T) {
	shellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shell := string(shellBytes)
	migrateIndex := strings.LastIndex(shell, "run_home_migration \"$FOUND\"")
	installIndex := strings.LastIndex(shell, "install_file \"$FOUND\" \"$TARGET\"")
	if migrateIndex < 0 || installIndex < 0 || migrateIndex >= installIndex {
		t.Fatal("install.sh must invoke migration from the staged binary before creating the install destination")
	}
	if !strings.Contains(shell, `REPO="tidbcloud/ti-cli"`) {
		t.Fatal("install.sh must download releases from tidbcloud/ti-cli")
	}

	powerShellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	powerShell := string(powerShellBytes)
	migrateIndex = strings.LastIndex(powerShell, "Invoke-HomeMigration $Extracted.FullName")
	installIndex = strings.LastIndex(powerShell, "New-Item -ItemType Directory -Force -Path $InstallDir")
	if migrateIndex < 0 || installIndex < 0 || migrateIndex >= installIndex {
		t.Fatal("install.ps1 must invoke migration from the staged binary before creating the install destination")
	}
	if !strings.Contains(powerShell, `$Repo = "tidbcloud/ti-cli"`) {
		t.Fatal("install.ps1 must download releases from tidbcloud/ti-cli")
	}
}

func TestUnixInstallerMigratesLegacyStateAndPreservesPathShadow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell installer is not supported on Windows")
	}
	bin := tiBinary(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	legacyHome := filepath.Join(home, ".tdc")
	writeE2EFile(t, filepath.Join(legacyHome, "config"), "[default]\nregion_code = 'aws-us-east-1'\n", 0o644)
	writeE2EFile(t, filepath.Join(legacyHome, "credentials"), "[default]\ntdc_public_key = 'public'\ntdc_private_key = 'private'\n", 0o600)

	assetDir := filepath.Join(root, "assets")
	packageDir := filepath.Join(root, "package")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	packagedTI := filepath.Join(packageDir, "ti")
	copyTestFile(t, bin, packagedTI, 0o755)
	artifact := fmt.Sprintf("ti_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := filepath.Join(assetDir, artifact)
	if err := os.MkdirAll(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tarCommand := exec.Command("tar", "-czf", archive, "-C", packageDir, "ti")
	if output, err := tarCommand.CombinedOutput(); err != nil {
		t.Fatalf("build installer fixture archive: %v\n%s", err, output)
	}
	archiveData, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveChecksum := sha256.Sum256(archiveData)
	writeE2EFile(t, filepath.Join(assetDir, "ti_checksums.txt"), fmt.Sprintf("%x  %s\n", archiveChecksum, artifact), 0o600)

	companionArtifact := fmt.Sprintf("drive9-%s-%s", runtime.GOOS, runtime.GOARCH)
	companion := filepath.Join(assetDir, companionArtifact)
	companionCalls := filepath.Join(root, "companion-calls")
	writeE2EFile(t, companion, compatibleDrive9ShellFixture(companionCalls), 0o755)
	companionData, err := os.ReadFile(companion)
	if err != nil {
		t.Fatal(err)
	}
	companionChecksum := sha256.Sum256(companionData)
	writeE2EFile(t, filepath.Join(assetDir, "drive9_checksums.txt"), fmt.Sprintf("%x  %s\n", companionChecksum, companionArtifact), 0o600)

	pathDir := filepath.Join(root, "path")
	shadow := filepath.Join(pathDir, "ti")
	writeE2EFile(t, shadow, "#!/bin/sh\nprintf 'unrelated ti\\n'\n", 0o755)
	fakeCurl := filepath.Join(pathDir, "curl")
	writeE2EFile(t, fakeCurl, fmt.Sprintf(`#!/bin/sh
out=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "$url" in
  */ti_checksums.txt) source=%q ;;
  */%s) source=%q ;;
  */checksums.txt) source=%q ;;
  */%s) source=%q ;;
  *) printf 'unexpected URL: %%s\n' "$url" >&2; exit 1 ;;
esac
if [ -n "$out" ]; then
  cp "$source" "$out"
else
  cat "$source"
fi
`, filepath.Join(assetDir, "ti_checksums.txt"), artifact, archive, filepath.Join(assetDir, "drive9_checksums.txt"), companionArtifact, companion), 0o755)

	script := filepath.Join("..", "scripts", "install.sh")
	command := exec.Command("sh", script, "--yes")
	command.Env = []string{
		"HOME=" + home,
		"PATH=" + pathDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install fixture release: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "PATH shadowing detected") || !strings.Contains(string(output), shadow) {
		t.Fatalf("installer did not report the unrelated ti on PATH:\n%s", output)
	}
	if strings.Contains(string(output), "ti fs companion installed to") {
		t.Fatalf("installer exposed the companion installation path:\n%s", output)
	}
	companionCallData, err := os.ReadFile(companionCalls)
	if err != nil {
		t.Fatalf("read companion validation calls: %v", err)
	}
	for _, want := range []string{"fs layer help", "mount --help"} {
		if !strings.Contains(string(companionCallData), want) {
			t.Fatalf("installer did not validate companion command surface %q; calls:\n%s", want, companionCallData)
		}
	}
	for _, regionCode := range []string{"aws-us-east-1", "aws-ap-southeast-1", "aws-us-west-2", "alicloud-ap-southeast-1"} {
		if !strings.Contains(string(output), regionCode) {
			t.Fatalf("installer did not list ti fs region %q:\n%s", regionCode, output)
		}
	}
	if data, err := os.ReadFile(shadow); err != nil || !strings.Contains(string(data), "unrelated ti") {
		t.Fatalf("installer replaced PATH shadow: %q, %v", data, err)
	}
	for _, installed := range []string{
		filepath.Join(home, ".ti", "bin", "ti"),
		filepath.Join(home, ".ti", "bin", "ti-drive9"),
	} {
		if info, err := os.Stat(installed); err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("missing installed executable %s: %v", installed, err)
		}
	}
	migratedCredentials, err := os.ReadFile(filepath.Join(home, ".ti", "credentials"))
	if err != nil || !strings.Contains(string(migratedCredentials), "tidb_cloud_public_key") || strings.Contains(string(migratedCredentials), "tdc_public_key") {
		t.Fatalf("legacy credentials were not migrated canonically: %q, %v", migratedCredentials, err)
	}
	legacyCredentials, err := os.ReadFile(filepath.Join(legacyHome, "credentials"))
	if err != nil || !strings.Contains(string(legacyCredentials), "tdc_public_key") {
		t.Fatalf("installer modified legacy credentials: %q, %v", legacyCredentials, err)
	}
}

func compatibleDrive9ShellFixture(callLog string) string {
	return fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
if [ "$1 $2 $3" = "fs layer help" ]; then
  printf 'usage: drive9 fs layer <create|list|fork|chain|delete>\n'
  exit 0
fi
if [ "$1 $2" = "mount --help" ]; then
  printf '  -layer string\n  -checkpoint string\n'
  exit 0
fi
printf 'compatible companion\n'
`, callLog)
}

func copyTestFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	writeE2EFile(t, destination, string(data), mode)
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
	if !strings.Contains(powerShell, `$DefaultInstallDir = Join-Path (Join-Path $HOME ".ti") "bin"`) {
		t.Fatalf("install.ps1 should default to $HOME\\.ti\\bin")
	}
}

func TestInstallersUseProductOwnedFSRegionsAndHideCompanionPath(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "scripts", "install.sh"),
		filepath.Join("..", "scripts", "install.ps1"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if strings.Contains(content, "drive9-regions.json") {
			t.Fatalf("%s still downloads the Drive9 region manifest", path)
		}
		if strings.Contains(content, "ti fs companion installed to") {
			t.Fatalf("%s still reports the companion installation path", path)
		}
		for _, regionCode := range []string{"aws-us-east-1", "aws-ap-southeast-1", "aws-us-west-2", "alicloud-ap-southeast-1"} {
			if !strings.Contains(content, regionCode) {
				t.Fatalf("%s does not list ti fs region %q", path, regionCode)
			}
		}
	}
}

func TestInstallersValidateCompanionCommandSurfaceBeforeInstallation(t *testing.T) {
	shellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	shell := string(shellBytes)
	shellValidation := strings.LastIndex(shell, `validate_companion "${TMP_DIR}/${COMPANION_ARTIFACT}"`)
	shellInstall := strings.LastIndex(shell, `install_file "${TMP_DIR}/${COMPANION_ARTIFACT}" "$COMPANION_TARGET"`)
	if shellValidation < 0 || shellInstall < 0 || shellValidation >= shellInstall {
		t.Fatal("install.sh must validate the staged companion before installing it")
	}

	powerShellBytes, err := os.ReadFile(filepath.Join("..", "scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	powerShell := string(powerShellBytes)
	powerShellValidation := strings.LastIndex(powerShell, "Assert-CompanionCommandSurface $CompanionPath")
	powerShellInstall := strings.LastIndex(powerShell, "Move-Item -Force -Path $CompanionPath -Destination $CompanionTarget")
	if powerShellValidation < 0 || powerShellInstall < 0 || powerShellValidation >= powerShellInstall {
		t.Fatal("install.ps1 must validate the staged companion before installing it")
	}
	for _, required := range []string{"fork", "chain", "delete", "layer", "checkpoint"} {
		if !strings.Contains(shell, required) || !strings.Contains(powerShell, required) {
			t.Fatalf("installers do not validate required companion surface %q", required)
		}
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
			"~/.ti/.preferences",
			"[telemetry]",
			"enabled = false",
			"TI_TELEMETRY=off ti ...",
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
