package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

func TestHelpAndVersion(t *testing.T) {
	bin := tiBinary(t)

	missingCommand := runTI(t, bin)
	missingCommand.wantExitCode(2)
	if missingCommand.stdout != "" {
		missingCommand.fail("stdout should be empty")
	}
	missingCommand.wantStderrContains("ti [ERROR]: the following arguments are required: command")
	missingCommand.wantStderrContains("The TiDB Cloud Command Line Interface is a unified tool")
	missingCommand.wantStderrContains("usage: ti <command> [<subcommand>] [parameters]")
	missingCommand.wantStderrContains("ti <command> <subcommand> help")
	missingCommand.wantStderrNotContains("Commands:")
	if !strings.HasPrefix(missingCommand.stderr, "\nti [ERROR]:") {
		missingCommand.fail("stderr should start with a blank line followed by the error prefix")
	}

	root := runTI(t, bin, "help")
	root.wantExitCode(0)
	root.wantStdoutContains("Commands:")
	root.wantStdoutContains("db")
	root.wantStdoutNotContains("-h,")
	root.wantStdoutContains("--region <string>")

	db := runTI(t, bin, "db", "help")
	db.wantExitCode(0)
	db.wantStdoutContains("create-db-cluster")
	db.wantStdoutContains("create-db-sql-users")
	db.wantStdoutContains("format-db-connection-string")

	subcommand := runTI(t, bin, "fs", "mount-file-system", "help")
	subcommand.wantExitCode(0)
	subcommand.wantStdoutContains("Mount a file system to a local path.")
	subcommand.wantStdoutContains("--mount-path")
	subcommand.wantStdoutNotContains("--foreground")
	subcommand.wantStdoutContains("--mount-profile")
	subcommand.wantStdoutContains("--local-root")
	subcommand.wantStdoutContains("--pack-path")
	vaultMount := runTI(t, bin, "fs-vault", "mount-vault", "help")
	vaultMount.wantExitCode(0)
	vaultMount.wantStdoutNotContains("--foreground")
	for _, args := range [][]string{
		{"fs", "mount-file-system", "--mount-path", "/tmp/ti-mount", "--foreground"},
		{"fs-vault", "mount-vault", "--mount-path", "/tmp/ti-vault", "--foreground"},
	} {
		removedForeground := runTI(t, bin, args...)
		removedForeground.wantExitCode(2)
		removedForeground.wantStderrContains("unknown flag: --foreground")
	}

	copyFile := runTI(t, bin, "fs", "copy-file", "help")
	copyFile.wantExitCode(0)
	copyFile.wantStdoutContains("--from-local")
	copyFile.wantStdoutContains("--to-remote")
	copyFile.wantStdoutContains("--from-stdin")
	copyFile.wantStdoutContains("--to-stdout")
	copyFile.wantStdoutContains("--description")

	chmodFile := runTI(t, bin, "fs", "chmod-file", "help")
	chmodFile.wantExitCode(0)
	chmodFile.wantStdoutContains("--mode")

	deleteFileSystem := runTI(t, bin, "fs", "delete-file-system", "help")
	deleteFileSystem.wantExitCode(0)
	deleteFileSystem.wantStdoutContains("--file-system-id")
	deleteFileSystem.wantStdoutNotContains("--file-system-name")
	deleteFileSystem.wantStdoutNotContains("--confirm-file-system-name")
	createFileSystem := runTI(t, bin, "fs", "create-file-system", "help")
	createFileSystem.wantExitCode(0)
	createFileSystem.wantStdoutContains("[--display-name <string>]")
	createFileSystem.wantStdoutContains("[--label <string>]")
	listFileSystems := runTI(t, bin, "fs", "list-file-systems", "help")
	listFileSystems.wantExitCode(0)
	listFileSystems.wantStdoutContains("[--display-name <string>]")
	listFileSystems.wantStdoutContains("[--label <string>]")

	createDBCluster := runTI(t, bin, "db", "create-db-cluster", "help")
	createDBCluster.wantExitCode(0)
	createDBCluster.wantStdoutContains("--db-cluster-name <string> (required)")
	createDBCluster.wantStdoutContains("--db-cluster-type <string> (required)")
	createDBCluster.wantStdoutNotContains("[--db-cluster-type <string>]")
	createDBCluster.wantStdoutNotContains("--project-id")
	removedProjectFlag := runTI(t, bin, "db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", "demo", "--project-id", "project-1", "--dry-run")
	removedProjectFlag.wantExitCode(2)
	removedProjectFlag.wantStderrContains("unknown flag: --project-id")
	removedOrganization := runTI(t, bin, "organization", "list-projects")
	removedOrganization.wantExitCode(2)
	removedOrganization.wantStderrContains(`unknown command "organization"`)

	configure := runTI(t, bin, "configure", "help")
	configure.wantExitCode(0)
	configure.wantStdoutContains("--region-code <string>")

	packFileSystem := runTI(t, bin, "fs", "pack-file-system", "help")
	packFileSystem.wantExitCode(0)
	packFileSystem.wantStdoutContains("--archive-path")
	packFileSystem.wantStdoutContains("--mount-profile")
	packFileSystem.wantStdoutContains("--path")

	gitClone := runTI(t, bin, "fs-git", "clone-git-workspace", "help")
	gitClone.wantExitCode(0)
	gitClone.wantStdoutContains("--repo-url")
	gitClone.wantStdoutContains("--target-path")
	gitClone.wantStdoutContains("--hydrate")

	version := runTI(t, bin, "fs", "mount-file-system", "--version")
	version.wantExitCode(0)
	version.wantStdoutContains("ti ")
}

func TestFSUnixAliasHelp(t *testing.T) {
	bin := tiBinary(t)

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
			result := runTI(t, bin, "fs", tt.alias, "help")
			result.wantExitCode(0)
			result.wantStdoutContains("ti fs " + tt.canonical)
			result.wantStdoutContains("Aliases:")
			result.wantStdoutContains("  " + tt.alias)
			result.wantStdoutContains(tt.flag)
		})
	}
}

func TestErrorsAreRenderedAtCLIBoundary(t *testing.T) {
	bin := tiBinary(t)

	shortFlag := runTI(t, bin, "-h")
	shortFlag.wantExitCode(2)
	shortFlag.wantStderrContains("ti [ERROR]: short flags are not supported")

	unknown := runTI(t, bin, "db", "missing-command")
	unknown.wantExitCode(2)
	unknown.wantStderrContains(`ti [ERROR]: unknown command "missing-command" for "ti db"`)

	removedConfirmation := runTI(t, bin, "fs", "delete-file-system", "--file-system-id", "tenant-workspace", "--confirm-file-system-name", "workspace")
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
			"html_url": "https://github.com/tidbcloud/ti-cli/releases/tag/v99.0.0",
			"assets": [
				{
					"name": %q,
					"browser_download_url": "https://github.com/tidbcloud/ti-cli/releases/download/v99.0.0/%s"
				}
			]
		}`, artifact, artifact)
	}))
	defer releaseServer.Close()

	checkUpdate := runTIWithInput(t, bin, "", []string{"TI_RELEASE_API_BASE_URL=" + releaseServer.URL}, "update", "--check", "--query", "latest_version")
	checkUpdate.wantExitCode(0)
	checkUpdate.wantStdoutContains(`"99.0.0"`)

	update := runTI(t, bin, "update", "--dry-run")
	update.wantExitCode(1)
	update.wantStderrContains("ti [ERROR]: ti install source")

	directUpdate := runTI(t, bin, "update")
	directUpdate.wantExitCode(1)
	directUpdate.wantStderrContains("ti [ERROR]: ti install source")
	directUpdate.wantStderrNotContains("requires --yes")

	invalidQuery := runTIWithInput(t, bin, "", tiConfigEnv(), append(createClusterDryRunArgs(), "--query", "command[")...)
	invalidQuery.wantExitCode(2)
	invalidQuery.wantStderrContains("ti [ERROR]: invalid --query expression")
}

func TestHomeMigrationThroughRealBinary(t *testing.T) {
	bin := tiBinary(t)

	t.Run("lazy migration", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, ".tdc")
		writeE2EFile(t, filepath.Join(legacy, "config"), "[default]\nregion_code = 'aws-us-east-1'\n", 0o644)
		writeE2EFile(t, filepath.Join(legacy, "credentials"), "[default]\ntdc_public_key = 'public'\ntdc_private_key = 'private'\n", 0o600)
		writeE2EFile(t, filepath.Join(legacy, "logs", "tdc.jsonl"), "not migrated\n", 0o600)

		result := runTIWithInput(t, bin, "", []string{"HOME=" + home}, "help")
		result.wantExitCode(0)
		result.wantStdoutContains("The TiDB Cloud Command Line Interface")

		credentials, err := os.ReadFile(filepath.Join(home, ".ti", "credentials"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(credentials), "tidb_cloud_public_key") || strings.Contains(string(credentials), "tdc_public_key") {
			t.Fatalf("migrated credentials are not canonical:\n%s", credentials)
		}
		if _, err := os.Stat(filepath.Join(home, ".tdc", "credentials")); err != nil {
			t.Fatalf("legacy credentials changed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".ti", "logs")); !os.IsNotExist(err) {
			t.Fatalf("runtime logs were migrated: %v", err)
		}
	})

	t.Run("both homes conflict", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, ".tdc"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(home, ".ti"), 0o700); err != nil {
			t.Fatal(err)
		}
		result := runTIWithInput(t, bin, "", []string{"HOME=" + home}, "help")
		result.wantExitCode(2)
		result.wantStderrContains("both " + filepath.Join(home, ".tdc") + " and " + filepath.Join(home, ".ti") + " exist")
	})

	t.Run("update bypasses migration", func(t *testing.T) {
		home := t.TempDir()
		for _, name := range []string{".tdc", ".ti"} {
			if err := os.Mkdir(filepath.Join(home, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		result := runTIWithInput(t, bin, "", []string{"HOME=" + home}, "update", "help")
		result.wantExitCode(0)
		result.wantStdoutContains("Update this tool.")
	})
}

func TestLegacyEnvironmentCompatibilityDoesNotPolluteOutput(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	result := runTIWithInput(t, bin, "", []string{
		"HOME=" + home,
		"TDC_PROFILE=legacy",
		"TDC_REGION_CODE=aws-us-east-1",
	}, "help")
	result.wantExitCode(0)
	result.wantStderrNotContains("deprecated")
	result.wantStdoutNotContains("deprecated")
	if _, err := os.Stat(filepath.Join(home, ".ti")); !os.IsNotExist(err) {
		t.Fatalf("help in a fresh home unexpectedly created state: %v", err)
	}

	conflictHome := t.TempDir()
	conflict := runTIWithInput(t, bin, "", []string{
		"HOME=" + conflictHome,
		"TI_REGION_CODE=aws-us-east-1",
		"TDC_REGION_CODE=aws-us-west-2",
	}, "help")
	conflict.wantExitCode(2)
	conflict.wantStderrContains("environment variables TI_REGION_CODE and TDC_REGION_CODE contain different values")
	if _, err := os.Stat(filepath.Join(conflictHome, ".ti")); !os.IsNotExist(err) {
		t.Fatalf("environment conflict mutated state: %v", err)
	}
}

func TestTelemetryUsesFakeIngestionServer(t *testing.T) {
	bin := tiBinary(t)
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
	result := runTIWithInput(t, bin, "", []string{
		"HOME=" + home,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_TELEMETRY_ENDPOINT=" + server.URL + "/v1/telemetry/batch",
		"TI_TELEMETRY=on",
		"TI_TELEMETRY_TAG=e2b-preview",
		`TI_TELEMETRY_EXTRA={"campaign":"launch","runtime":"e2b"}`,
		"TI_LOGGING=off",
		"TI_REGION_CODE=aws-us-east-1",
		"TIDB_CLOUD_PUBLIC_KEY=must-not-appear-public-key",
		"TIDB_CLOUD_PRIVATE_KEY=must-not-appear-private-key",
	}, "db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", secretName, "--dry-run")
	result.wantExitCode(0)
	result.wantStdoutContains(`"dry_run": true`)

	var body []byte
	select {
	case body = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("ti did not send telemetry to the fake ingestion server")
	}
	for _, prohibited := range []string{secretName, "must-not-appear-public-key", "must-not-appear-private-key"} {
		if strings.Contains(string(body), prohibited) {
			t.Fatalf("telemetry payload leaked %q: %s", prohibited, body)
		}
	}
	if !strings.Contains(string(body), `"schema_version":2`) ||
		!strings.Contains(string(body), `"command_path":"ti db create-db-cluster"`) ||
		!strings.Contains(string(body), `"db-cluster-name"`) ||
		!strings.Contains(string(body), `"db-cluster-type"`) ||
		!strings.Contains(string(body), `"tag":"e2b-preview"`) ||
		!strings.Contains(string(body), `"extra":{"campaign":"launch","runtime":"e2b"}`) {
		t.Fatalf("unexpected telemetry payload: %s", body)
	}
	idPath := filepath.Join(home, ".ti", ".telemetry-installation-id")
	id, err := os.ReadFile(idPath)
	if err != nil {
		t.Fatalf("read telemetry installation ID: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(id)), "ti_") {
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
	bin := tiBinary(t)
	env := tiConfigEnv()

	dryRun := runTIWithInput(t, bin, "", env, createClusterDryRunArgs()...)
	dryRun.wantExitCode(0)
	dryRun.wantStdoutContains(`"dry_run": true`)
	dryRun.wantStdoutContains(`"would_send_request": true`)
	dryRun.wantStdoutContains(`"post_create_wait"`)

	regionOverride := runTIWithInput(t, bin, "", []string{
		"TIDB_CLOUD_PUBLIC_KEY=e2e-public",
		"TIDB_CLOUD_PRIVATE_KEY=e2e-private",
	}, append([]string{"--region", "aws-ap-southeast-1"}, createClusterDryRunArgs()...)...)
	regionOverride.wantExitCode(0)
	regionOverride.wantStdoutContains("aws ap-southeast-1")

	text := runTIWithInput(t, bin, "", env, append(createClusterDryRunArgs(), "--output", "text")...)
	text.wantExitCode(0)
	text.wantStdoutContains("Dry run: ti db create-db-cluster")

	query := runTIWithInput(t, bin, "", env, append(createClusterDryRunArgs(), "--query", "command")...)
	query.wantExitCode(0)
	query.wantStdoutContains(`"ti db create-db-cluster"`)

	readOnlyDryRun := runTI(t, bin, "db", "list-db-clusters", "--dry-run")
	readOnlyDryRun.wantExitCode(2)
	readOnlyDryRun.wantStderrContains("ti [ERROR]: invalid flag for ti db list-db-clusters: unknown flag: --dry-run")
}

func TestStarterOnlyDBGuardrailsThroughBinary(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	mutations := 0
	var listFilters []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters":
			listFilters = append(listFilters, r.URL.Query().Get("filter"))
			_, _ = w.Write([]byte(`{
				"clusters":[
					{"clusterId":"starter-east","displayName":"starter-east","servicePlan":"Starter","region":{"name":"regions/aws-us-east-1"}},
					{"clusterId":"starter-west","displayName":"starter-west","servicePlan":"Starter","region":{"regionId":"us-west-2","cloudProvider":"aws"}},
					{"clusterId":"starter-ali","displayName":"starter-ali","servicePlan":"Starter","region":{"regionId":"ap-southeast-1","cloudProvider":"alicloud"}},
					{"clusterId":"starter-unknown","displayName":"starter-unknown","servicePlan":"Starter"},
					{"clusterId":"essential-1","displayName":"essential","servicePlan":"Essential","region":{"name":"regions/aws-us-east-1"}}
				],
				"nextPageToken":"token-2",
				"totalSize":5
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/essential-1":
			_, _ = w.Write([]byte(`{"clusterId":"essential-1","displayName":"essential","servicePlan":"Essential"}`))
		default:
			mutations++
			t.Errorf("unexpected request after Starter guard: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	writeE2EFile(t, filepath.Join(home, ".ti", "config"), "[default]\nregion_code = 'aws-us-east-1'\nproject_id = 'project-1'\n", 0o600)
	writeE2EFile(t, filepath.Join(home, ".ti", "credentials"), "[default]\ntidb_cloud_public_key = 'public'\ntidb_cloud_private_key = 'private'\n", 0o600)
	env := []string{
		"HOME=" + home,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_STARTER_BASE_URL=" + server.URL,
		"TI_LOGGING=on",
	}

	list := runTIWithInput(t, bin, "", env, "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1")
	list.wantExitCode(0)
	list.wantStdoutContains(`"id": "starter-east"`)
	list.wantStdoutContains(`"next_page_token"`)
	list.wantStdoutNotContains("starter-west")
	list.wantStdoutNotContains("starter-ali")
	list.wantStdoutNotContains("starter-unknown")
	list.wantStdoutNotContains("essential-1")
	list.wantStdoutNotContains("total_size")

	westList := runTIWithInput(t, bin, "", env, "--region", "aws-us-west-2", "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1")
	westList.wantExitCode(0)
	westList.wantStdoutContains(`"id": "starter-west"`)
	westList.wantStdoutNotContains("starter-east")
	westList.wantStdoutNotContains("starter-ali")
	westList.wantStdoutNotContains("starter-unknown")

	wantFilters := []string{
		`region.provider="aws" AND region.name="regions/aws-us-east-1"`,
		`region.provider="aws" AND region.name="regions/aws-us-west-2"`,
	}
	if !reflect.DeepEqual(listFilters, wantFilters) {
		t.Fatalf("list filters = %#v, want %#v", listFilters, wantFilters)
	}

	describe := runTIWithInput(t, bin, "", env, "db", "describe-db-cluster", "--db-cluster-id", "essential-1")
	describe.wantExitCode(2)
	describe.wantStderrContains(`cluster "essential-1" uses database cluster type "essential"`)

	update := runTIWithInput(t, bin, "", env, "db", "update-db-cluster", "--db-cluster-id", "essential-1", "--db-cluster-name", "renamed")
	update.wantExitCode(2)
	update.wantStderrContains(`database cluster type "essential", which is not supported`)
	if mutations != 0 {
		t.Fatalf("non-Starter commands sent %d mutation requests", mutations)
	}

	logData, err := os.ReadFile(filepath.Join(home, ".ti", "logs", "ti.jsonl"))
	if err != nil {
		t.Fatalf("read operation log: %v", err)
	}
	if !strings.Contains(string(logData), `"error_code":"db.cluster_type_not_supported"`) {
		t.Fatalf("operation log does not contain stable guard error code:\n%s", logData)
	}
}

func TestCreateDBClusterUsesServerDefaultProjectThroughBinary(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta1/clusters" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if _, ok := body["labels"]; ok {
			t.Errorf("create request with no configured project must omit labels: %#v", body)
			http.Error(w, "unexpected labels", http.StatusBadRequest)
			return
		}
		created = true
		_, _ = w.Write([]byte(`{"clusterId":"starter-1","displayName":"server-default-project","servicePlan":"Starter","state":"CREATING","labels":{"tidb.cloud/project":"server-selected","custom":"preserved"}}`))
	}))
	defer server.Close()

	writeE2EFile(t, filepath.Join(home, ".ti", "config"), "[default]\nregion_code = 'aws-us-east-1'\nproject_id = 'legacy-must-not-be-sent'\n", 0o600)
	writeE2EFile(t, filepath.Join(home, ".ti", "credentials"), "[default]\ntidb_cloud_public_key = 'public'\ntidb_cloud_private_key = 'private'\n", 0o600)
	env := []string{
		"HOME=" + home,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_STARTER_BASE_URL=" + server.URL,
	}

	result := runTIWithInput(t, bin, "", env, "db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", "server-default-project")
	result.wantExitCode(0)
	result.wantStdoutContains(`"id": "starter-1"`)
	result.wantStdoutContains(`"tidb.cloud/project": "server-selected"`)
	result.wantStdoutContains(`"custom": "preserved"`)
	if !created {
		t.Fatal("create request was not sent")
	}
}

func tiConfigEnv() []string {
	return []string{
		"TI_REGION_CODE=aws-us-east-1",
		"TIDB_CLOUD_PUBLIC_KEY=e2e-public",
		"TIDB_CLOUD_PRIVATE_KEY=e2e-private",
	}
}

func createClusterDryRunArgs() []string {
	return []string{
		"db", "create-db-cluster",
		"--db-cluster-type", "starter",
		"--db-cluster-name", "demo-cluster",
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
		return fmt.Sprintf("ti_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	case "windows":
		if runtime.GOARCH != "amd64" {
			t.Skipf("unsupported release target %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return "ti_windows_amd64.zip"
	default:
		t.Skipf("unsupported release target %s/%s", runtime.GOOS, runtime.GOARCH)
		return ""
	}
}

func TestConfigureWritesLocalProfile(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	env := []string{"HOME=" + home}

	result := runTIWithInput(t, bin, "aws-us-east-1\npublic-key\nprivate-key\n", env, "configure", "--profile", "stage")
	result.wantExitCode(0)
	result.wantStdoutNotContains("project")
	result.wantStdoutNotContains("private-key")

	configBytes, err := os.ReadFile(filepath.Join(home, ".ti", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	credentialsPath := filepath.Join(home, ".ti", "credentials")
	credentialsBytes, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}

	if !strings.Contains(string(configBytes), `[stage]`) ||
		strings.Contains(string(configBytes), `cloud_provider`) ||
		!strings.Contains(string(configBytes), `region_code = 'aws-us-east-1'`) ||
		strings.Contains(string(configBytes), `project_id`) {
		t.Fatalf("config did not contain expected stage profile:\n%s", string(configBytes))
	}
	if !strings.Contains(string(credentialsBytes), `tidb_cloud_public_key = 'public-key'`) ||
		!strings.Contains(string(credentialsBytes), `tidb_cloud_private_key = 'private-key'`) {
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
	if _, err := os.Stat(filepath.Join(home, ".ti", ".preferences")); !os.IsNotExist(err) {
		t.Fatalf("configure created global settings: %v", err)
	}
}

func TestConfigureNonInteractiveFromEnvironment(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()

	env := []string{
		"HOME=" + home,
		"TI_REGION_CODE=aws-us-east-1",
		"TIDB_CLOUD_PUBLIC_KEY=ci-public",
		"TIDB_CLOUD_PRIVATE_KEY=ci-private",
	}
	result := runTIWithInput(t, bin, "", env, "configure", "--profile", "ci", "--non-interactive")
	result.wantExitCode(0)
	result.wantStdoutNotContains("project")
	result.wantStdoutNotContains("ci-private")

	configBytes, err := os.ReadFile(filepath.Join(home, ".ti", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	credentialsBytes, err := os.ReadFile(filepath.Join(home, ".ti", "credentials"))
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(configBytes), `[ci]`) ||
		!strings.Contains(string(configBytes), `region_code = 'aws-us-east-1'`) ||
		strings.Contains(string(configBytes), `project_id`) ||
		strings.Contains(string(configBytes), `cloud_provider`) {
		t.Fatalf("config did not contain expected ci profile:\n%s", string(configBytes))
	}
	if !strings.Contains(string(credentialsBytes), `tidb_cloud_public_key = 'ci-public'`) ||
		!strings.Contains(string(credentialsBytes), `tidb_cloud_private_key = 'ci-private'`) {
		t.Fatalf("credentials did not contain expected ci keys:\n%s", string(credentialsBytes))
	}
}

func TestFSRemoteInventoryAndIDCredentialSelectionAcrossCommandFamilies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	bin := tiBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	recordPath := filepath.Join(t.TempDir(), "calls.jsonl")
	eastControl := newFakeFSTenantControlPlane(t, "aws-us-east-1", "tenant-aws-us-east-1")
	defer eastControl.close()
	westControl := newFakeFSTenantControlPlane(t, "aws-us-west-2", "tenant-aws-us-west-2")
	defer westControl.close()
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":%q,"cloud_provider":"aws","tidb_region":"us-east-1"},{"region_code":"aws-us-west-2","mode":"tidb_cloud_native","server_url":%q,"cloud_provider":"aws","tidb_region":"us-west-2"}]}`, eastControl.URL(), westControl.URL())
	}))
	defer manifestServer.Close()
	baseEnv := []string{
		"HOME=" + home,
		"TI_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
	}
	configured := runTIWithInput(t, bin, "", append(baseEnv,
		"TI_REGION_CODE=aws-us-east-1",
		"TIDB_CLOUD_PUBLIC_KEY=e2e-public",
		"TIDB_CLOUD_PRIVATE_KEY=e2e-private",
	), "configure", "--profile", "stage", "--non-interactive")
	configured.wantExitCode(0)
	invalidDisplayName := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-file-system", "--display-name", "bad name")
	invalidDisplayName.wantExitCode(2)
	invalidDisplayName.wantStderrContains("--display-name must be 4-64 characters")
	duplicateLabel := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-file-system", "--label", "team=ai", "--label", "team=data")
	duplicateLabel.wantExitCode(2)
	duplicateLabel.wantStderrContains("duplicate label key")
	multipleListLabels := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems", "--label", "team=ai", "--label", "environment=test")
	multipleListLabels.wantExitCode(2)
	multipleListLabels.wantStderrContains("--label can be provided at most once")
	missingWithZeroResources := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	missingWithZeroResources.wantExitCode(2)
	missingWithZeroResources.wantStderrContains("file system ID is required")

	createWorkspace := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-file-system",
		"--display-name", "agent-workspace", "--label", "environment=production", "--label", "team=ai", "--wait")
	createWorkspace.wantExitCode(0)
	createWorkspace.wantStdoutContains(`"status": "ready"`)
	createWorkspace.wantStdoutContains(`"credentials_stored": true`)
	createWorkspace.wantStdoutContains(`"file_system_id": "tenant-aws-us-east-1"`)
	createWorkspace.wantStdoutContains(`"display_name": "agent-workspace"`)
	createWorkspace.wantStdoutContains(`"environment": "production"`)
	createWorkspace.wantStdoutContains(`"team": "ai"`)
	missingWithOneResource := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	missingWithOneResource.wantExitCode(2)
	missingWithOneResource.wantStderrContains("file system ID is required")
	createScratch := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "create-file-system", "--wait")
	createScratch.wantExitCode(0)
	createScratch.wantStdoutContains(`"status": "ready"`)
	createScratch.wantStdoutContains(`"credentials_stored": true`)
	createScratch.wantStdoutContains(`"display_name": "tenant-aws-us-west-2"`)
	createScratch.wantStdoutContains(`"labels": {}`)

	list := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems")
	list.wantExitCode(0)
	list.wantStdoutContains(`"file_system_id": "tenant-aws-us-east-1"`)
	list.wantStdoutNotContains(`"file_system_id": "tenant-aws-us-west-2"`)
	list.wantStdoutContains(`"has_local_token": true`)
	list.wantStdoutContains(`"display_name": "agent-workspace"`)
	list.wantStdoutContains(`"labels": {`)
	list.wantStdoutNotContains("drive9_")
	list.wantStdoutNotContains("default_file_system_name")
	list.wantStdoutNotContains("is_default")
	textList := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems", "--output", "text")
	textList.wantExitCode(0)
	textList.wantStdoutContains("FILE_SYSTEM_ID")
	textList.wantStdoutContains("DISPLAY_NAME")
	textList.wantStdoutContains("agent-workspace")
	textList.wantStdoutContains("tenant-aws-us-east-1")
	textList.wantStdoutNotContains(`"file_system_id"`)
	filteredList := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems",
		"--display-name", "agent", "--label", "environment=production")
	filteredList.wantExitCode(0)
	filteredList.wantStdoutContains(`"file_system_id": "tenant-aws-us-east-1"`)
	filteredList.wantStdoutNotContains(`"file_system_id": "tenant-aws-us-west-2"`)
	queriedList := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems",
		"--label", "team=ai", "--query", "file_systems[0].display_name")
	queriedList.wantExitCode(0)
	queriedList.wantStdoutContains(`"agent-workspace"`)
	dryRunCreate := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-file-system",
		"--display-name", "dry-run-workspace", "--label", "environment=test", "--wait", "--dry-run")
	dryRunCreate.wantExitCode(0)
	dryRunCreate.wantStdoutContains(`"path": "/v1/admin/tenants"`)
	dryRunCreate.wantStdoutContains(`"display_name": "dry-run-workspace"`)
	dryRunCreate.wantStdoutContains(`"environment": "test"`)
	dryRunCreate.wantStdoutNotContains("e2e-public")
	dryRunCreate.wantStdoutNotContains("e2e-private")
	westList := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "list-file-systems")
	westList.wantExitCode(0)
	westList.wantStdoutContains(`"file_system_id": "tenant-aws-us-west-2"`)
	westList.wantStdoutNotContains(`"file_system_id": "tenant-aws-us-east-1"`)
	describe := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "describe-file-system", "--file-system-id", "tenant-aws-us-west-2")
	describe.wantExitCode(0)
	describe.wantStdoutContains(`"file_system_id": "tenant-aws-us-west-2"`)
	describe.wantStdoutContains(`"display_name": "tenant-aws-us-west-2"`)
	describe.wantStdoutContains(`"labels": {}`)
	describe.wantStdoutContains(`"quota": {`)
	describe.wantStdoutContains(`"region_code": "aws-us-west-2"`)
	describe.wantStdoutNotContains("drive9_")
	textDescribe := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "describe-file-system", "--file-system-id", "tenant-aws-us-west-2", "--output", "text")
	textDescribe.wantExitCode(0)
	textDescribe.wantStdoutContains("File system ID: tenant-aws-us-west-2")
	textDescribe.wantStdoutContains("Display name: tenant-aws-us-west-2")
	textDescribe.wantStdoutContains("Labels: none")
	textDescribe.wantStdoutNotContains(`"file_system_id"`)
	callsBeforeMissingSelectorCommands := len(readFakeDrive9Calls(t, recordPath))
	for _, args := range [][]string{
		{"fs", "list-files", "--path", "/"},
		{"fs-vault", "list-secrets"},
		{"fs-journal", "read-journal-entries", "--journal-id", "jrn-e2e"},
		{"fs-git", "hydrate-git-workspace", "--target-path", filepath.Join(home, "ambiguous-workspace")},
	} {
		missing := runTIWithInput(t, bin, "", baseEnv, append([]string{"--profile", "stage"}, args...)...)
		missing.wantExitCode(2)
		missing.wantStderrContains("file system ID is required")
	}
	if calls := readFakeDrive9Calls(t, recordPath); len(calls) != callsBeforeMissingSelectorCommands {
		t.Fatalf("missing resource selection must fail before invoking Drive9: calls before=%d after=%d", callsBeforeMissingSelectorCommands, len(calls))
	}
	missingDryRun := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "create-directory", "--path", "/tmp", "--dry-run")
	missingDryRun.wantExitCode(2)
	missingDryRun.wantStderrContains("file system ID is required")

	dataPlane := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--file-system-id", "tenant-aws-us-west-2", "--path", "/")
	dataPlane.wantExitCode(0)
	vault := runTIWithInput(t, bin, "", append(baseEnv, "TI_FS_FILE_SYSTEM_ID=tenant-aws-us-east-1"), "--profile", "stage", "fs-vault", "list-secrets")
	vault.wantExitCode(0)
	journal := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs-journal", "create-journal", "--file-system-id", "tenant-aws-us-west-2", "--journal-id", "jrn-e2e")
	journal.wantExitCode(0)
	git := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs-git", "hydrate-git-workspace", "--file-system-id", "tenant-aws-us-west-2", "--target-path", filepath.Join(home, "workspace"))
	git.wantExitCode(0)
	mount := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "mount-file-system", "--file-system-id", "tenant-aws-us-west-2", "--mount-path", filepath.Join(home, "mount"))
	mount.wantExitCode(0)
	profileFork := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "fork-layer", "--file-system-id", "tenant-aws-us-west-2", "--parent-layer-ref", "parent-id", "--layer-id", "profile-child")
	profileFork.wantExitCode(0)
	profileFork.wantStdoutContains(`"layer_id": "child-id"`)
	profileChain := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "list-layer-chain", "--file-system-id", "tenant-aws-us-west-2", "--layer-ref", "profile-child")
	profileChain.wantExitCode(0)
	profileChain.wantStdoutContains(`"chain"`)
	profileDeleteLayer := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "delete-layer", "--file-system-id", "tenant-aws-us-west-2", "--layer-ref", "profile-child")
	profileDeleteLayer.wantExitCode(0)
	profileDeleteLayer.wantStdoutContains(`"status": "abandoned"`)

	calls := readFakeDrive9Calls(t, recordPath)
	for _, call := range calls {
		if len(call.Args) > 0 && call.Args[0] == "create" || len(call.Args) >= 3 && call.Args[0] == "admin" && call.Args[1] == "tenant" {
			t.Fatalf("direct file system control plane unexpectedly invoked ti-drive9: %#v", call.Args)
		}
	}
	assertFakeDrive9Call(t, calls, []string{"fs", "ls"}, drive9TestToken("tenant-aws-us-west-2"), home, "stage", "tenant-aws-us-west-2", westControl.URL(), "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"vault", "ls"}, drive9TestToken("tenant-aws-us-east-1"), home, "stage", "tenant-aws-us-east-1", eastControl.URL(), "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"fs", "layer", "fork"}, drive9TestToken("tenant-aws-us-west-2"), home, "stage", "tenant-aws-us-west-2", westControl.URL(), "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"fs", "layer", "chain"}, drive9TestToken("tenant-aws-us-west-2"), home, "stage", "tenant-aws-us-west-2", westControl.URL(), "aws-us-west-2")
	assertFakeDrive9Call(t, calls, []string{"fs", "layer", "delete"}, drive9TestToken("tenant-aws-us-west-2"), home, "stage", "tenant-aws-us-west-2", westControl.URL(), "aws-us-west-2")
	if !eastControl.hasRequest(http.MethodPost, "/v1/admin/tenants") ||
		!eastControl.hasRequest(http.MethodGet, "/v1/admin/tenants", "display_name=agent", "label=environment%3D%3Dproduction") ||
		!westControl.hasRequest(http.MethodGet, "/v1/admin/tenants/tenant-aws-us-west-2") {
		t.Fatalf("direct control-plane requests were incomplete: east=%#v west=%#v", eastControl.requests, westControl.requests)
	}

	requestsBeforeTokenOnlyDelete := westControl.requestCount()
	tokenOnlyDelete := runTIWithInput(t, bin, "", append(baseEnv, "TI_FS_TOKEN="+drive9TestToken("tenant-aws-us-west-2")), "--profile", "stage", "--region", "aws-us-west-2", "fs", "delete-file-system")
	tokenOnlyDelete.wantExitCode(2)
	tokenOnlyDelete.wantStderrContains("FS tokens cannot select or authorize file system deletion")
	if got := westControl.requestCount(); got != requestsBeforeTokenOnlyDelete {
		t.Fatalf("token-only delete sent a remote request: before=%d after=%d", requestsBeforeTokenOnlyDelete, got)
	}

	deleteScratch := runTIWithInput(t, bin, "", append(baseEnv, "TI_FS_TOKEN="+drive9TestToken("tenant-aws-us-west-2")), "--profile", "stage", "--region", "aws-us-west-2", "fs", "delete-file-system", "--file-system-id", "tenant-aws-us-west-2")
	deleteScratch.wantExitCode(0)
	deleteScratch.wantStdoutContains(`"status": "deleting"`)
	afterDelete := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-file-systems")
	afterDelete.wantExitCode(0)
	afterDelete.wantStdoutContains(`"file_system_id": "tenant-aws-us-east-1"`)
	afterDelete.wantStdoutNotContains(`"file_system_id": "tenant-aws-us-west-2"`)
	westAfterDelete := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "--region", "aws-us-west-2", "fs", "list-file-systems")
	westAfterDelete.wantExitCode(0)
	westAfterDelete.wantStdoutNotContains(`"file_system_id": "tenant-aws-us-west-2"`)
	stillMissingAfterDelete := runTIWithInput(t, bin, "", baseEnv, "--profile", "stage", "fs", "list-files", "--path", "/")
	stillMissingAfterDelete.wantExitCode(2)
	stillMissingAfterDelete.wantStderrContains("file system ID is required")
	if !westControl.hasRequest(http.MethodDelete, "/v1/admin/tenants/tenant-aws-us-west-2") {
		t.Fatalf("delete did not use the direct control plane: %#v", westControl.requests)
	}

	for _, args := range [][]string{
		{"--profile", "stage", "fs", "set-default-file-system"},
		{"--profile", "stage", "fs", "unset-default-file-system"},
		{"--profile", "stage", "fs", "create-file-system", "--set-default"},
	} {
		removed := runTIWithInput(t, bin, "", baseEnv, args...)
		removed.wantExitCode(2)
	}
}

func TestFSConfigurationFreeAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	bin := tiBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "ti-drive9")
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
		"TI_LOGGING=on",
		"TI_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
	}
	authEnv := append(append([]string{}, baseEnv...),
		"TDC_FS_TOKEN="+drive9TestToken("tenant-sandbox"),
		"TDC_REGION_CODE=aws-us-east-1",
		"TDC_PUBLIC_KEY=must-not-reach-data-plane",
	)

	remoteList := runTIWithInput(t, bin, "", baseEnv, "fs", "list-file-systems")
	remoteList.wantExitCode(2)
	remoteList.wantStderrContains("profile \"default\" not found")

	for _, args := range [][]string{
		{"fs", "check-file-system"},
		{"fs", "list-files", "--path", "/"},
		{"fs-journal", "create-journal", "--journal-id", "jrn-ephemeral"},
		{"fs-vault", "list-secrets"},
		{"fs-git", "hydrate-git-workspace", "--target-path", filepath.Join(home, "workspace")},
	} {
		result := runTIWithInput(t, bin, "", authEnv, args...)
		result.wantExitCode(0)
	}

	flagsOnly := runTIWithInput(t, bin, "", baseEnv,
		"--region", "aws-us-east-1",
		"fs", "list-files",
		"--fs-token", drive9TestToken("tenant-flag"),
		"--path", "/",
	)
	flagsOnly.wantExitCode(0)

	mixed := runTIWithInput(t, bin, "", append(baseEnv, "TDC_FS_TOKEN="+drive9TestToken("tenant-mixed")),
		"--region", "aws-us-east-1",
		"fs", "list-files",
		"--path", "/",
	)
	mixed.wantExitCode(0)

	mountPath := filepath.Join(home, "mount")
	mount := runTIWithInput(t, bin, "", authEnv,
		"fs", "mount-file-system",
		"--mount-path", mountPath,
	)
	mount.wantExitCode(0)
	drain := runTIWithInput(t, bin, "", baseEnv,
		"fs", "drain-file-system",
		"--mount-path", mountPath,
	)
	drain.wantExitCode(0)
	unmount := runTIWithInput(t, bin, "", baseEnv,
		"fs", "unmount-file-system",
		"--mount-path", mountPath,
	)
	unmount.wantExitCode(0)

	for _, path := range []string{
		filepath.Join(home, ".tdc", "config"),
		filepath.Join(home, ".tdc", "credentials"),
		filepath.Join(home, ".tdc", "fs_resources"),
		filepath.Join(home, ".tdc", "fs_credentials"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("configuration-free command persisted ti configuration at %s: %v", path, err)
		}
	}
	logData, err := os.ReadFile(filepath.Join(home, ".ti", "logs", "ti.jsonl"))
	if err != nil {
		t.Fatalf("read configuration-free operation log: %v", err)
	}
	for _, secret := range []string{drive9TestToken("tenant-sandbox"), drive9TestToken("tenant-flag"), drive9TestToken("tenant-mixed"), "must-not-reach-data-plane"} {
		if strings.Contains(string(logData), secret) {
			t.Fatalf("configuration-free operation log leaked a credential")
		}
	}
	calls := readFakeDrive9Calls(t, recordPath)
	if len(calls) == 0 {
		t.Fatal("configuration-free commands did not invoke companion")
	}
	for _, call := range calls {
		if call.TiDBCloudPublicKey != "" || call.TiDBCloudPrivateKey != "" || call.TIFSToken != "" || call.Drive9Public != "" || call.Drive9Private != "" {
			t.Fatalf("data-plane companion inherited TiDB Cloud or raw ti secrets: %#v", call)
		}
	}
	assertFakeDrive9Call(t, calls, []string{"fs", "ls"}, drive9TestToken("tenant-sandbox"), home, "default", "tenant-sandbox", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"mount", "drain"}, "", home, "default", "tenant-sandbox", "https://fs-east.test", "aws-us-east-1")
	assertFakeDrive9Call(t, calls, []string{"umount"}, "", home, "default", "tenant-sandbox", "https://fs-east.test", "aws-us-east-1")
}

func TestFSLayerForkWorkflowCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	bin := tiBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "ti-drive9")
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
	env := []string{
		"HOME=" + home,
		"TI_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
		"TI_FS_TOKEN=" + drive9TestToken("tenant-layer"),
		"TI_REGION_CODE=aws-us-east-1",
	}

	fork := runTIWithInput(t, bin, "", env, "fs", "fork-layer", "--parent-layer-ref", "research-base", "--layer-id", "child-id", "--layer-name", "child", "--checkpoint-id", "seed", "--actor-id", "agent")
	fork.wantExitCode(0)
	fork.wantStdoutContains(`"parent_layer_id": "parent-id"`)
	fork.wantStdoutNotContains("drive9_")

	chain := runTIWithInput(t, bin, "", env, "fs", "list-layer-chain", "--layer-ref", "child-id", "--output", "text")
	chain.wantExitCode(0)
	chain.wantStdoutContains("LAYER_ID")
	chain.wantStdoutContains("root-id")
	chain.wantStdoutContains("child-id")

	queried := runTIWithInput(t, bin, "", env, "fs", "list-layer-chain", "--layer-ref", "child-id", "--query", "chain[].layer_id")
	queried.wantExitCode(0)
	queried.wantStdoutContains("root-id")

	dryRunDelete := runTIWithInput(t, bin, "", env, "fs", "delete-layer", "--layer-ref", "child-id", "--cascade", "--dry-run")
	dryRunDelete.wantExitCode(0)
	dryRunDelete.wantStdoutContains(`"operation": "delete_layer"`)
	dryRunDelete.wantStdoutContains(`"companion_capability"`)
	dryRunDelete.wantStdoutNotContains(drive9TestToken("tenant-layer"))

	deleted := runTIWithInput(t, bin, "", env, "fs", "delete-layer", "--layer-ref", "child-id", "--cascade", "--output", "text")
	deleted.wantExitCode(0)
	deleted.wantStdoutContains("status=abandoned")

	recursive := runTIWithInput(t, bin, "", env, "fs", "copy-file", "--from-local", ".", "--to-remote", "/workspace", "--recursive", "--layer-id", "child-id")
	recursive.wantExitCode(2)
	recursive.wantStderrContains("--recursive cannot be combined with --layer-id")

	checkpointWithoutLayer := runTIWithInput(t, bin, "", env, "fs", "mount-file-system", "--mount-path", filepath.Join(home, "missing-layer"), "--driver", "fuse", "--checkpoint-id", "seed")
	checkpointWithoutLayer.wantExitCode(2)
	checkpointWithoutLayer.wantStderrContains("--checkpoint-id requires --layer-ref")

	webdavLayer := runTIWithInput(t, bin, "", env, "fs", "mount-file-system", "--mount-path", filepath.Join(home, "webdav"), "--driver", "webdav", "--layer-ref", "child-id")
	webdavLayer.wantExitCode(2)
	webdavLayer.wantStderrContains("layer and checkpoint mounts require FUSE")

	mountPath := filepath.Join(home, "checkpoint")
	mounted := runTIWithInput(t, bin, "", env, "fs", "mount-file-system", "--mount-path", mountPath, "--remote-path", "/workspace", "--driver", "fuse", "--layer-ref", "child-id", "--checkpoint-id", "seed")
	mounted.wantExitCode(0)
	mounted.wantStdoutContains(`"read_only": true`)
	mounted.wantStdoutContains(`"write_back_cache": false`)
	mounted.wantStdoutContains(`"checkpoint_id": "seed"`)

	explicitWritable := runTIWithInput(t, bin, "", env, "fs", "mount-file-system", "--mount-path", filepath.Join(home, "writable-checkpoint"), "--driver", "fuse", "--layer-ref", "child-id", "--checkpoint-id", "seed", "--read-only=false")
	explicitWritable.wantExitCode(2)
	explicitWritable.wantStderrContains("checkpoint mounts are read-only")
	explicitWriteBack := runTIWithInput(t, bin, "", env, "fs", "mount-file-system", "--mount-path", filepath.Join(home, "write-back-checkpoint"), "--driver", "fuse", "--layer-ref", "child-id", "--checkpoint-id", "seed", "--write-back-cache=true")
	explicitWriteBack.wantExitCode(2)
	explicitWriteBack.wantStderrContains("checkpoint mounts are read-only")
	unmounted := runTIWithInput(t, bin, "", env, "fs", "unmount-file-system", "--mount-path", mountPath)
	unmounted.wantExitCode(0)

	calls := readFakeDrive9Calls(t, recordPath)
	assertFakeDrive9Args(t, calls, []string{"fs", "layer", "fork", "--json", "--id", "child-id", "--name", "child", "--checkpoint", "seed", "--actor", "agent", "research-base"})
	assertFakeDrive9Args(t, calls, []string{"fs", "layer", "chain", "--json", "child-id"})
	assertFakeDrive9Args(t, calls, []string{"fs", "layer", "delete", "--cascade", "child-id"})
	assertFakeDrive9Args(t, calls, []string{"mount", "--mode", "fuse", "--read-only", "--layer", "child-id", "--checkpoint", "seed", "--cache-size", "128", "--read-cache-max-file-mb", "4", "--read-cache-ttl", "30s", ":/workspace", mountPath})
}

func TestFSImportFileSystemToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX token-file permission checks are not available on Windows")
	}
	bin := tiBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "tdc-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}
	token := drive9TestToken("tenant-imported")
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":"https://fs.test","cloud_provider":"aws","tidb_region":"us-east-1"}]}`)
	}))
	defer manifestServer.Close()
	recordPath := filepath.Join(t.TempDir(), "calls.jsonl")
	baseEnv := []string{
		"HOME=" + home,
		"TI_DRIVE9_BIN=" + companion,
		"FAKE_DRIVE9_RECORD=" + recordPath,
		"FAKE_DRIVE9_EXPECT_API_KEY=" + token,
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
	}
	tokenPath := filepath.Join(t.TempDir(), "fs-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dryHome := t.TempDir()
	dryRun := runTIWithInput(t, bin, "", append(append([]string{}, baseEnv...), "HOME="+dryHome),
		"--region", "aws-us-east-1", "fs", "import-file-system-token", "--from-file", tokenPath, "--dry-run")
	dryRun.wantExitCode(0)
	if _, err := fscred.GetCredential(dryHome, "default", "tenant-imported"); err == nil {
		t.Fatal("dry-run persisted imported credentials")
	}

	imported := runTIWithInput(t, bin, "", baseEnv,
		"--region", "aws-us-east-1", "fs", "import-file-system-token", "--from-file", tokenPath)
	imported.wantExitCode(0)
	imported.wantStdoutContains(`"file_system_id": "tenant-imported"`)
	imported.wantStdoutNotContains(token)
	credential, err := fscred.GetCredential(home, "default", "tenant-imported")
	if err != nil || credential.APIKey != token || credential.RegionCode != "aws-us-east-1" {
		t.Fatalf("imported credential=%#v err=%v", credential, err)
	}

	useStored := runTIWithInput(t, bin, "", baseEnv,
		"fs", "list-files", "--file-system-id", "tenant-imported", "--path", "/")
	useStored.wantExitCode(0)
	calls := readFakeDrive9Calls(t, recordPath)
	assertFakeDrive9Call(t, calls, []string{"fs", "ls"}, token, home, "default", "tenant-imported", "https://fs.test", "aws-us-east-1")

	multipleSources := runTIWithInput(t, bin, "", append(baseEnv, "TDC_FS_TOKEN="+token),
		"--region", "aws-us-east-1", "fs", "import-file-system-token", "--from-file", tokenPath)
	multipleSources.wantExitCode(2)
	multipleSources.wantStderrContains("provide exactly one")

	insecurePath := filepath.Join(t.TempDir(), "insecure-token")
	if err := os.WriteFile(insecurePath, []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	insecure := runTIWithInput(t, bin, "", baseEnv,
		"--region", "aws-us-east-1", "fs", "import-file-system-token", "--from-file", insecurePath)
	insecure.wantExitCode(2)
	insecure.wantStderrContains("mode 0600 or stricter")

	stdinHome := t.TempDir()
	stdinImport := runTIWithInput(t, bin, token+"\n", append(append([]string{}, baseEnv...), "HOME="+stdinHome),
		"--region", "aws-us-east-1", "fs", "import-file-system-token", "--from-file", "-")
	stdinImport.wantExitCode(0)
	if _, err := fscred.GetCredential(stdinHome, "default", "tenant-imported"); err != nil {
		t.Fatalf("stdin import did not store credentials: %v", err)
	}
}

func TestFSFileSystemTokenLifecycle(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	generatedToken := drive9TestTokenWithVersion("tenant-tokens", 1)
	refreshedToken := drive9TestTokenWithVersion("tenant-tokens", 2)
	scopedToken := drive9TestTokenWithVersion("tenant-tokens", 3)
	remoteRequests := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteRequests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/tokens/refresh" && !(r.Method == http.MethodPost && r.URL.Path == "/v1/tokens") && r.Header.Get("Authorization") == "" {
			if r.Header.Get("X-TiDBCloud-Public-Key") != "e2e-public" || r.Header.Get("X-TiDBCloud-Private-Key") != "e2e-private" || r.Header.Get("Authorization") != "" {
				t.Errorf("control-plane token authentication headers = %#v", r.Header)
			}
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":%q,"token_id":"token-e2e","tenant_id":"tenant-tokens","key_name":"e2e-owner","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-13T00:00:00Z"}`, generatedToken)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens":
			if r.Header.Get("Authorization") != "Bearer "+generatedToken || r.Header.Get("X-TiDBCloud-Public-Key") != "" {
				t.Errorf("scoped issue authentication headers = %#v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"token":%q,"token_id":"token-scoped","subject":"e2e-agent","scope_kind":"fs_scoped","expires_at":"2026-08-13T00:00:00Z","scopes":[{"prefix":"/workspace","ops":["read","list"]}]}`, scopedToken)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tokens":
			if r.URL.Query().Get("tenant_id") != "tenant-tokens" || r.URL.Query().Get("limit") != "50" {
				t.Errorf("list query = %s", r.URL.RawQuery)
			}
			_, _ = fmt.Fprint(w, `{"tokens":[{"token_id":"token-e2e","tenant_id":"tenant-tokens","key_name":"e2e-owner","scope_kind":"owner","status":"active","expired":false,"issued_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-13T00:00:00Z","created_at":"2026-08-12T00:00:00Z","updated_at":"2026-08-12T00:00:00Z"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/token-e2e/deactivate":
			_, _ = fmt.Fprint(w, `{"token_id":"token-e2e","tenant_id":"tenant-tokens","status":"disabled"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/token-e2e/activate":
			_, _ = fmt.Fprint(w, `{"token_id":"token-e2e","tenant_id":"tenant-tokens","status":"active"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/token-scoped/deactivate":
			if r.Header.Get("Authorization") != "Bearer "+generatedToken || r.Header.Get("X-TiDBCloud-Public-Key") != "" {
				t.Errorf("scoped deactivate authentication headers = %#v", r.Header)
			}
			_, _ = fmt.Fprint(w, `{"token_id":"token-scoped","tenant_id":"tenant-tokens","status":"disabled"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/token-scoped/activate":
			if r.Header.Get("Authorization") != "Bearer "+generatedToken || r.Header.Get("X-TiDBCloud-Public-Key") != "" {
				t.Errorf("scoped activate authentication headers = %#v", r.Header)
			}
			_, _ = fmt.Fprint(w, `{"token_id":"token-scoped","tenant_id":"tenant-tokens","status":"active"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/refresh":
			if r.Header.Get("Authorization") != "Bearer "+generatedToken || r.Header.Get("X-TiDBCloud-Public-Key") != "" {
				t.Errorf("refresh authentication headers = %#v", r.Header)
			}
			_, _ = fmt.Fprintf(w, `{"token":%q,"token_id":"token-e2e","tenant_id":"tenant-tokens","scope_kind":"owner","expires_at":"2026-08-14T00:00:00Z"}`, refreshedToken)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tokens/token-e2e":
			_, _ = fmt.Fprint(w, `{"token_id":"token-e2e","tenant_id":"tenant-tokens","status":"revoked"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tokenServer.Close()
	manifestServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":%q,"cloud_provider":"aws","tidb_region":"us-east-1"}]}`, tokenServer.URL)
	}))
	defer manifestServer.Close()
	env := []string{
		"HOME=" + home, "TI_ALLOW_TEST_ENDPOINTS=1", "TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
		"TI_REGION_CODE=aws-us-east-1", "TIDB_CLOUD_PUBLIC_KEY=e2e-public", "TIDB_CLOUD_PRIVATE_KEY=e2e-private",
	}
	configured := runTIWithInput(t, bin, "", env, "configure", "--profile", "stage", "--non-interactive")
	configured.wantExitCode(0)
	env = append(env, "TI_REGION_CODE=")

	missingLifetime := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "generate-file-system-token", "--file-system-id", "tenant-tokens", "--token-name", "e2e-owner")
	missingLifetime.wantExitCode(2)
	missingLifetime.wantStderrContains("exactly one of --ttl or --no-expiration")

	beforeDryRun := remoteRequests
	dryRun := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "generate-file-system-token", "--file-system-id", "tenant-tokens", "--token-name", "e2e-owner", "--ttl", "24h", "--dry-run")
	dryRun.wantExitCode(0)
	dryRun.wantStdoutNotContains("drive9_")
	if remoteRequests != beforeDryRun {
		t.Fatalf("dry-run sent a remote request: before=%d after=%d", beforeDryRun, remoteRequests)
	}

	generated := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "generate-file-system-token", "--file-system-id", "tenant-tokens", "--token-name", "e2e-owner", "--ttl", "24h", "--store-locally", "--query", "fs_token", "--output", "text")
	generated.wantExitCode(0)
	if strings.TrimSpace(generated.stdout) != generatedToken {
		generated.fail("generate query did not return the one-time token")
	}
	credential, err := fscred.GetCredential(home, "stage", "tenant-tokens")
	if err != nil || credential.TokenID != "token-e2e" || credential.APIKey != generatedToken {
		t.Fatalf("generated credential = %#v, %v", credential, err)
	}

	scoped := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "generate-file-system-scoped-token", "--file-system-id", "tenant-tokens", "--fs-token", generatedToken, "--subject", "e2e-agent", "--ttl", "1h", "--allow", "/workspace:read,list")
	scoped.wantExitCode(0)
	scoped.wantStdoutContains(`"scope_kind": "fs_scoped"`)
	scoped.wantStdoutContains(`"prefix": "/workspace"`)
	scoped.wantStdoutContains(scopedToken)
	scopedText := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "generate-file-system-scoped-token", "--file-system-id", "tenant-tokens", "--fs-token", generatedToken, "--subject", "e2e-agent", "--ttl", "1h", "--allow", "/workspace:read,list", "--output", "text")
	scopedText.wantExitCode(0)
	scopedText.wantStdoutContains("File system ID: tenant-tokens")
	scopedText.wantStdoutContains("PREFIX")
	scopedText.wantStdoutNotContains(`"scope_kind"`)

	bearerListEnv := append(append([]string{}, env...), "TI_FS_TOKEN="+generatedToken)
	bearerListed := runTIWithInput(t, bin, "", bearerListEnv, "--profile", "stage", "fs", "list-file-system-tokens")
	bearerListed.wantExitCode(0)
	configFreeBearerEnv := []string{
		"HOME=" + t.TempDir(), "TI_ALLOW_TEST_ENDPOINTS=1", "TI_TEST_FS_MANIFEST_URL=" + manifestServer.URL,
		"TI_REGION_CODE=aws-us-east-1", "TI_FS_TOKEN=" + generatedToken,
	}
	configFreeBearerList := runTIWithInput(t, bin, "", configFreeBearerEnv, "fs", "list-file-system-tokens")
	configFreeBearerList.wantExitCode(0)
	bearerDisabled := runTIWithInput(t, bin, "", bearerListEnv, "--profile", "stage", "fs", "disable-file-system-token", "--token-id", "token-scoped")
	bearerDisabled.wantExitCode(0)
	bearerDisabled.wantStdoutContains(`"status": "disabled"`)
	bearerEnabled := runTIWithInput(t, bin, "", bearerListEnv, "--profile", "stage", "fs", "enable-file-system-token", "--token-id", "token-scoped")
	bearerEnabled.wantExitCode(0)
	bearerEnabled.wantStdoutContains(`"status": "active"`)

	listed := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "list-file-system-tokens", "--file-system-id", "tenant-tokens", "--output", "text")
	listed.wantExitCode(0)
	listed.wantStdoutContains("token-e2e")
	listed.wantStdoutContains("e2e-owner")
	listed.wantStdoutNotContains("drive9_")

	disabled := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "disable-file-system-token", "--file-system-id", "tenant-tokens", "--token-id", "token-e2e")
	disabled.wantExitCode(0)
	disabled.wantStdoutContains(`"status": "disabled"`)
	enabled := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "enable-file-system-token", "--file-system-id", "tenant-tokens", "--token-id", "token-e2e")
	enabled.wantExitCode(0)
	enabled.wantStdoutContains(`"status": "active"`)
	enabledText := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "enable-file-system-token", "--file-system-id", "tenant-tokens", "--token-id", "token-e2e", "--output", "text")
	enabledText.wantExitCode(0)
	enabledText.wantStdoutContains("Status: active")
	enabledText.wantStdoutNotContains(`"status"`)

	refreshed := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "refresh-file-system-token", "--file-system-id", "tenant-tokens", "--query", "fs_token", "--output", "text")
	refreshed.wantExitCode(0)
	if strings.TrimSpace(refreshed.stdout) != refreshedToken {
		refreshed.fail("refresh query did not return the rotated token")
	}
	credential, err = fscred.GetCredential(home, "stage", "tenant-tokens")
	if err != nil || credential.APIKey != refreshedToken {
		t.Fatalf("refreshed credential = %#v, %v", credential, err)
	}

	deleted := runTIWithInput(t, bin, "", env, "--profile", "stage", "fs", "delete-file-system-token", "--file-system-id", "tenant-tokens", "--token-id", "token-e2e")
	deleted.wantExitCode(0)
	deleted.wantStdoutContains(`"local_credentials_updated": true`)
	if _, err := fscred.GetCredential(home, "stage", "tenant-tokens"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("deleted token left local credentials: %v", err)
	}
	logData, err := os.ReadFile(filepath.Join(home, ".ti", "logs", "ti.jsonl"))
	if err == nil && (strings.Contains(string(logData), generatedToken) || strings.Contains(string(logData), refreshedToken)) {
		t.Fatal("operation log leaked a file system token")
	}
}

func drive9TestToken(fileSystemID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"tenant_id": fileSystemID})
	jwt := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
}

func drive9TestTokenWithVersion(fileSystemID string, version int) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"tenant_id": fileSystemID, "token_version": version})
	jwt := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
}

type fakeFSTenant struct {
	TenantID    string            `json:"tenant_id"`
	DisplayName string            `json:"display_name"`
	Labels      map[string]string `json:"label"`
	Status      string            `json:"status"`
	Kind        string            `json:"kind"`
	Quota       map[string]any    `json:"quota"`
}

type fakeFSTenantRequest struct {
	Method string
	Path   string
	Query  string
}

type fakeFSTenantControlPlane struct {
	t          *testing.T
	regionCode string
	tenantID   string
	server     *httptest.Server
	mu         sync.Mutex
	tenants    map[string]fakeFSTenant
	requests   []fakeFSTenantRequest
}

func newFakeFSTenantControlPlane(t *testing.T, regionCode, tenantID string) *fakeFSTenantControlPlane {
	t.Helper()
	fake := &fakeFSTenantControlPlane{t: t, regionCode: regionCode, tenantID: tenantID, tenants: map[string]fakeFSTenant{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeFSTenantControlPlane) close() {
	f.server.Close()
}

func (f *fakeFSTenantControlPlane) URL() string {
	return f.server.URL
}

func (f *fakeFSTenantControlPlane) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeFSTenantRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
	if r.Header.Get("X-TiDBCloud-Public-Key") != "e2e-public" || r.Header.Get("X-TiDBCloud-Private-Key") != "e2e-private" || r.Header.Get("Authorization") != "" {
		http.Error(w, `{"error":"missing TiDB Cloud credentials"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/tenants":
		var body struct {
			DisplayName string            `json:"display_name"`
			Labels      map[string]string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		displayName := body.DisplayName
		if displayName == "" {
			displayName = f.tenantID
		}
		labels := map[string]string{}
		for key, value := range body.Labels {
			labels[key] = value
		}
		tenant := fakeFSTenant{
			TenantID: f.tenantID, DisplayName: displayName, Labels: labels, Status: "active", Kind: "live", Quota: fakeFSTenantQuota(),
		}
		f.tenants[tenant.TenantID] = tenant
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant_id": tenant.TenantID, "display_name": tenant.DisplayName, "label": tenant.Labels, "api_key": drive9TestToken(tenant.TenantID),
			"status": "provisioning", "cloud_provider": "aws", "region": strings.TrimPrefix(f.regionCode, "aws-"),
		})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants":
		items := make([]fakeFSTenant, 0, len(f.tenants))
		displayFilter := r.URL.Query().Get("display_name")
		labelFilter := r.URL.Query().Get("label")
		labelKey, labelValue, hasLabelFilter := strings.Cut(labelFilter, "==")
		for _, tenant := range f.tenants {
			if displayFilter != "" && !strings.Contains(tenant.DisplayName, displayFilter) {
				continue
			}
			if hasLabelFilter && tenant.Labels[labelKey] != labelValue {
				continue
			}
			items = append(items, tenant)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].TenantID < items[j].TenantID })
		_ = json.NewEncoder(w).Encode(map[string]any{"tenants": items, "page": 1, "page_size": 100, "next_page": 0})
	case strings.HasPrefix(r.URL.Path, "/v1/admin/tenants/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/admin/tenants/")
		tenant, ok := f.tenants[id]
		if !ok {
			http.Error(w, `{"error":"tenant not found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(tenant)
		case http.MethodDelete:
			delete(f.tenants, id)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"tenant_id": id, "status": "deleting"})
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeFSTenantControlPlane) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeFSTenantControlPlane) hasRequest(method, path string, queryParts ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if request.Method != method || request.Path != path {
			continue
		}
		matched := true
		for _, part := range queryParts {
			if !strings.Contains(request.Query, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func fakeFSTenantQuota() map[string]any {
	return map[string]any{
		"config": map[string]any{"max_storage_size": 1024, "max_file_size": 128, "max_file_count": 1000, "tidbcloud_spending_limit": nil},
		"usage":  map[string]any{"storage_bytes": 0, "reserved_bytes": 0, "file_count": 0},
	}
}

type fakeDrive9Call struct {
	Args                []string `json:"args"`
	Home                string   `json:"home"`
	APIKey              string   `json:"api_key"`
	Server              string   `json:"server"`
	RegionCode          string   `json:"region_code"`
	TiDBCloudPublicKey  string   `json:"tidb_cloud_public_key,omitempty"`
	TiDBCloudPrivateKey string   `json:"tidb_cloud_private_key,omitempty"`
	TIFSToken           string   `json:"ti_fs_token,omitempty"`
	Drive9Public        string   `json:"drive9_public_key,omitempty"`
	Drive9Private       string   `json:"drive9_private_key,omitempty"`
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

func assertFakeDrive9Args(t *testing.T, calls []fakeDrive9Call, want []string) {
	t.Helper()
	for _, call := range calls {
		if len(call.Args) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if call.Args[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Fatalf("missing exact fake Drive9 args %v in %#v", want, calls)
}

func assertFakeDrive9TransientCall(t *testing.T, calls []fakeDrive9Call, prefix []string, apiKey, persistentHome, server, regionCode string) {
	t.Helper()
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
		if call.APIKey != apiKey || call.Server != server || call.RegionCode != regionCode {
			t.Fatalf("unexpected fake Drive9 environment for %v: %#v", prefix, call)
		}
		if call.Home == "" || strings.HasPrefix(call.Home, filepath.Join(persistentHome, ".tdc")) {
			t.Fatalf("fake Drive9 call %v used persistent HOME %q", prefix, call.Home)
		}
		if _, err := os.Stat(call.Home); !os.IsNotExist(err) {
			t.Fatalf("transient Drive9 HOME still exists: %q, err=%v", call.Home, err)
		}
		return
	}
	t.Fatalf("missing fake Drive9 call with prefix %v in %#v", prefix, calls)
}

func tiBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TI_E2E_BIN")
	if bin == "" {
		t.Skip("TI_E2E_BIN is not set; run make e2e")
	}
	return bin
}

func TestOperationLogWritesSafeJSONL(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()

	env := []string{
		"HOME=" + home,
		"TI_LOGGING=on",
		"TI_REGION_CODE=aws-us-east-1",
		"TIDB_CLOUD_PUBLIC_KEY=ci-public-secret",
		"TIDB_CLOUD_PRIVATE_KEY=ci-private-secret",
	}
	result := runTIWithInput(t, bin, "", env, "configure", "--profile", "ci", "--non-interactive")
	result.wantExitCode(0)

	logPath := filepath.Join(home, ".ti", "logs", "ti.jsonl")
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
	if event["type"] != "command" || event["command"] != "ti configure" || event["profile"] != "ci" {
		t.Fatalf("unexpected operation log event: %#v", event)
	}
}

func TestOperationLogCanBeDisabled(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()

	result := runTIWithInput(t, bin, "", []string{
		"HOME=" + home,
		"TI_LOGGING=off",
	}, "help")
	result.wantExitCode(0)
	if _, err := os.Stat(filepath.Join(home, ".ti", "logs", "ti.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no operation log file, got err=%v", err)
	}
}

func TestGlobalSettingsControlOperationLogging(t *testing.T) {
	bin := tiBinary(t)

	t.Run("enabled", func(t *testing.T) {
		home := t.TempDir()
		writeE2EFile(t, filepath.Join(home, ".ti", ".preferences"), "schema_version = 1\n\n[logging]\nenabled = true\nmax_file_mb = 1\nmax_files = 2\n", 0o600)
		for _, profileName := range []string{"default", "stage"} {
			result := runTIUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "--profile", profileName, "help")
			result.wantExitCode(0)
		}
		data, err := os.ReadFile(filepath.Join(home, ".ti", "logs", "ti.jsonl"))
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
		writeE2EFile(t, filepath.Join(home, ".ti", ".preferences"), "[logging]\nenabled = false\n", 0o600)
		result := runTIUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "help")
		result.wantExitCode(0)
		if _, err := os.Stat(filepath.Join(home, ".ti", "logs", "ti.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("disabled settings created operation log: %v", err)
		}
	})
}

func TestLegacyLoggingConfigMigratesThroughBinary(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	configPath := filepath.Join(home, ".ti", "config")
	writeE2EFile(t, configPath, `[default]
region_code = "aws-us-east-1"
project_id = "virtual-e2e"

[logging]
enabled = false
max_file_mb = 3
max_files = 2
`, 0o644)
	credentialsPath := filepath.Join(home, ".ti", "credentials")
	credentials := "[default]\ntidb_cloud_public_key = \"public\"\ntidb_cloud_private_key = \"private\"\n"
	writeE2EFile(t, credentialsPath, credentials, 0o600)

	result := runTIUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "help")
	result.wantExitCode(0)

	settingsPath := filepath.Join(home, ".ti", ".preferences")
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
	bin := tiBinary(t)
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".ti", ".preferences")
	before := "[logging]\nenabled = \"not-a-boolean\"\n"
	writeE2EFile(t, settingsPath, before, 0o644)

	result := runTIUsingLoggingSettings(t, bin, []string{"HOME=" + home}, "--debug", "help")
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
	if _, err := os.Stat(filepath.Join(home, ".ti", "logs", "ti.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("malformed settings did not fail closed: %v", err)
	}
}

func TestUpdateAndReservedProfileGlobalSettingsBoundaries(t *testing.T) {
	bin := tiBinary(t)

	t.Run("update does not access ti home", func(t *testing.T) {
		home := t.TempDir()
		settingsPath := filepath.Join(home, ".ti", ".preferences")
		before := "not valid toml = ["
		writeE2EFile(t, settingsPath, before, 0o600)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			extension := "tar.gz"
			if runtime.GOOS == "windows" {
				extension = "zip"
			}
			assetName := fmt.Sprintf("ti_%s_%s.%s", runtime.GOOS, runtime.GOARCH, extension)
			_, _ = fmt.Fprintf(w, `{"tag_name":"v0.1.5","html_url":"https://example.test/v0.1.5","assets":[{"name":%q,"browser_download_url":"https://example.test/%s"}]}`, assetName, assetName)
		}))
		defer server.Close()
		result := runTIUsingLoggingSettings(t, bin, []string{
			"HOME=" + home,
			"TI_LOGGING=on",
			"TI_RELEASE_API_BASE_URL=" + server.URL,
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
		if _, err := os.Stat(filepath.Join(home, ".ti", "logs", "ti.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("update wrote operation log: %v", err)
		}
	})

	t.Run("logging profile is reserved", func(t *testing.T) {
		home := t.TempDir()
		result := runTIWithInput(t, bin, "", []string{
			"HOME=" + home,
			"TI_LOGGING=off",
		}, "configure", "--profile", "logging", "--region-code", "aws-us-east-1", "--tidb-cloud-public-key", "public", "--tidb-cloud-private-key", "private", "--non-interactive")
		result.wantExitCode(2)
		result.wantStderrContains(`profile name "logging" is reserved`)
		for _, name := range []string{"config", "credentials", ".preferences"} {
			if _, err := os.Stat(filepath.Join(home, ".ti", name)); !os.IsNotExist(err) {
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

func runTI(t *testing.T, bin string, args ...string) commandResult {
	t.Helper()
	return runTIWithInput(t, bin, "", nil, args...)
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

func runTIWithInput(t *testing.T, bin, stdin string, env []string, args ...string) commandResult {
	return runTIProcess(t, bin, stdin, env, true, args...)
}

func runTIUsingLoggingSettings(t *testing.T, bin string, env []string, args ...string) commandResult {
	t.Helper()
	return runTIProcess(t, bin, "", env, false, args...)
}

func runTIProcess(t *testing.T, bin, stdin string, env []string, disableLoggingByDefault bool, args ...string) commandResult {
	t.Helper()
	if os.Getenv("TI_LIVE") == "1" && hasLiveFSCommandFamily(args) && !envContains(env, "TI_FS_FILE_SYSTEM_ID") && !envContains(env, "TI_FS_TOKEN") && !envContains(env, "TDC_FS_TOKEN") {
		fileSystemID := strings.TrimSpace(os.Getenv("TI_LIVE_FS_ID"))
		if fileSystemID == "" {
			fileSystemID = liveFSSelectedID
		}
		if fileSystemID != "" {
			env = append(env, "TI_FS_FILE_SYSTEM_ID="+fileSystemID)
		}
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
	if disableLoggingByDefault && !envContains(env, "TI_LOGGING") {
		cmd.Env = append(cmd.Env, "TI_LOGGING=off")
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
		"command: ti " + strings.Join(r.args, " "),
		message,
		"stdout:\n" + r.stdout,
		"stderr:\n" + r.stderr,
	}, "\n"))
}
