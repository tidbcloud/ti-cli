package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/tdc/internal/fs/fscred"
)

func TestHelpAndVersion(t *testing.T) {
	bin := tdcBinary(t)

	missingCommand := runTDC(t, bin)
	missingCommand.wantExitCode(2)
	if missingCommand.stdout != "" {
		missingCommand.fail("stdout should be empty")
	}
	missingCommand.wantStderrContains("tdc [ERROR]: the following arguments are required: command")
	missingCommand.wantStderrContains("The TiDB Cloud Command Line Interface is a unified tool")
	missingCommand.wantStderrContains("usage: tdc <command> [<subcommand>] [parameters]")
	missingCommand.wantStderrContains("tdc <command> <subcommand> help")
	missingCommand.wantStderrNotContains("Commands:")
	if !strings.HasPrefix(missingCommand.stderr, "\ntdc [ERROR]:") {
		missingCommand.fail("stderr should start with a blank line followed by the error prefix")
	}

	root := runTDC(t, bin, "help")
	root.wantExitCode(0)
	root.wantStdoutContains("Commands:")
	root.wantStdoutContains("db")
	root.wantStdoutNotContains("-h,")
	root.wantStdoutContains("--region <string>")

	db := runTDC(t, bin, "db", "help")
	db.wantExitCode(0)
	db.wantStdoutContains("create-db-cluster")
	db.wantStdoutContains("create-db-sql-users")
	db.wantStdoutContains("format-db-connection-string")

	subcommand := runTDC(t, bin, "fs", "mount-file-system", "help")
	subcommand.wantExitCode(0)
	subcommand.wantStdoutContains("Mount a file system to a local path.")
	subcommand.wantStdoutContains("--mount-path")
	subcommand.wantStdoutContains("--foreground")
	subcommand.wantStdoutContains("--mount-profile")
	subcommand.wantStdoutContains("--local-root")
	subcommand.wantStdoutContains("--pack-path")

	copyFile := runTDC(t, bin, "fs", "copy-file", "help")
	copyFile.wantExitCode(0)
	copyFile.wantStdoutContains("--from-local")
	copyFile.wantStdoutContains("--to-remote")
	copyFile.wantStdoutContains("--from-stdin")
	copyFile.wantStdoutContains("--to-stdout")
	copyFile.wantStdoutContains("--description")

	chmodFile := runTDC(t, bin, "fs", "chmod-file", "help")
	chmodFile.wantExitCode(0)
	chmodFile.wantStdoutContains("--mode")

	deleteFileSystem := runTDC(t, bin, "fs", "delete-file-system", "help")
	deleteFileSystem.wantExitCode(0)
	deleteFileSystem.wantStdoutContains("--file-system-name")
	deleteFileSystem.wantStdoutNotContains("--confirm-file-system-name")

	createDBCluster := runTDC(t, bin, "db", "create-db-cluster", "help")
	createDBCluster.wantExitCode(0)
	createDBCluster.wantStdoutContains("--db-cluster-name <string> (required)")
	createDBCluster.wantStdoutContains("[--db-cluster-type <string>]")
	createDBCluster.wantStdoutNotContains("--db-cluster-type <string> (required)")
	createDBCluster.wantStdoutContains("--project-id <string>")
	createDBCluster.wantStdoutNotContains("--project-id <string> (required)")

	configure := runTDC(t, bin, "configure", "help")
	configure.wantExitCode(0)
	configure.wantStdoutContains("--region-code <string>")

	packFileSystem := runTDC(t, bin, "fs", "pack-file-system", "help")
	packFileSystem.wantExitCode(0)
	packFileSystem.wantStdoutContains("--archive-path")
	packFileSystem.wantStdoutContains("--mount-profile")
	packFileSystem.wantStdoutContains("--path")

	gitClone := runTDC(t, bin, "fs-git", "clone-git-workspace", "help")
	gitClone.wantExitCode(0)
	gitClone.wantStdoutContains("--repo-url")
	gitClone.wantStdoutContains("--target-path")
	gitClone.wantStdoutContains("--hydrate")

	version := runTDC(t, bin, "fs", "mount-file-system", "--version")
	version.wantExitCode(0)
	version.wantStdoutContains("tdc ")
}

func TestFSUnixAliasHelp(t *testing.T) {
	bin := tdcBinary(t)

	tests := []struct {
		alias     string
		canonical string
		flag      string
	}{
		{alias: "cp", canonical: "copy-file", flag: "--from-local"},
		{alias: "cat", canonical: "read-file", flag: "--path"},
		{alias: "ls", canonical: "list-files", flag: "--path"},
		{alias: "stat", canonical: "describe-file", flag: "--path"},
		{alias: "mv", canonical: "move-file", flag: "--from-remote"},
		{alias: "rm", canonical: "delete-file", flag: "--recursive"},
		{alias: "mkdir", canonical: "create-directory", flag: "--mode"},
		{alias: "chmod", canonical: "chmod-file", flag: "--mode"},
		{alias: "symlink", canonical: "create-symlink", flag: "--link-path"},
		{alias: "hardlink", canonical: "create-hardlink", flag: "--source-path"},
		{alias: "grep", canonical: "search-file-content", flag: "--pattern"},
		{alias: "find", canonical: "find-files", flag: "--file-name-pattern"},
		{alias: "mount", canonical: "mount-file-system", flag: "--mount-path"},
		{alias: "drain", canonical: "drain-file-system", flag: "--timeout"},
		{alias: "umount", canonical: "unmount-file-system", flag: "--ignore-absent"},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			result := runTDC(t, bin, "fs", tt.alias, "help")
			result.wantExitCode(0)
			result.wantStdoutContains("tdc fs " + tt.canonical)
			result.wantStdoutContains("Aliases:")
			result.wantStdoutContains("  " + tt.alias)
			result.wantStdoutContains(tt.flag)
		})
	}
}

func TestErrorsAreRenderedAtCLIBoundary(t *testing.T) {
	bin := tdcBinary(t)

	shortFlag := runTDC(t, bin, "-h")
	shortFlag.wantExitCode(2)
	shortFlag.wantStderrContains("tdc [ERROR]: short flags are not supported")

	unknown := runTDC(t, bin, "db", "missing-command")
	unknown.wantExitCode(2)
	unknown.wantStderrContains(`tdc [ERROR]: unknown command "missing-command" for "tdc db"`)

	removedConfirmation := runTDC(t, bin, "fs", "delete-file-system", "--file-system-name", "workspace", "--confirm-file-system-name", "workspace")
	removedConfirmation.wantExitCode(2)
	removedConfirmation.wantStderrContains(`unknown flag: --confirm-file-system-name`)

	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		artifact := artifactNameForRuntime(t)
		fmt.Fprintf(w, `{
			"tag_name": "v99.0.0",
			"html_url": "https://github.com/tidbcloud/tdc/releases/tag/v99.0.0",
			"assets": [
				{
					"name": %q,
					"browser_download_url": "https://github.com/tidbcloud/tdc/releases/download/v99.0.0/%s"
				}
			]
		}`, artifact, artifact)
	}))
	defer releaseServer.Close()

	checkUpdate := runTDCWithInput(t, bin, "", []string{"TDC_RELEASE_API_BASE_URL=" + releaseServer.URL}, "update", "--check", "--query", "latest_version")
	checkUpdate.wantExitCode(0)
	checkUpdate.wantStdoutContains(`"99.0.0"`)

	update := runTDC(t, bin, "update", "--dry-run")
	update.wantExitCode(1)
	update.wantStderrContains("tdc [ERROR]: tdc install source")

	directUpdate := runTDC(t, bin, "update")
	directUpdate.wantExitCode(1)
	directUpdate.wantStderrContains("tdc [ERROR]: tdc install source")
	directUpdate.wantStderrNotContains("requires --yes")

	invalidQuery := runTDCWithInput(t, bin, "", tdcConfigEnv(), append(createClusterDryRunArgs(), "--query", "command[")...)
	invalidQuery.wantExitCode(2)
	invalidQuery.wantStderrContains("tdc [ERROR]: invalid --query expression")
}

func TestTelemetryUsesFakeIngestionServer(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/telemetry/batch" {
			t.Errorf("unexpected telemetry request %s %s", request.Method, request.URL.Path)
		}
		body, _ := io.ReadAll(request.Body)
		requests <- body
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	secretName := "must-not-appear-in-telemetry"
	result := runTDCWithInput(t, bin, "", []string{
		"HOME=" + home,
		"TDC_ALLOW_TEST_ENDPOINTS=1",
		"TDC_TEST_TELEMETRY_ENDPOINT=" + server.URL + "/v1/telemetry/batch",
		"TDC_TELEMETRY=on",
		"TDC_LOGGING=off",
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=must-not-appear-public-key",
		"TDC_PRIVATE_KEY=must-not-appear-private-key",
	}, "db", "create-db-cluster", "--db-cluster-name", secretName, "--project-id", "must-not-appear-project", "--dry-run")
	result.wantExitCode(0)
	result.wantStdoutContains(`"dry_run": true`)

	var body []byte
	select {
	case body = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("tdc did not send telemetry to the fake ingestion server")
	}
	for _, prohibited := range []string{secretName, "must-not-appear-public-key", "must-not-appear-private-key", "must-not-appear-project"} {
		if strings.Contains(string(body), prohibited) {
			t.Fatalf("telemetry payload leaked %q: %s", prohibited, body)
		}
	}
	if !strings.Contains(string(body), `"command_path":"tdc db create-db-cluster"`) ||
		!strings.Contains(string(body), `"db-cluster-name"`) ||
		!strings.Contains(string(body), `"project-id"`) {
		t.Fatalf("unexpected telemetry payload: %s", body)
	}
	idPath := filepath.Join(home, ".tdc", ".telemetry-installation-id")
	id, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("read telemetry installation ID: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(id)), "tdc_") {
		t.Fatalf("unexpected telemetry installation ID %q", id)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(idPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("telemetry installation ID mode = %o, want 0600", info.Mode().Perm())
		}
	}
}

func TestOutputQueryAndDryRun(t *testing.T) {
	bin := tdcBinary(t)
	env := tdcConfigEnv()

	dryRun := runTDCWithInput(t, bin, "", env, createClusterDryRunArgs()...)
	dryRun.wantExitCode(0)
	dryRun.wantStdoutContains(`"dry_run": true`)
	dryRun.wantStdoutContains(`"would_send_request": true`)
	dryRun.wantStdoutContains(`"post_create_wait"`)

	regionOverride := runTDCWithInput(t, bin, "", []string{
		"TDC_PUBLIC_KEY=e2e-public",
		"TDC_PRIVATE_KEY=e2e-private",
	}, append([]string{"--region", "aws-ap-southeast-1"}, createClusterDryRunArgs()...)...)
	regionOverride.wantExitCode(0)
	regionOverride.wantStdoutContains("aws ap-southeast-1")

	text := runTDCWithInput(t, bin, "", env, append(createClusterDryRunArgs(), "--output", "text")...)
	text.wantExitCode(0)
	text.wantStdoutContains("Dry run: tdc db create-db-cluster")

	query := runTDCWithInput(t, bin, "", env, append(createClusterDryRunArgs(), "--query", "command")...)
	query.wantExitCode(0)
	query.wantStdoutContains(`"tdc db create-db-cluster"`)

	readOnlyDryRun := runTDC(t, bin, "db", "list-db-clusters", "--dry-run")
	readOnlyDryRun.wantExitCode(2)
	readOnlyDryRun.wantStderrContains("tdc [ERROR]: invalid flag for tdc db list-db-clusters: unknown flag: --dry-run")
}

func tdcConfigEnv() []string {
	return []string{
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=e2e-public",
		"TDC_PRIVATE_KEY=e2e-private",
	}
}

func createClusterDryRunArgs() []string {
	return []string{
		"db", "create-db-cluster",
		"--db-cluster-name", "demo-cluster",
		"--project-id", "project-1",
		"--wait",
		"--dry-run",
	}
}

func artifactNameForRuntime(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "linux":
		if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
			t.Skipf("unsupported release target %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return fmt.Sprintf("tdc_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	case "windows":
		if runtime.GOARCH != "amd64" {
			t.Skipf("unsupported release target %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return "tdc_windows_amd64.zip"
	default:
		t.Skipf("unsupported release target %s/%s", runtime.GOOS, runtime.GOARCH)
		return ""
	}
}

func TestConfigureWritesLocalProfile(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()
	env := append([]string{"HOME=" + home}, configureIAMEnv(t)...)

	result := runTDCWithInput(t, bin, "aws-us-east-1\npublic-key\nprivate-key\n", env, "configure", "--profile", "stage")
	result.wantExitCode(0)
	result.wantStdoutContains(`"project_id": "virtual-e2e"`)
	result.wantStdoutNotContains("private-key")

	configBytes, err := os.ReadFile(filepath.Join(home, ".tdc", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	credentialsPath := filepath.Join(home, ".tdc", "credentials")
	credentialsBytes, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}

	if !strings.Contains(string(configBytes), `[stage]`) ||
		strings.Contains(string(configBytes), `cloud_provider`) ||
		!strings.Contains(string(configBytes), `region_code = 'aws-us-east-1'`) ||
		!strings.Contains(string(configBytes), `project_id = 'virtual-e2e'`) {
		t.Fatalf("config did not contain expected stage profile:\n%s", string(configBytes))
	}
	if !strings.Contains(string(credentialsBytes), `tdc_public_key = 'public-key'`) ||
		!strings.Contains(string(credentialsBytes), `tdc_private_key = 'private-key'`) {
		t.Fatalf("credentials did not contain expected keys:\n%s", string(credentialsBytes))
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(credentialsPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials mode: want 0600, got %o", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".tdc", ".preferences")); !os.IsNotExist(err) {
		t.Fatalf("configure created global settings: %v", err)
	}
}

func TestConfigureNonInteractiveFromEnvironment(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()

	env := []string{
		"HOME=" + home,
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=ci-public",
		"TDC_PRIVATE_KEY=ci-private",
	}
	env = append(env, configureIAMEnv(t)...)
	result := runTDCWithInput(t, bin, "", env, "configure", "--profile", "ci", "--non-interactive")
	result.wantExitCode(0)
	result.wantStdoutContains(`"project_id": "virtual-e2e"`)
	result.wantStdoutNotContains("ci-private")

	configBytes, err := os.ReadFile(filepath.Join(home, ".tdc", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	credentialsBytes, err := os.ReadFile(filepath.Join(home, ".tdc", "credentials"))
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(configBytes), `[ci]`) ||
		!strings.Contains(string(configBytes), `region_code = 'aws-us-east-1'`) ||
		!strings.Contains(string(configBytes), `project_id = 'virtual-e2e'`) ||
		strings.Contains(string(configBytes), `cloud_provider`) {
		t.Fatalf("config did not contain expected ci profile:\n%s", string(configBytes))
	}
	if !strings.Contains(string(credentialsBytes), `tdc_public_key = 'ci-public'`) ||
		!strings.Contains(string(credentialsBytes), `tdc_private_key = 'ci-private'`) {
		t.Fatalf("credentials did not contain expected ci keys:\n%s", string(credentialsBytes))
	}
}

func TestFSResourceRegistrySelectionAcrossCommandFamilies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	bin := tdcBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "tdc-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	recordPath := filepath.Join(t.TempDir(), "calls.jsonl")
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":"https://fs-east.test","cloud_provider":"aws","tidb_region":"us-east-1"},{"region_code":"aws-us-west-2","mode":"tidb_cloud_native","server_url":"https://fs-west.test","cloud_provider":"aws","tidb_region":"us-west-2"}]}`)
	}))
	defer manifestServer.Close()
	baseEnv := []string{
		"HOME=" + home,
		"TDC_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"TDC_ALLOW_TEST_ENDPOINTS=1",
		"TDC_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
	}
	baseEnv = append(baseEnv, configureIAMEnv(t)...)
	configured := runTDCWithInput(t, bin, "", append(baseEnv,
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=e2e-public",
		"TDC_PRIVATE_KEY=e2e-private",
	), "configure", "--profile", "stage", "--non-interactive")
	configured.wantExitCode(0)
	missingWithZeroResources := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	missingWithZeroResources.wantExitCode(2)
	missingWithZeroResources.wantStderrContains("file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")

	createWorkspace := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-file-system", "--file-system-name", "workspace", "--wait")
	createWorkspace.wantExitCode(0)
	createWorkspace.wantStdoutContains(`"status": "ready"`)
	createWorkspace.wantStdoutContains(`"credentials_stored": true`)
	createWorkspace.wantStdoutContains(`"fs_token": "key-workspace"`)
	missingWithOneResource := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	missingWithOneResource.wantExitCode(2)
	missingWithOneResource.wantStderrContains("file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")
	createScratch := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "create-file-system", "--file-system-name", "scratch", "--wait")
	createScratch.wantExitCode(0)
	createScratch.wantStdoutContains(`"status": "ready"`)
	createScratch.wantStdoutContains(`"credentials_stored": true`)

	list := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems")
	list.wantExitCode(0)
	list.wantStdoutContains(`"file_system_name": "workspace"`)
	list.wantStdoutContains(`"file_system_name": "scratch"`)
	list.wantStdoutNotContains("key-workspace")
	list.wantStdoutNotContains("key-scratch")
	list.wantStdoutNotContains("default_file_system_name")
	list.wantStdoutNotContains("is_default")
	describe := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "describe-file-system", "--file-system-name", "scratch")
	describe.wantExitCode(0)
	describe.wantStdoutContains(`"tenant_id": "tenant-scratch"`)
	describe.wantStdoutContains(`"region_code": "aws-us-west-2"`)
	describe.wantStdoutNotContains("key-scratch")
	callsBeforeMissingSelectorCommands := len(readFakeDrive9Calls(t, recordPath))
	for _, args := range [][]string{
		{"fs", "list-files", "--path", "/"},
		{"fs-vault", "list-secrets"},
		{"fs-journal", "read-journal-entries", "--journal-id", "jrn-e2e"},
		{"fs-git", "hydrate-git-workspace", "--target-path", filepath.Join(home, "ambiguous-workspace")},
	} {
		missing := runTDCWithInput(t, bin, "", baseEnv, append([]string{"--profile", "stage"}, args...)...)
		missing.wantExitCode(2)
		missing.wantStderrContains("file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")
	}
	if calls := readFakeDrive9Calls(t, recordPath); len(calls) != callsBeforeMissingSelectorCommands {
		t.Fatalf("missing resource selection must fail before invoking Drive9: calls before=%d after=%d", callsBeforeMissingSelectorCommands, len(calls))
	}
	missingDryRun := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-directory", "--path", "/tmp", "--dry-run")
	missingDryRun.wantExitCode(2)
	missingDryRun.wantStderrContains("file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")

	dataPlane := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--file-system-name", "scratch", "--path", "/")
	dataPlane.wantExitCode(0)
	vault := runTDCWithInput(t, bin, "", append(baseEnv, "TDC_FS_FILE_SYSTEM_NAME=workspace"), "--profile", "stage", "fs-vault", "list-secrets")
	vault.wantExitCode(0)
	journal := runTDCWithInput(t, bin, "", append(baseEnv, "TDC_FS_FILE_SYSTEM_NAME=workspace"), "--profile", "stage", "fs-journal", "create-journal", "--file-system-name", "scratch", "--journal-id", "jrn-e2e")
	journal.wantExitCode(0)
	git := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs-git", "hydrate-git-workspace", "--file-system-name", "scratch", "--target-path", filepath.Join(home, "workspace"))
	git.wantExitCode(0)
	mount := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "mount-file-system", "--file-system-name", "scratch", "--mount-path", filepath.Join(home, "mount"), "--foreground")
	mount.wantExitCode(0)

	calls := readFakeDrive9Calls(t, recordPath)
	assertFakeDrive9Call(t, calls, []string{"create", "--json", "--name", "workspace"}, "", home, "stage", "workspace", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"create", "--json", "--name", "scratch"}, "", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"fs", "ls"}, "key-scratch", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"vault", "ls"}, "key-workspace", home, "stage", "workspace", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"journal", "new"}, "key-scratch", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"git", "hydrate"}, "key-scratch", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"mount"}, "key-scratch", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")

	deleteScratch := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "delete-file-system", "--file-system-name", "scratch")
	deleteScratch.wantExitCode(0)
	deleteScratch.wantStdoutContains(`"status": "deleting"`)
	afterDelete := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems")
	afterDelete.wantExitCode(0)
	afterDelete.wantStdoutContains(`"file_system_name": "workspace"`)
	afterDelete.wantStdoutNotContains(`"file_system_name": "scratch"`)
	stillMissingAfterDelete := runTDCWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	stillMissingAfterDelete.wantExitCode(2)
	stillMissingAfterDelete.wantStderrContains("file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")
	assertFakeDrive9Call(t, readFakeDrive9Calls(t, recordPath), []string{"delete", "--json", "--yes"}, "key-scratch", home, "stage", "scratch", "https://fs-west.test", "aws-us-west-2")

	for _, args := range [][]string{
		{"--profile", "stage", "fs", "set-default-file-system"},
		{"--profile", "stage", "fs", "unset-default-file-system"},
		{"--profile", "stage", "fs", "create-file-system", "--file-system-name", "removed-flag", "--set-default"},
	} {
		removed := runTDCWithInput(t, bin, "", baseEnv, args...)
		removed.wantExitCode(2)
	}
}

func TestFSConfigurationFreeAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	bin := tdcBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "tdc-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	recordPath := filepath.Join(t.TempDir(), "calls.jsonl")
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":"https://fs-east.test","cloud_provider":"aws","tidb_region":"us-east-1"}]}`)
	}))
	defer manifestServer.Close()
	baseEnv := []string{
		"HOME=" + home,
		"TDC_LOGGING=on",
		"TDC_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"TDC_ALLOW_TEST_ENDPOINTS=1",
		"TDC_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
	}
	authEnv := append(append([]string{}, baseEnv...),
		"TDC_FS_FILE_SYSTEM_NAME=workspace",
		"TDC_FS_TOKEN=configuration-free-token",
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=must-not-reach-data-plane",
	)

	localList := runTDCWithInput(t, bin, "", baseEnv, "fs", "list-file-systems")
	localList.wantExitCode(0)
	localList.wantStdoutContains(`"file_systems": []`)

	for _, args := range [][]string{
		{"fs", "check-file-system"},
		{"fs", "list-files", "--path", "/"},
		{"fs-journal", "create-journal", "--journal-id", "jrn-ephemeral"},
		{"fs-vault", "list-secrets"},
		{"fs-git", "hydrate-git-workspace", "--target-path", filepath.Join(home, "workspace")},
	} {
		result := runTDCWithInput(t, bin, "", authEnv, args...)
		result.wantExitCode(0)
	}

	flagsOnly := runTDCWithInput(t, bin, "", baseEnv,
		"--region", "aws-us-east-1",
		"fs", "list-files",
		"--file-system-name", "workspace",
		"--fs-token", "flag-token",
		"--path", "/",
	)
	flagsOnly.wantExitCode(0)

	mixed := runTDCWithInput(t, bin, "", append(baseEnv, "TDC_FS_TOKEN=mixed-token"),
		"--region", "aws-us-east-1",
		"fs", "list-files",
		"--file-system-name", "workspace",
		"--path", "/",
	)
	mixed.wantExitCode(0)

	mountPath := filepath.Join(home, "mount")
	mount := runTDCWithInput(t, bin, "", authEnv,
		"fs", "mount-file-system",
		"--mount-path", mountPath,
	)
	mount.wantExitCode(0)
	drain := runTDCWithInput(t, bin, "", baseEnv,
		"fs", "drain-file-system",
		"--mount-path", mountPath,
	)
	drain.wantExitCode(0)
	unmount := runTDCWithInput(t, bin, "", baseEnv,
		"fs", "unmount-file-system",
		"--mount-path", mountPath,
	)
	unmount.wantExitCode(0)

	for _, path := range []string{
		filepath.Join(home, ".tdc", "config"),
		filepath.Join(home, ".tdc", "credentials"),
		filepath.Join(home, ".tdc", "fs_resources"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("configuration-free command persisted tdc configuration at %s: %v", path, err)
		}
	}
	logData, err := os.ReadFile(filepath.Join(home, ".tdc", "logs", "tdc.jsonl"))
	if err != nil {
		t.Fatalf("read configuration-free operation log: %v", err)
	}
	for _, secret := range []string{"configuration-free-token", "flag-token", "mixed-token", "must-not-reach-data-plane"} {
		if strings.Contains(string(logData), secret) {
			t.Fatalf("configuration-free operation log leaked a credential")
		}
	}
	calls := readFakeDrive9Calls(t, recordPath)
	if len(calls) == 0 {
		t.Fatal("configuration-free commands did not invoke companion")
	}
	for _, call := range calls {
		if call.TDCPublicKey != "" || call.TDCPrivateKey != "" || call.TDCFSToken != "" || call.Drive9Public != "" || call.Drive9Private != "" {
			t.Fatalf("data-plane companion inherited TiDB Cloud or raw tdc secrets: %#v", call)
		}
	}
	assertFakeDrive9Call(t, calls, []string{"fs", "ls"}, "configuration-free-token", home, "default", "workspace", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"mount", "drain"}, "", home, "default", "workspace", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"umount"}, "", home, "default", "workspace", "https://fs-east.test", "aws-us-east-1")
}

type fakeDrive9Call struct {
	Args          []string `json:"args"`
	Home          string   `json:"home"`
	APIKey        string   `json:"api_key"`
	Server        string   `json:"server"`
	RegionCode    string   `json:"region_code"`
	TDCPublicKey  string   `json:"tdc_public_key,omitempty"`
	TDCPrivateKey string   `json:"tdc_private_key,omitempty"`
	TDCFSToken    string   `json:"tdc_fs_token,omitempty"`
	Drive9Public  string   `json:"drive9_public_key,omitempty"`
	Drive9Private string   `json:"drive9_private_key,omitempty"`
}

func readFakeDrive9Calls(t *testing.T, path string) []fakeDrive9Call {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []fakeDrive9Call
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var call fakeDrive9Call
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatalf("decode fake Drive9 call: %v", err)
		}
		calls = append(calls, call)
	}
	return calls
}

func assertFakeDrive9Call(t *testing.T, calls []fakeDrive9Call, prefix []string, apiKey, home, profileName, resourceName, server, regionCode string) {
	t.Helper()
	wantHome, err := fscred.CompanionHome(home, profileName, resourceName)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if len(call.Args) < len(prefix) {
			continue
		}
		matches := true
		for i := range prefix {
			if call.Args[i] != prefix[i] {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if call.APIKey != apiKey || call.Home != wantHome || call.Server != server || call.RegionCode != regionCode {
			t.Fatalf("unexpected fake Drive9 environment for %v: %#v, want api_key=%q home=%q server=%q region=%q", prefix, call, apiKey, wantHome, server, regionCode)
		}
		return
	}
	t.Fatalf("missing fake Drive9 call with prefix %v in %#v", prefix, calls)
}

func tdcBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TDC_E2E_BIN")
	if bin == "" {
		t.Skip("TDC_E2E_BIN is not set; run make e2e")
	}
	return bin
}

func TestOperationLogWritesSafeJSONL(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()

	env := []string{
		"HOME=" + home,
		"TDC_LOGGING=on",
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=ci-public-secret",
		"TDC_PRIVATE_KEY=ci-private-secret",
	}
	env = append(env, configureIAMEnv(t)...)
	result := runTDCWithInput(t, bin, "", env, "configure", "--profile", "ci", "--non-interactive")
	result.wantExitCode(0)

	logPath := filepath.Join(home, ".tdc", "logs", "tdc.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read operation log: %v", err)
	}
	if strings.Contains(string(data), "ci-public-secret") || strings.Contains(string(data), "ci-private-secret") {
		t.Fatalf("operation log leaked secret values:\n%s", string(data))
	}
	var event map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var candidate map[string]any
		if err := json.Unmarshal(line, &candidate); err != nil {
			t.Fatalf("decode operation log: %v\n%s", err, string(data))
		}
		if candidate["type"] == "command" {
			event = candidate
		}
	}
	if event["type"] != "command" || event["command"] != "tdc configure" || event["profile"] != "ci" {
		t.Fatalf("unexpected operation log event: %#v", event)
	}
}

func configureIAMEnv(t *testing.T) []string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/projects" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"projects":[{"id":"virtual-e2e","type":"tidbx_virtual"}]}`))
	}))
	t.Cleanup(server.Close)
	return []string{
		"TDC_ALLOW_TEST_ENDPOINTS=1",
		"TDC_TEST_IAM_BASE_URL=" + server.URL,
	}
}

func TestOperationLogCanBeDisabled(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()

	result := runTDCWithInput(t, bin, "", []string{
		"HOME=" + home,
		"TDC_LOGGING=off",
	}, "help")
	result.wantExitCode(0)
	if _, err := os.Stat(filepath.Join(home, ".tdc", "logs", "tdc.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no operation log file, got err=%v", err)
	}
}

func TestGlobalSettingsControlOperationLogging(t *testing.T) {
	bin := tdcBinary(t)

	t.Run("enabled", func(t *testing.T) {
		home := t.TempDir()
		writeE2EFile(t, filepath.Join(home, ".tdc", ".preferences"), "schema_version = 1\n\n[logging]\nenabled = true\nmax_file_mb = 1\nmax_files = 2\n", 0o600)
		for _, profileName := range []string{"default", "stage"} {
			result := runTDCUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "--profile", profileName, "help")
			result.wantExitCode(0)
		}
		data, err := os.ReadFile(filepath.Join(home, ".tdc", "logs", "tdc.jsonl"))
		if err != nil {
			t.Fatalf("enabled settings did not create operation log: %v", err)
		}
		profiles := make(map[string]bool)
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var event map[string]any
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatalf("decode operation log: %v", err)
			}
			if profileName, ok := event["profile"].(string); ok {
				profiles[profileName] = true
			}
		}
		if !profiles["default"] || !profiles["stage"] {
			t.Fatalf("global settings did not apply across profiles: %#v", profiles)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		home := t.TempDir()
		writeE2EFile(t, filepath.Join(home, ".tdc", ".preferences"), "[logging]\nenabled = false\n", 0o600)
		result := runTDCUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "help")
		result.wantExitCode(0)
		if _, err := os.Stat(filepath.Join(home, ".tdc", "logs", "tdc.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("disabled settings created operation log: %v", err)
		}
	})
}

func TestLegacyLoggingConfigMigratesThroughBinary(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()
	configPath := filepath.Join(home, ".tdc", "config")
	writeE2EFile(t, configPath, `[default]
region_code = "aws-us-east-1"
project_id = "virtual-e2e"

[logging]
enabled = false
max_file_mb = 3
max_files = 2
`, 0o644)
	credentialsPath := filepath.Join(home, ".tdc", "credentials")
	credentials := "[default]\ntdc_public_key = \"public\"\ntdc_private_key = \"private\"\n"
	writeE2EFile(t, credentialsPath, credentials, 0o600)

	result := runTDCUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "help")
	result.wantExitCode(0)

	settingsPath := filepath.Join(home, ".tdc", ".preferences")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read migrated settings: %v", err)
	}
	if !strings.Contains(string(settingsData), "[logging]") || !strings.Contains(string(settingsData), "enabled = false") {
		t.Fatalf("legacy logging was not migrated:\n%s", settingsData)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("migrated settings mode = %o, want 0600", info.Mode().Perm())
		}
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "[logging]") || !strings.Contains(string(configData), "[default]") || !strings.Contains(string(configData), "virtual-e2e") {
		t.Fatalf("legacy migration changed profile config incorrectly:\n%s", configData)
	}
	afterCredentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCredentials) != credentials {
		t.Fatalf("legacy migration changed credentials:\n%s", afterCredentials)
	}
}

func TestMalformedGlobalSettingsFailClosedThroughBinary(t *testing.T) {
	bin := tdcBinary(t)
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".tdc", ".preferences")
	before := "[logging]\nenabled = \"not-a-boolean\"\n"
	writeE2EFile(t, settingsPath, before, 0o644)

	result := runTDCUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "--debug", "help")
	result.wantExitCode(0)
	result.wantStderrContains("operation logging disabled because global settings could not be loaded")
	if strings.Contains(result.stderr, "not-a-boolean") {
		result.fail("debug diagnostic leaked settings contents")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("malformed settings were rewritten: %q", after)
	}
	if _, err := os.Stat(filepath.Join(home, ".tdc", "logs", "tdc.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("malformed settings did not fail closed: %v", err)
	}
}

func TestUpdateAndReservedProfileGlobalSettingsBoundaries(t *testing.T) {
	bin := tdcBinary(t)

	t.Run("update does not access tdc home", func(t *testing.T) {
		home := t.TempDir()
		settingsPath := filepath.Join(home, ".tdc", ".preferences")
		before := "not valid toml = ["
		writeE2EFile(t, settingsPath, before, 0o600)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			extension := "tar.gz"
			if runtime.GOOS == "windows" {
				extension = "zip"
			}
			assetName := fmt.Sprintf("tdc_%s_%s.%s", runtime.GOOS, runtime.GOARCH, extension)
			_, _ = fmt.Fprintf(w, `{"tag_name":"v0.1.5","html_url":"https://example.test/v0.1.5","assets":[{"name":%q,"browser_download_url":"https://example.test/%s"}]}`, assetName, assetName)
		}))
		defer server.Close()
		result := runTDCUsingLoggingSettings(t, bin, []string{
			"HOME=" + home,
			"TDC_LOGGING=on",
			"TDC_RELEASE_API_BASE_URL=" + server.URL,
		}, "--debug", "update", "--check")
		result.wantExitCode(0)
		result.wantStderrNotContains("operation logging disabled")
		after, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != before {
			t.Fatalf("update changed settings: %q", after)
		}
		if _, err := os.Stat(filepath.Join(home, ".tdc", "logs", "tdc.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("update wrote operation log: %v", err)
		}
	})

	t.Run("logging profile is reserved", func(t *testing.T) {
		home := t.TempDir()
		result := runTDCWithInput(t, bin, "", []string{
			"HOME=" + home,
			"TDC_LOGGING=off",
		}, "configure", "--profile", "logging", "--region-code", "aws-us-east-1", "--tdc-public-key", "public", "--tdc-private-key", "private", "--non-interactive")
		result.wantExitCode(2)
		result.wantStderrContains(`profile name "logging" is reserved`)
		for _, name := range []string{"config", "credentials", ".preferences"} {
			if _, err := os.Stat(filepath.Join(home, ".tdc", name)); !os.IsNotExist(err) {
				t.Fatalf("reserved profile wrote %s: %v", name, err)
			}
		}
	})
}

func writeE2EFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func runTDC(t *testing.T, bin string, args ...string) commandResult {
	t.Helper()
	return runTDCWithInput(t, bin, "", nil, args...)
}

func hasLiveFSCommandFamily(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "fs", "fs-git", "fs-journal", "fs-vault":
			return true
		}
	}
	return false
}

func runTDCWithInput(t *testing.T, bin, stdin string, env []string, args ...string) commandResult {
	return runTDCProcess(t, bin, stdin, env, true, args...)
}

func runTDCUsingLoggingSettings(t *testing.T, bin string, env []string, args ...string) commandResult {
	t.Helper()
	return runTDCProcess(t, bin, "", env, false, args...)
}

func runTDCProcess(t *testing.T, bin, stdin string, env []string, disableLoggingByDefault bool, args ...string) commandResult {
	t.Helper()
	if os.Getenv("TDC_LIVE") == "1" && hasLiveFSCommandFamily(args) && !envContains(env, "TDC_FS_FILE_SYSTEM_NAME") {
		name := strings.TrimSpace(os.Getenv("TDC_LIVE_FS_NAME"))
		if name == "" {
			name = "workspace"
		}
		env = append(env, "TDC_FS_FILE_SYSTEM_NAME="+name)
	}

	cmd := exec.Command(bin, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append([]string{}, os.Environ()...)
	if disableLoggingByDefault && !envContains(env, "TDC_LOGGING") {
		cmd.Env = append(cmd.Env, "TDC_LOGGING=off")
	}
	if len(env) > 0 {
		cmd.Env = append(cmd.Env, env...)
	}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run %s %s: %v", bin, strings.Join(args, " "), err)
		}
		exitCode = exitErr.ExitCode()
	}

	return commandResult{
		t:        t,
		args:     args,
		exitCode: exitCode,
		stdout:   stdout.String(),
		stderr:   stderr.String(),
	}
}

func envContains(env []string, key string) bool {
	prefix := key + "="
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

type commandResult struct {
	t        *testing.T
	args     []string
	exitCode int
	stdout   string
	stderr   string
}

func (r commandResult) wantExitCode(want int) {
	r.t.Helper()
	if r.exitCode != want {
		r.fail("exit code: want %d, got %d", want, r.exitCode)
	}
}

func (r commandResult) wantStdoutContains(want string) {
	r.t.Helper()
	if !strings.Contains(r.stdout, want) {
		r.fail("stdout should contain %q", want)
	}
}

func (r commandResult) wantStdoutNotContains(want string) {
	r.t.Helper()
	if strings.Contains(r.stdout, want) {
		r.fail("stdout should not contain %q", want)
	}
}

func (r commandResult) wantStderrContains(want string) {
	r.t.Helper()
	if !strings.Contains(r.stderr, want) {
		r.fail("stderr should contain %q", want)
	}
}

func (r commandResult) wantStderrNotContains(want string) {
	r.t.Helper()
	if strings.Contains(r.stderr, want) {
		r.fail("stderr should not contain %q", want)
	}
}

func (r commandResult) fail(format string, args ...any) {
	r.t.Helper()
	message := fmt.Sprintf(format, args...)
	r.t.Fatalf("%s", strings.Join([]string{
		"command: tdc " + strings.Join(r.args, " "),
		message,
		"stdout:\n" + r.stdout,
		"stderr:\n" + r.stderr,
	}, "\n"))
}
