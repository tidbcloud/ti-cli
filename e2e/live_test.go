package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

const defaultLiveProfile = "live-e2e"

var (
	liveFSResourceMu            sync.Mutex
	liveFSResourceAutoCreatedID string
	liveFSTokenAutoCreatedID    string
	liveFSTokenAutoFileSystemID string
	liveFSSelectedID            string
	liveProfileConfigureMu      sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if liveFSResourceAutoCreatedID != "" || liveFSTokenAutoCreatedID != "" {
		cleanupAutoCreatedLiveFSResource()
	}
	os.Exit(code)
}

func TestLiveProfileConfigured(t *testing.T) {
	requireLive(t)

	bin := tiBinary(t)
	version := runTI(t, bin, "--version")
	version.wantExitCode(0)
	version.wantStdoutContains("ti ")

	profile := liveProfile(t)
	if profile.CloudProvider == "" || profile.RegionCode == "" {
		t.Fatalf("live e2e profile %q is incomplete", profile.Name)
	}
	configured := runTIWithInput(t, bin, "", []string{
		"TI_REGION_CODE=" + profile.PlacementRegionCode,
		"TIDB_CLOUD_PUBLIC_KEY=" + profile.TiDBCloudPublicKey,
		"TIDB_CLOUD_PRIVATE_KEY=" + profile.TiDBCloudPrivateKey,
	}, "configure", "--profile", profile.Name, "--non-interactive")
	configured.wantExitCode(0)
	configured.wantStdoutContains(`"region_code": "` + profile.PlacementRegionCode + `"`)
	configured.wantStdoutNotContains("project")
}

func TestLiveDBAPIReadOnlyProbes(t *testing.T) {
	requireLive(t)

	profile := liveProfile(t)
	resolver := endpoints.NewResolver()

	starterEndpoint, err := resolver.ResolveStarter(profile.CloudProvider, profile.RegionCode)
	if err != nil {
		t.Fatalf("resolve Starter endpoint: %v", err)
	}
	starter := liveDigestClient(t, profile, starterEndpoint, authz.StarterClusterRead)
	liveGETJSON(t, starter, "/v1beta1/regions")
	liveGETJSON(t, starter, "/v1beta1/regions:listCloudProviders")
}

func TestLiveFSRemoteInventoryLifecycle(t *testing.T) {
	requireLive(t)

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	preflightList := runTI(t, bin, "--profile", profileName, "fs", "list-file-systems")
	preflightList.wantExitCode(0)
	create := runTI(t, bin, "--profile", profileName, "fs", "create-file-system", "--wait")
	if create.exitCode != 0 && isLiveFSQuotaError(create.stderr) {
		t.Skipf("tdc fs live inventory lifecycle requires one free Starter slot: %s", strings.TrimSpace(create.stderr))
	}
	create.wantExitCode(0)
	var created struct {
		FileSystemID string `json:"file_system_id"`
		FSToken      string `json:"fs_token"`
	}
	if err := json.Unmarshal([]byte(create.stdout), &created); err != nil || created.FileSystemID == "" || created.FSToken == "" {
		t.Fatalf("decode live tdc fs create result: %v", err)
	}
	liveFSResourceMu.Lock()
	liveFSResourceAutoCreatedID = created.FileSystemID
	liveFSSelectedID = created.FileSystemID
	liveFSResourceMu.Unlock()

	list := runTI(t, bin, "--profile", profileName, "fs", "list-file-systems")
	list.wantExitCode(0)
	list.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
	list.wantStdoutContains(`"has_local_token": true`)
	list.wantStdoutNotContains(created.FSToken)

	missingSelector := runTIWithInput(t, bin, "", []string{"TI_FS_FILE_SYSTEM_ID="}, "--profile", profileName, "fs", "check-file-system")
	missingSelector.wantExitCode(2)
	missingSelector.wantStderrContains("file system ID is required")
	environmentCheck := runTIWithInput(t, bin, "", []string{"TI_FS_FILE_SYSTEM_ID=" + created.FileSystemID}, "--profile", profileName, "fs", "check-file-system")
	environmentCheck.wantExitCode(0)
	environmentCheck.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
	explicitCheck := runTI(t, bin, "--profile", profileName, "fs", "check-file-system", "--file-system-id", created.FileSystemID)
	explicitCheck.wantExitCode(0)
	explicitCheck.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
	describe := runTI(t, bin, "--profile", profileName, "fs", "describe-file-system", "--file-system-id", created.FileSystemID)
	describe.wantExitCode(0)
	describe.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
}

func TestLiveCLICommandSurface(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	testLiveHelpCommands(t, bin, [][]string{{"help"}, {"update", "help"}})

	checkUpdateHelp := runTI(t, bin, "update", "help")
	checkUpdateHelp.wantExitCode(0)
	checkUpdateHelp.wantStdoutContains("--check")
	checkUpdateHelp.wantStdoutContains("--fail-if-update-available")
	checkUpdateHelp.wantStdoutNotContains("--yes")
}

func TestLiveDBCommandSurface(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	profile := liveProfile(t)
	expectedRegionName := "regions/" + endpoints.APIProvider(profile.CloudProvider) + "-" + profile.RegionCode
	testLiveHelpCommands(t, bin, [][]string{
		{"db", "help"},
		{"db", "create-db-cluster", "help"},
		{"db", "list-db-clusters", "help"},
	})
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", "tdc-e2e-dry-run", "--wait"},
	}, "remote_mutation")
	testLiveReadOnlyDryRunRejections(t, bin, profileName, [][]string{
		{"db", "list-db-clusters", "--db-cluster-type", "starter"},
		{"db", "describe-db-cluster"},
		{"db", "list-db-cluster-branches"},
		{"db", "describe-db-cluster-branch"},
		{"db", "format-db-connection-string"},
		{"db", "execute-sql-statement"},
	})

	clusters := runTI(t, bin, "--profile", profileName, "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1")
	clusters.wantExitCode(0)
	clusters.wantStdoutContains(`"clusters"`)
	var clusterList struct {
		Clusters []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Region      struct {
				Name string `json:"name"`
			} `json:"region"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(clusters.stdout), &clusterList); err != nil {
		t.Fatalf("decode db list-db-clusters output: %v\n%s", err, clusters.stdout)
	}
	for _, cluster := range clusterList.Clusters {
		if cluster.Region.Name != expectedRegionName {
			t.Fatalf("listed cluster %q has region %q, want %q", cluster.ID, cluster.Region.Name, expectedRegionName)
		}
	}

	clusterQuery := runTI(t, bin, "--profile", profileName, "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1", "--query", "clusters[].id")
	clusterQuery.wantExitCode(0)

	clusterText := runTI(t, bin, "--profile", profileName, "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1", "--output", "text")
	clusterText.wantExitCode(0)
	clusterText.wantStdoutContains("ID")

	if len(clusterList.Clusters) > 0 && clusterList.Clusters[0].ID != "" {
		describe := runTI(t, bin, "--profile", profileName, "db", "describe-db-cluster", "--db-cluster-id", clusterList.Clusters[0].ID)
		describe.wantExitCode(0)
		describe.wantStdoutContains(`"id"`)
		describe.wantStdoutContains(clusterList.Clusters[0].ID)
	}
}

func TestLiveFSCommandSurface(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	testLiveHelpCommands(t, bin, [][]string{
		{"fs", "help"},
		{"fs", "create-file-system", "help"}, {"fs", "import-file-system-token", "help"}, {"fs", "list-file-systems", "help"},
		{"fs", "describe-file-system", "help"}, {"fs", "copy-file", "help"},
		{"fs", "read-file", "help"}, {"fs", "chmod-file", "help"},
		{"fs", "create-symlink", "help"}, {"fs", "create-hardlink", "help"},
		{"fs", "create-layer", "help"}, {"fs", "list-layers", "help"},
		{"fs", "describe-layer", "help"}, {"fs", "diff-layer", "help"},
		{"fs", "create-layer-checkpoint", "help"}, {"fs", "rollback-layer", "help"},
		{"fs", "commit-layer", "help"}, {"fs", "pack-file-system", "help"},
		{"fs", "unpack-file-system", "help"}, {"fs", "drain-file-system", "help"},
		{"fs", "cp", "help"}, {"fs", "cat", "help"}, {"fs", "ls", "help"},
		{"fs", "stat", "help"}, {"fs", "mv", "help"}, {"fs", "rm", "help"},
		{"fs", "mkdir", "help"}, {"fs", "chmod", "help"}, {"fs", "symlink", "help"},
		{"fs", "hardlink", "help"}, {"fs", "grep", "help"}, {"fs", "find", "help"},
		{"fs", "generate-file-system-token", "help"}, {"fs", "list-file-system-tokens", "help"},
		{"fs", "enable-file-system-token", "help"}, {"fs", "disable-file-system-token", "help"},
		{"fs", "delete-file-system-token", "help"}, {"fs", "refresh-file-system-token", "help"},
		{"fs", "mount", "help"}, {"fs", "drain", "help"}, {"fs", "umount", "help"},
	})
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"fs", "create-file-system", "--wait"},
		{"fs", "delete-file-system", "--file-system-id", selected.FSTenantID},
		{"fs", "create-layer", "--layer-id", "layer-1", "--base-root-path", "/workspace", "--layer-name", "dev"},
		{"fs", "create-layer-checkpoint", "--layer-id", "layer-1", "--checkpoint-id", "cp-1"},
		{"fs", "rollback-layer", "--layer-id", "layer-1"}, {"fs", "commit-layer", "--layer-id", "layer-1"},
		{"fs", "pack-file-system", "--local-root", "/tmp/ti-e2e-pack", "--remote-root", "/workspace", "--mount-profile", "portable"},
		{"fs", "unpack-file-system", "--local-root", "/tmp/ti-e2e-pack", "--remote-root", "/workspace", "--mount-profile", "portable"},
		{"fs", "mount-file-system", "--mount-path", "/tmp/ti-e2e-mount", "--driver", "webdav"},
		{"fs", "generate-file-system-token", "--file-system-id", selected.FSTenantID, "--token-name", "ti-e2e-dry-run", "--ttl", "1h"},
		{"fs", "enable-file-system-token", "--file-system-id", selected.FSTenantID, "--token-id", "00000000-0000-0000-0000-000000000000"},
		{"fs", "disable-file-system-token", "--file-system-id", selected.FSTenantID, "--token-id", "00000000-0000-0000-0000-000000000000"},
		{"fs", "delete-file-system-token", "--file-system-id", selected.FSTenantID, "--token-id", "00000000-0000-0000-0000-000000000000"},
	}, "remote_mutation")
	refreshDryRun := runTIWithInput(t, bin, "", []string{"TI_FS_TOKEN=" + drive9TestTokenWithVersion(selected.FSTenantID, 999), "TI_REGION_CODE=" + selected.FSPlacementRegionCode},
		"--profile", profileName, "fs", "refresh-file-system-token", "--file-system-id", selected.FSTenantID, "--dry-run")
	refreshDryRun.wantExitCode(0)
	unmountDryRun := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", "/tmp/ti-e2e-mount", "--ignore-absent", "--dry-run", "--query", "checks[].name")
	unmountDryRun.wantExitCode(0)
	for _, check := range []string{"input_validation", "mount_locator", "remote_mutation"} {
		unmountDryRun.wantStdoutContains(check)
	}
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"fs", "copy-file", "--from-remote", "/workspace/source.txt", "--to-remote", "/workspace/target.txt"},
		{"fs", "move-file", "--from-remote", "/workspace/source.txt", "--to-remote", "/workspace/target.txt"},
		{"fs", "delete-file", "--path", "/workspace/source.txt"},
		{"fs", "create-directory", "--path", "/workspace/newdir"},
		{"fs", "chmod-file", "--path", "/workspace/source.txt", "--mode", "0600"},
		{"fs", "create-symlink", "--target", "source.txt", "--link-path", "/workspace/link.txt"},
		{"fs", "create-hardlink", "--source-path", "/workspace/source.txt", "--link-path", "/workspace/hard.txt"},
	}, "request_construction")
	for _, args := range [][]string{
		{"fs", "set-default-file-system"},
		{"fs", "unset-default-file-system"},
		{"fs", "create-file-system", "--set-default"},
	} {
		result := runTI(t, bin, append([]string{"--profile", profileName}, args...)...)
		result.wantExitCode(2)
	}
	testLiveReadOnlyDryRunRejections(t, bin, profileName, [][]string{
		{"fs", "check-file-system"}, {"fs", "list-file-systems"}, {"fs", "describe-file-system"},
		{"fs", "read-file"}, {"fs", "list-files"}, {"fs", "describe-file"},
		{"fs", "search-file-content"}, {"fs", "find-files"}, {"fs", "list-layers"},
		{"fs", "describe-layer"}, {"fs", "diff-layer"},
	})
}

func TestLiveFSFileSystemTokenLifecycle(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	regionCode := selected.FSPlacementRegionCode
	if regionCode == "" {
		regionCode = selected.PlacementRegionCode
	}
	tokenName := fmt.Sprintf("ti-e2e-token-%d", time.Now().UnixNano())
	generated := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "generate-file-system-token",
		"--file-system-id", selected.FSTenantID, "--token-name", tokenName, "--ttl", "1h")
	generated.wantExitCode(0)
	var generatedResult struct {
		FileSystemID string `json:"file_system_id"`
		TokenID      string `json:"token_id"`
		FSToken      string `json:"fs_token"`
	}
	if err := json.Unmarshal([]byte(generated.stdout), &generatedResult); err != nil || generatedResult.TokenID == "" || generatedResult.FSToken == "" {
		t.Fatalf("decode generated FS token: %v\n%s", err, generated.stdout)
	}
	if generatedResult.FileSystemID != selected.FSTenantID {
		t.Fatalf("generated token file_system_id = %q, want %q", generatedResult.FileSystemID, selected.FSTenantID)
	}
	deleted := false
	defer func() {
		if deleted {
			return
		}
		cleanup := runLiveFSSetupCommand(t, bin, "--profile", profileName, "--region", regionCode, "fs", "delete-file-system-token",
			"--file-system-id", selected.FSTenantID, "--token-id", generatedResult.TokenID)
		if cleanup.exitCode != 0 && !strings.Contains(strings.ToLower(cleanup.stderr), "not found") {
			t.Logf("cleanup generated FS token failed: %s", strings.TrimSpace(cleanup.stderr))
		}
	}()

	list := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "list-file-system-tokens", "--file-system-id", selected.FSTenantID)
	list.wantExitCode(0)
	list.wantStdoutContains(generatedResult.TokenID)
	list.wantStdoutContains(tokenName)
	list.wantStdoutNotContains(generatedResult.FSToken)

	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, generatedResult.FSToken, true, 30*time.Second)
	disable := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "disable-file-system-token",
		"--file-system-id", selected.FSTenantID, "--token-id", generatedResult.TokenID)
	disable.wantExitCode(0)
	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, generatedResult.FSToken, false, 30*time.Second)

	enable := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "enable-file-system-token",
		"--file-system-id", selected.FSTenantID, "--token-id", generatedResult.TokenID)
	enable.wantExitCode(0)
	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, generatedResult.FSToken, true, 30*time.Second)

	refreshEnv := []string{"TI_FS_TOKEN=" + generatedResult.FSToken, "TI_REGION_CODE=" + regionCode}
	refresh := runTIWithInput(t, bin, "", refreshEnv, "--profile", profileName, "fs", "refresh-file-system-token", "--file-system-id", selected.FSTenantID)
	refresh.wantExitCode(0)
	var refreshResult struct {
		TokenID string `json:"token_id"`
		FSToken string `json:"fs_token"`
	}
	if err := json.Unmarshal([]byte(refresh.stdout), &refreshResult); err != nil || refreshResult.FSToken == "" {
		t.Fatalf("decode refreshed FS token: %v\n%s", err, refresh.stdout)
	}
	if refreshResult.TokenID != generatedResult.TokenID {
		t.Fatalf("refresh changed token ID: %q -> %q", generatedResult.TokenID, refreshResult.TokenID)
	}
	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, generatedResult.FSToken, false, 30*time.Second)
	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, refreshResult.FSToken, true, 30*time.Second)

	remove := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "delete-file-system-token",
		"--file-system-id", selected.FSTenantID, "--token-id", generatedResult.TokenID)
	remove.wantExitCode(0)
	deleted = true
	waitLiveFSTokenAccess(t, bin, profileName, regionCode, selected.FSTenantID, refreshResult.FSToken, false, 30*time.Second)
	listAfter := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "list-file-system-tokens", "--file-system-id", selected.FSTenantID)
	listAfter.wantExitCode(0)
	listAfter.wantStdoutNotContains(generatedResult.TokenID)
}

func waitLiveFSTokenAccess(t *testing.T, bin, profileName, regionCode, fileSystemID, token string, wantAccess bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last commandResult
	for {
		last = runTIWithInput(t, bin, "", []string{"TI_FS_TOKEN=" + token, "TI_REGION_CODE=" + regionCode},
			"--profile", profileName, "fs", "list-files", "--file-system-id", fileSystemID, "--path", "/")
		hasAccess := last.exitCode == 0
		if hasAccess == wantAccess {
			message := strings.ToLower(last.stderr)
			if !wantAccess && !strings.Contains(message, "authentication") && !strings.Contains(message, "invalid api key") {
				last.fail("token access failed for a reason other than authentication")
			}
			return
		}
		if time.Now().After(deadline) {
			last.fail("token access did not converge to %v within %s", wantAccess, timeout)
		}
		time.Sleep(time.Second)
	}
}

func TestLiveFSVaultCommandSurface(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	ensureLiveFSResource(t, bin, profileName)
	testLiveHelpCommands(t, bin, [][]string{
		{"fs-vault", "help"}, {"fs-vault", "create-secret", "help"},
		{"fs-vault", "replace-secret", "help"}, {"fs-vault", "read-secret", "help"},
		{"fs-vault", "list-secrets", "help"}, {"fs-vault", "delete-secret", "help"},
		{"fs-vault", "create-grant", "help"}, {"fs-vault", "delete-grant", "help"},
		{"fs-vault", "list-audit-events", "help"}, {"fs-vault", "run-with-secret", "help"},
		{"fs-vault", "mount-vault", "help"}, {"fs-vault", "unmount-vault", "help"},
	})
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"fs-vault", "create-secret", "--secret-name", "ti-e2e-secret", "--field", "DB_URL=mysql://example"},
		{"fs-vault", "replace-secret", "--secret-path", "/n/vault/ti-e2e-secret", "--from-directory", "/tmp"},
		{"fs-vault", "delete-secret", "--secret-name", "ti-e2e-secret"},
		{"fs-vault", "create-grant", "--agent-id", "ti-live-e2e", "--scope", "ti-e2e-secret/DB_URL", "--permission", "read", "--ttl", "10m"},
		{"fs-vault", "delete-grant", "--grant-id", "grant-1"},
		{"fs-vault", "mount-vault", "--mount-path", "/tmp/ti-e2e-vault"},
	}, "remote_mutation")
	unmountDryRun := runTI(t, bin, "--profile", profileName, "fs-vault", "unmount-vault", "--mount-path", "/tmp/ti-e2e-vault", "--ignore-absent", "--dry-run", "--query", "checks[].name")
	unmountDryRun.wantExitCode(0)
	for _, check := range []string{"input_validation", "mount_locator", "remote_mutation"} {
		unmountDryRun.wantStdoutContains(check)
	}
	testLiveReadOnlyDryRunRejections(t, bin, profileName, [][]string{
		{"fs-vault", "read-secret"}, {"fs-vault", "list-secrets"},
		{"fs-vault", "list-audit-events"}, {"fs-vault", "run-with-secret"},
	})
}

func TestLiveFSJournalCommandSurface(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	ensureLiveFSResource(t, bin, profileName)
	testLiveHelpCommands(t, bin, [][]string{
		{"fs-journal", "help"}, {"fs-journal", "create-journal", "help"},
		{"fs-journal", "append-journal-entries", "help"}, {"fs-journal", "read-journal-entries", "help"},
		{"fs-journal", "search-journal-entries", "help"}, {"fs-journal", "verify-journal", "help"},
	})
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"fs-journal", "create-journal", "--journal-id", "jrn-ti-e2e", "--journal-kind", "agent"},
		{"fs-journal", "append-journal-entries", "--journal-id", "jrn-ti-e2e", "--entry-json", `{"type":"task.started"}`},
	}, "remote_mutation")
	testLiveReadOnlyDryRunRejections(t, bin, profileName, [][]string{
		{"fs-journal", "read-journal-entries"}, {"fs-journal", "search-journal-entries"},
		{"fs-journal", "verify-journal"},
	})
}

func TestLiveFSGitCommandSurface(t *testing.T) {
	requireLive(t)
	testLiveHelpCommands(t, tiBinary(t), [][]string{
		{"fs-git", "help"}, {"fs-git", "clone-git-workspace", "help"},
		{"fs-git", "hydrate-git-workspace", "help"}, {"fs-git", "add-git-worktree", "help"},
		{"fs-git", "remove-git-worktree", "help"},
	})
}

func TestLiveFSGitLifecycle(t *testing.T) {
	requireLive(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("ti fs-git live e2e requires a FUSE-capable macOS or Linux host")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("ti fs-git live e2e requires git")
	}

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	suffix := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102150405"), os.Getpid())
	remoteRoot := "/ti-e2e-git-" + suffix
	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatalf("create fs-git mount path: %v", err)
	}
	unmounted := false
	remoteDeleted := false
	defer func() {
		if !unmounted {
			cleanupUnmount := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", mountPath, "--ignore-absent", "--force")
			if cleanupUnmount.exitCode != 0 {
				t.Logf("cleanup fs-git unmount failed for %s: exit=%d stdout=%s stderr=%s", mountPath, cleanupUnmount.exitCode, cleanupUnmount.stdout, cleanupUnmount.stderr)
			}
		}
		if !remoteDeleted {
			cleanupRemote := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
			if cleanupRemote.exitCode != 0 && cleanupRemote.exitCode != 5 {
				t.Logf("cleanup fs-git remote path failed for %s: exit=%d stdout=%s stderr=%s", remoteRoot, cleanupRemote.exitCode, cleanupRemote.stdout, cleanupRemote.stderr)
			}
		}
	}()

	createDir := runTI(t, bin, "--profile", profileName, "fs", "create-directory", "--path", remoteRoot, "--mode", "0755")
	createDir.wantExitCode(0)
	mount := runTI(t, bin, "--profile", profileName, "fs", "mount-file-system", "--mount-path", mountPath, "--remote-path", remoteRoot, "--driver", "fuse", "--ready-timeout", "30s")
	mount.wantExitCode(0)

	repoPath := filepath.Join(mountPath, "hello-world")
	clone := runTI(t, bin, "--profile", profileName, "fs-git", "clone-git-workspace", "--repo-url", "https://github.com/octocat/Hello-World.git", "--target-path", repoPath, "--blobless", "--hydrate", "sync")
	clone.wantExitCode(0)
	clone.wantStdoutContains(`"operation": "clone_git_workspace"`)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		t.Fatalf("cloned fs-git workspace is missing .git: %v", err)
	}

	hydrate := runTIWithInput(t, bin, "", liveFSTokenEnv(selected, t.TempDir()), "fs-git", "hydrate-git-workspace", "--target-path", repoPath, "--timeout", "5m")
	hydrate.wantExitCode(0)
	hydrate.wantStdoutContains(`"operation": "hydrate_git_workspace"`)

	worktreePath := filepath.Join(mountPath, "hello-world-worktree")
	branchName := "ti-e2e-" + suffix
	addWorktree := runTI(t, bin, "--profile", profileName, "fs-git", "add-git-worktree", "--base-path", repoPath, "--worktree-path", worktreePath, "--branch-name", branchName, "--hydrate", "sync")
	addWorktree.wantExitCode(0)
	addWorktree.wantStdoutContains(`"operation": "add_git_worktree"`)
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err != nil {
		t.Fatalf("fs-git linked worktree is missing .git: %v", err)
	}

	removeWorktree := runTI(t, bin, "--profile", profileName, "fs-git", "remove-git-worktree", "--worktree-path", worktreePath, "--force")
	removeWorktree.wantExitCode(0)
	removeWorktree.wantStdoutContains(`"status": "removed"`)

	unmount := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", mountPath)
	unmount.wantExitCode(0)
	unmounted = true
	deleteRoot := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
	deleteRoot.wantExitCode(0)
	remoteDeleted = true
}

func testLiveHelpCommands(t *testing.T, bin string, commands [][]string) {
	t.Helper()
	for _, args := range commands {
		result := runTI(t, bin, args...)
		result.wantExitCode(0)
		result.wantStdoutContains("Usage:")
	}
}

func testLiveMutatingDryRuns(t *testing.T, bin, profileName string, commands [][]string, finalCheck string) {
	t.Helper()
	for _, args := range commands {
		fullArgs := append([]string{"--profile", profileName}, args...)
		fullArgs = append(fullArgs, "--dry-run", "--query", "checks[].name")
		result := runTI(t, bin, fullArgs...)
		result.wantExitCode(0)
		result.wantStdoutContains("config_and_credentials")
		result.wantStdoutContains("endpoint_selection")
		if len(args) > 0 && args[0] == "db" {
			result.wantStdoutContains("operation_permission")
			if slices.Contains(args, "--db-cluster-id") {
				result.wantStdoutContains("cluster_discovery_permission")
			}
		} else {
			result.wantStdoutContains("permission_requirement")
		}
		result.wantStdoutContains(finalCheck)
	}
}

func testLiveReadOnlyDryRunRejections(t *testing.T, bin, profileName string, commands [][]string) {
	t.Helper()
	for _, args := range commands {
		fullArgs := append([]string{"--profile", profileName}, args...)
		fullArgs = append(fullArgs, "--dry-run")
		result := runTI(t, bin, fullArgs...)
		result.wantExitCode(2)
		result.wantStderrContains("unknown flag: --dry-run")
	}
}

func resolveLiveFSResourceByID(t *testing.T, profile *config.Profile, fileSystemID string) *config.Profile {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("determine home directory: %v", err)
	}
	selected, _, err := fscred.ResolveCredential(home, profile, fscred.ResolveCredentialOptions{FileSystemID: fileSystemID, FileSystemIDExplicit: true, TokenRequired: true})
	if err != nil {
		t.Fatalf("resolve live tdc fs resource %q: %v", fileSystemID, err)
	}
	return selected
}

func isLiveFSQuotaError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "maximum number of free clusters") ||
		strings.Contains(message, "quota or capacity limit")
}

func TestLiveFSDataPlaneLifecycle(t *testing.T) {
	requireLive(t)

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	profile := ensureLiveFSResource(t, bin, profileName)
	suffix := time.Now().UTC().Format("20060102150405")
	rootPath := "/ti-e2e-" + suffix
	sourcePath := rootPath + "/README.md"
	copyPath := rootPath + "/README.copy.md"
	movedPath := rootPath + "/README.moved.md"
	deleted := false
	defer func() {
		if deleted {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", rootPath, "--recursive")
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup delete failed for %s: exit=%d stdout=%s stderr=%s", rootPath, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
		}
	}()

	createDir := runTI(t, bin, "--profile", profileName, "fs", "create-directory", "--path", rootPath, "--mode", "0755")
	createDir.wantExitCode(0)
	createDir.wantStdoutContains(`"status": "created"`)

	content := "hello ti fs live e2e " + suffix + "\n"
	localFile := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(localFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write local test file: %v", err)
	}

	upload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", localFile, "--to-remote", sourcePath)
	upload.wantExitCode(0)
	upload.wantStdoutContains(`"status": "copied"`)

	list := runTI(t, bin, "--profile", profileName, "fs", "list-files", "--path", rootPath)
	list.wantExitCode(0)
	list.wantStdoutContains("README.md")

	listText := runTI(t, bin, "--profile", profileName, "fs", "list-files", "--path", rootPath, "--output", "text")
	listText.wantExitCode(0)
	listText.wantStdoutContains("NAME")
	listText.wantStdoutContains("README.md")

	describe := runTI(t, bin, "--profile", profileName, "fs", "describe-file", "--path", sourcePath)
	describe.wantExitCode(0)
	describe.wantStdoutContains(`"size_bytes"`)

	read := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", sourcePath)
	read.wantExitCode(0)
	if read.stdout != content {
		read.fail("read-file should return raw file bytes exactly")
	}

	rangeRead := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", sourcePath, "--offset", "6", "--length", "3")
	rangeRead.wantExitCode(0)
	if rangeRead.stdout != "ti " {
		rangeRead.fail("read-file --offset/--length should return the requested byte range")
	}

	appendText := "appended live e2e " + suffix + "\n"
	appendFile := filepath.Join(t.TempDir(), "append.txt")
	if err := os.WriteFile(appendFile, []byte(appendText), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}
	appendRemote := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", appendFile, "--to-remote", sourcePath, "--append")
	appendRemote.wantExitCode(0)
	appendRemote.wantStdoutContains(`"status": "appended"`)
	fullContent := content + appendText
	readAppended := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", sourcePath)
	readAppended.wantExitCode(0)
	if readAppended.stdout != fullContent {
		readAppended.fail("read-file should include appended bytes")
	}

	stdinPath := rootPath + "/stdin.txt"
	stdinContent := "stdin live e2e " + suffix + "\n"
	stdinUpload := runTIWithInput(t, bin, stdinContent, nil, "--profile", profileName, "fs", "copy-file", "--from-stdin", "--to-remote", stdinPath, "--tag", "source=stdin", "--tag", "suite=live-e2e", "--description", "ti live e2e stdin")
	stdinUpload.wantExitCode(0)
	stdinUpload.wantStdoutContains(`"status": "copied"`)
	stdinDownload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", stdinPath, "--to-stdout")
	stdinDownload.wantExitCode(0)
	if stdinDownload.stdout != stdinContent {
		stdinDownload.fail("copy-file --to-stdout should return raw file bytes exactly")
	}

	taggedPath := rootPath + "/tagged.md"
	taggedUpload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", localFile, "--to-remote", taggedPath, "--tag", "source=local", "--tag", "suite=live-e2e", "--description", "ti live e2e tagged")
	taggedUpload.wantExitCode(0)
	taggedUpload.wantStdoutContains(`"status": "copied"`)
	taggedDescribe := runTI(t, bin, "--profile", profileName, "fs", "describe-file", "--path", taggedPath)
	taggedDescribe.wantExitCode(0)
	taggedDescribe.wantStdoutContains(`"source": "local"`)
	taggedDescribe.wantStdoutContains(`"suite": "live-e2e"`)

	chmod := runTI(t, bin, "--profile", profileName, "fs", "chmod-file", "--path", sourcePath, "--mode", "0600")
	chmod.wantExitCode(0)
	chmod.wantStdoutContains(`"status": "updated"`)
	describeMode := runTI(t, bin, "--profile", profileName, "fs", "describe-file", "--path", sourcePath)
	describeMode.wantExitCode(0)
	describeMode.wantStdoutContains(sourcePath)

	symlinkPath := rootPath + "/README.link.md"
	symlink := runTI(t, bin, "--profile", profileName, "fs", "create-symlink", "--target", "README.md", "--link-path", symlinkPath)
	symlink.wantExitCode(0)
	symlink.wantStdoutContains(`"status": "created"`)
	listSymlink := runTI(t, bin, "--profile", profileName, "fs", "list-files", "--path", rootPath)
	listSymlink.wantExitCode(0)
	listSymlink.wantStdoutContains("README.link.md")

	hardlinkPath := rootPath + "/README.hard.md"
	hardlink := runTI(t, bin, "--profile", profileName, "fs", "create-hardlink", "--source-path", sourcePath, "--link-path", hardlinkPath)
	hardlink.wantExitCode(0)
	hardlink.wantStdoutContains(`"status": "created"`)
	readHardlink := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", hardlinkPath)
	readHardlink.wantExitCode(0)
	if readHardlink.stdout != fullContent {
		readHardlink.fail("hardlink should read the source file contents")
	}

	aliasDir := rootPath + "/alias"
	aliasMkdir := runTI(t, bin, "--profile", profileName, "fs", "mkdir", "--path", aliasDir, "--mode", "0755")
	aliasMkdir.wantExitCode(0)
	aliasMkdir.wantStdoutContains(`"status": "created"`)

	aliasContent := "alias live e2e " + suffix + "\n"
	aliasLocalFile := filepath.Join(t.TempDir(), "alias.txt")
	if err := os.WriteFile(aliasLocalFile, []byte(aliasContent), 0o644); err != nil {
		t.Fatalf("write alias local file: %v", err)
	}
	aliasPath := aliasDir + "/alias.txt"
	aliasUpload := runTI(t, bin, "--profile", profileName, "fs", "cp", "--from-local", aliasLocalFile, "--to-remote", aliasPath)
	aliasUpload.wantExitCode(0)
	aliasUpload.wantStdoutContains(`"status": "copied"`)

	aliasList := runTI(t, bin, "--profile", profileName, "fs", "ls", "--path", aliasDir)
	aliasList.wantExitCode(0)
	aliasList.wantStdoutContains("alias.txt")

	aliasStat := runTI(t, bin, "--profile", profileName, "fs", "stat", "--path", aliasPath)
	aliasStat.wantExitCode(0)
	aliasStat.wantStdoutContains(`"size_bytes"`)

	aliasRead := runTI(t, bin, "--profile", profileName, "fs", "cat", "--path", aliasPath)
	aliasRead.wantExitCode(0)
	if aliasRead.stdout != aliasContent {
		aliasRead.fail("cat alias should return raw file bytes exactly")
	}

	aliasChmod := runTI(t, bin, "--profile", profileName, "fs", "chmod", "--path", aliasPath, "--mode", "0600")
	aliasChmod.wantExitCode(0)
	aliasChmod.wantStdoutContains(`"status": "updated"`)

	aliasSymlinkPath := aliasDir + "/alias.link"
	aliasSymlink := runTI(t, bin, "--profile", profileName, "fs", "symlink", "--target", "alias.txt", "--link-path", aliasSymlinkPath)
	aliasSymlink.wantExitCode(0)
	aliasSymlink.wantStdoutContains(`"status": "created"`)

	aliasHardlinkPath := aliasDir + "/alias.hard"
	aliasHardlink := runTI(t, bin, "--profile", profileName, "fs", "hardlink", "--source-path", aliasPath, "--link-path", aliasHardlinkPath)
	aliasHardlink.wantExitCode(0)
	aliasHardlink.wantStdoutContains(`"status": "created"`)

	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "grep", "--path", aliasDir, "--pattern", "alias live e2e", "--limit", "5"}, "alias.txt", 5*time.Minute, "grep alias file content")
	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "find", "--path", aliasDir, "--file-name-pattern", "alias.txt", "--limit", "5"}, aliasPath, 2*time.Minute, "find alias file by name")

	aliasCopyPath := aliasDir + "/alias.copy.txt"
	aliasCopy := runTI(t, bin, "--profile", profileName, "fs", "cp", "--from-remote", aliasPath, "--to-remote", aliasCopyPath)
	aliasCopy.wantExitCode(0)
	aliasCopy.wantStdoutContains(`"status": "copied"`)

	aliasMovedPath := aliasDir + "/alias.moved.txt"
	aliasMove := runTI(t, bin, "--profile", profileName, "fs", "mv", "--from-remote", aliasCopyPath, "--to-remote", aliasMovedPath)
	aliasMove.wantExitCode(0)
	aliasMove.wantStdoutContains(`"status": "moved"`)

	aliasDelete := runTI(t, bin, "--profile", profileName, "fs", "rm", "--path", aliasMovedPath)
	aliasDelete.wantExitCode(0)
	aliasDelete.wantStdoutContains(`"status": "deleted"`)

	largePath := rootPath + "/large.bin"
	largeContent := strings.Repeat("0123456789abcdef", 4096)
	largeFile := filepath.Join(t.TempDir(), "large.bin")
	if err := os.WriteFile(largeFile, []byte(largeContent), 0o644); err != nil {
		t.Fatalf("write large local file: %v", err)
	}
	largeUpload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", largeFile, "--to-remote", largePath)
	largeUpload.wantExitCode(0)
	largeUpload.wantStdoutContains(`"status": "copied"`)
	readLarge := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", largePath)
	readLarge.wantExitCode(0)
	if readLarge.stdout != largeContent {
		readLarge.fail("large multipart upload should preserve file contents")
	}

	largeAppendText := strings.Repeat("append-"+suffix+"\n", 2048)
	largeAppendFile := filepath.Join(t.TempDir(), "large-append.txt")
	if err := os.WriteFile(largeAppendFile, []byte(largeAppendText), 0o644); err != nil {
		t.Fatalf("write large append file: %v", err)
	}
	largeAppend := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", largeAppendFile, "--to-remote", largePath, "--append")
	largeAppend.wantExitCode(0)
	largeAppend.wantStdoutContains(`"status": "appended"`)
	readLargeAppended := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", largePath)
	readLargeAppended.wantExitCode(0)
	if readLargeAppended.stdout != largeContent+largeAppendText {
		readLargeAppended.fail("efficient append should preserve existing and appended bytes")
	}

	resumeUploadPath := rootPath + "/resume-upload.bin"
	resumeUploadContent := strings.Repeat("resume-"+suffix+"-", 4096)
	resumeUploadFile := filepath.Join(t.TempDir(), "resume-upload.bin")
	if err := os.WriteFile(resumeUploadFile, []byte(resumeUploadContent), 0o644); err != nil {
		t.Fatalf("write resume upload file: %v", err)
	}
	fsClient := liveFSClient(t, profile, authz.FSFileWrite)
	resumeUploadID := initiateLiveUpload(t, fsClient, resumeUploadPath, resumeUploadFile)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = fsClient.AbortUpload(ctx, resumeUploadID)
	}()
	resumeUpload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", resumeUploadFile, "--to-remote", resumeUploadPath, "--resume")
	resumeUpload.wantExitCode(0)
	resumeUpload.wantStdoutContains(`"status": "resumed"`)
	readResumeUpload := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", resumeUploadPath)
	readResumeUpload.wantExitCode(0)
	if readResumeUpload.stdout != resumeUploadContent {
		readResumeUpload.fail("upload resume should preserve uploaded contents")
	}

	layerID := "ti-e2e-layer-" + suffix
	checkpointID := layerID + "-cp"
	layerClosed := false
	defer func() {
		if layerClosed {
			return
		}
		rollback := runTI(t, bin, "--profile", profileName, "fs", "rollback-layer", "--layer-id", layerID)
		if rollback.exitCode != 0 && rollback.exitCode != 5 {
			t.Logf("cleanup rollback failed for layer %s: exit=%d stdout=%s stderr=%s", layerID, rollback.exitCode, rollback.stdout, rollback.stderr)
		}
	}()
	createLayer := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs", "create-layer",
		"--layer-id", layerID,
		"--base-root-path", rootPath,
		"--layer-name", "live-e2e-"+suffix,
		"--durability-mode", "restore-safe",
		"--actor-id", "ti-live-e2e",
		"--tag", "test=ti-e2e",
		"--tag", "suffix="+suffix,
	)
	createLayer.wantExitCode(0)
	createLayer.wantStdoutContains(layerID)

	listLayers := runTI(t, bin, "--profile", profileName, "fs", "list-layers", "--query", fmt.Sprintf("layers[?layer_id=='%s'].layer_id | [0]", layerID))
	listLayers.wantExitCode(0)
	listLayers.wantStdoutContains(layerID)

	describeLayer := runTI(t, bin, "--profile", profileName, "fs", "describe-layer", "--layer-id", layerID)
	describeLayer.wantExitCode(0)
	describeLayer.wantStdoutContains(layerID)

	layerCopyPath := rootPath + "/copy-layer.txt"
	layerCopyContent := "copy-file into layer " + suffix + "\n"
	layerCopyFile := filepath.Join(t.TempDir(), "copy-layer.txt")
	if err := os.WriteFile(layerCopyFile, []byte(layerCopyContent), 0o644); err != nil {
		t.Fatalf("write layer copy file: %v", err)
	}
	copyToLayer := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", layerCopyFile, "--to-remote", layerCopyPath, "--layer-id", layerID)
	copyToLayer.wantExitCode(0)
	copyToLayer.wantStdoutContains(`"status"`)

	diffLayer := runTI(t, bin, "--profile", profileName, "fs", "diff-layer", "--layer-id", layerID, "--output", "text")
	diffLayer.wantExitCode(0)
	diffLayer.wantStdoutContains(layerCopyPath)

	createCheckpoint := runTI(t, bin, "--profile", profileName, "fs", "create-layer-checkpoint", "--layer-id", layerID, "--checkpoint-id", checkpointID, "--label", "live-e2e")
	createCheckpoint.wantExitCode(0)
	createCheckpoint.wantStdoutContains(checkpointID)

	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "find-files", "--path", rootPath, "--file-name-pattern", "copy-layer.txt", "--layer-id", layerID, "--limit", "5"}, layerCopyPath, 2*time.Minute, "find file inside layer")

	commitLayer := runTI(t, bin, "--profile", profileName, "fs", "commit-layer", "--layer-id", layerID)
	commitLayer.wantExitCode(0)
	commitLayer.wantStdoutContains(`"status"`)
	layerClosed = true
	waitLiveRemoteRead(t, bin, profileName, layerCopyPath, layerCopyContent, 30*time.Second)
	testLiveFSDataPlaneContinuation(t, bin, profileName, rootPath, sourcePath, copyPath, movedPath, suffix, fullContent, &deleted)
}

func TestLiveFSVaultLifecycle(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	suffix := time.Now().UTC().Format("20060102150405")
	vaultSecretName := "ti-e2e-vault-" + suffix
	vaultDeleted := false
	defer func() {
		if vaultDeleted {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "fs-vault", "delete-secret", "--secret-name", vaultSecretName)
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup vault secret failed for %s: exit=%d stdout=%s stderr=%s", vaultSecretName, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
		}
	}()
	createVaultSecret := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs-vault", "create-secret",
		"--secret-name", vaultSecretName,
		"--field", "DB_URL=mysql://"+suffix,
		"--field", "PASSWORD=secret-"+suffix,
	)
	createVaultSecret.wantExitCode(0)
	createVaultSecret.wantStdoutContains(vaultSecretName)

	listVaultSecrets := runTIWithInput(t, bin, "", liveFSTokenEnv(selected, t.TempDir()), "fs-vault", "list-secrets")
	listVaultSecrets.wantExitCode(0)
	listVaultSecrets.wantStdoutContains(vaultSecretName)

	readVaultSecret := runTI(t, bin, "--profile", profileName, "fs-vault", "read-secret", "--secret-name", vaultSecretName, "--field", "PASSWORD", "--format", "raw")
	readVaultSecret.wantExitCode(0)
	if readVaultSecret.stdout != "secret-"+suffix {
		readVaultSecret.fail("vault read-secret --format raw should return exact field bytes")
	}

	readVaultEnv := runTI(t, bin, "--profile", profileName, "fs-vault", "read-secret", "--secret-name", vaultSecretName, "--format", "env")
	readVaultEnv.wantExitCode(0)
	readVaultEnv.wantStdoutContains("DB_URL=mysql://" + suffix)
	readVaultEnv.wantStdoutContains("PASSWORD=secret-" + suffix)

	replaceVaultDir := filepath.Join(t.TempDir(), "vault-replace")
	if err := os.MkdirAll(replaceVaultDir, 0o755); err != nil {
		t.Fatalf("create vault replace dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replaceVaultDir, "DB_URL"), []byte("mysql://replaced-"+suffix), 0o600); err != nil {
		t.Fatalf("write replacement DB_URL: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replaceVaultDir, "PASSWORD"), []byte("replaced-"+suffix), 0o600); err != nil {
		t.Fatalf("write replacement PASSWORD: %v", err)
	}
	replaceVaultSecret := runTI(t, bin, "--profile", profileName, "fs-vault", "replace-secret", "--secret-path", "/n/vault/"+vaultSecretName, "--from-directory", replaceVaultDir)
	replaceVaultSecret.wantExitCode(0)
	replaceVaultSecret.wantStdoutContains(vaultSecretName)

	readReplacedVaultSecret := runTI(t, bin, "--profile", profileName, "fs-vault", "read-secret", "--secret-name", vaultSecretName, "--field", "PASSWORD", "--format", "raw")
	readReplacedVaultSecret.wantExitCode(0)
	if readReplacedVaultSecret.stdout != "replaced-"+suffix {
		readReplacedVaultSecret.fail("vault replace-secret should replace stored field bytes")
	}

	createVaultGrant := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs-vault", "create-grant",
		"--agent-id", "ti-live-e2e",
		"--scope", vaultSecretName,
		"--permission", "read",
		"--ttl", "10m",
		"--label-hint", "ti-live-e2e-"+suffix,
	)
	createVaultGrant.wantExitCode(0)
	vaultGrant := decodeLiveVaultToken(t, createVaultGrant)
	if vaultGrant.Token == "" || vaultGrant.GrantID == "" {
		t.Fatalf("unexpected vault grant response: %#v\n%s", vaultGrant, createVaultGrant.stdout)
	}
	grantDeleted := false
	defer func() {
		if grantDeleted {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "fs-vault", "delete-grant", "--grant-id", vaultGrant.GrantID, "--reason", "cleanup")
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup vault grant failed for %s: exit=%d stdout=%s stderr=%s", vaultGrant.GrantID, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
		}
	}()
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		vaultMountPath := filepath.Join(t.TempDir(), "vault-mount")
		if err := os.MkdirAll(vaultMountPath, 0o755); err != nil {
			t.Fatalf("create vault mount path: %v", err)
		}
		vaultUnmounted := false
		defer func() {
			if vaultUnmounted {
				return
			}
			cleanupUnmount := runTI(t, bin, "--profile", profileName, "fs-vault", "unmount-vault", "--mount-path", vaultMountPath, "--ignore-absent", "--force")
			if cleanupUnmount.exitCode != 0 {
				t.Logf("cleanup vault unmount failed for %s: exit=%d stdout=%s stderr=%s", vaultMountPath, cleanupUnmount.exitCode, cleanupUnmount.stdout, cleanupUnmount.stderr)
			}
		}()
		mountVault := runTI(t, bin, "--profile", profileName, "fs-vault", "mount-vault", "--mount-path", vaultMountPath, "--vault-token", vaultGrant.Token, "--ready-timeout", "30s")
		mountVault.wantExitCode(0)
		mountVault.wantStdoutContains(`"status": "mounted"`)
		waitLiveLocalFile(t, filepath.Join(vaultMountPath, vaultSecretName, "PASSWORD"), "replaced-"+suffix, 30*time.Second)
		unmountVault := runTI(t, bin, "--profile", profileName, "fs-vault", "unmount-vault", "--mount-path", vaultMountPath)
		unmountVault.wantExitCode(0)
		unmountVault.wantStdoutContains(`"status": "unmounted"`)
		vaultUnmounted = true
	}
	readVaultWithGrant := runTI(t, bin, "--profile", profileName, "fs-vault", "read-secret", "--secret-name", vaultSecretName, "--field", "DB_URL", "--format", "raw", "--vault-token", vaultGrant.Token)
	readVaultWithGrant.wantExitCode(0)
	if readVaultWithGrant.stdout != "mysql://replaced-"+suffix {
		readVaultWithGrant.fail("delegated vault grant should read scoped field")
	}
	deleteVaultGrant := runTI(t, bin, "--profile", profileName, "fs-vault", "delete-grant", "--grant-id", vaultGrant.GrantID, "--reason", "live-e2e-complete")
	deleteVaultGrant.wantExitCode(0)
	grantDeleted = true

	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("env"); err == nil {
			runWithVault := runTI(t, bin, "--profile", profileName, "fs-vault", "run-with-secret", "--secret-path", "/n/vault/"+vaultSecretName, "--", "env")
			runWithVault.wantExitCode(0)
			runWithVault.wantStdoutContains("DB_URL=mysql://replaced-" + suffix)
			runWithVault.wantStdoutContains("PASSWORD=replaced-" + suffix)
			runWithVault.wantStdoutNotContains("TIDB_CLOUD_PRIVATE_KEY=")
		}
	}

	listVaultAuditEvents := runTI(t, bin, "--profile", profileName, "fs-vault", "list-audit-events", "--secret-name", vaultSecretName, "--limit", "20")
	listVaultAuditEvents.wantExitCode(0)
	listVaultAuditEvents.wantStdoutContains(`"events"`)

	deleteVaultSecret := runTI(t, bin, "--profile", profileName, "fs-vault", "delete-secret", "--secret-name", vaultSecretName)
	deleteVaultSecret.wantExitCode(0)
	deleteVaultSecret.wantStdoutContains(`"status": "deleted"`)
	vaultDeleted = true
}

func TestLiveFSJournalLifecycle(t *testing.T) {
	requireLive(t)
	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	suffix := time.Now().UTC().Format("20060102150405")
	rootPath := "/ti-e2e-journal-" + suffix
	journalID := "jrn-ti-e2e-" + suffix
	appendID := "app-ti-e2e-" + suffix
	createJournal := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs-journal", "create-journal",
		"--journal-id", journalID,
		"--journal-kind", "agent",
		"--title", "ti live e2e "+suffix,
		"--actor", "agent:ti-live-e2e",
		"--label", "test=ti-e2e",
		"--label", "suffix="+suffix,
	)
	createJournal.wantExitCode(0)
	createJournal.wantStdoutContains(journalID)

	appendJournal := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs-journal", "append-journal-entries",
		"--journal-id", journalID,
		"--idempotency-key", appendID,
		"--entry-json", `{"type":"task.started","summary":{"message":"ti live e2e `+suffix+`"}}`,
		"--subject", "path:"+rootPath,
	)
	appendJournal.wantExitCode(0)
	appendJournal.wantStdoutContains(`"count": 1`)
	appendJournal.wantStdoutContains(appendID)

	readJournal := runTIWithInput(t, bin, "", liveFSTokenEnv(selected, t.TempDir()), "fs-journal", "read-journal-entries", "--journal-id", journalID, "--limit", "10")
	readJournal.wantExitCode(0)
	readJournal.wantStdoutContains(journalID)
	readJournal.wantStdoutContains("task.started")

	waitLiveFSResult(
		t,
		bin,
		[]string{
			"--profile", profileName,
			"fs-journal", "search-journal-entries",
			"--entry-type", "task.started",
			"--label", "test=ti-e2e",
			"--limit", "10",
		},
		journalID,
		2*time.Minute,
		"index journal entry",
	)

	verifyJournal := runTI(t, bin, "--profile", profileName, "fs-journal", "verify-journal", "--journal-id", journalID, "--output", "text")
	verifyJournal.wantExitCode(0)
	verifyJournal.wantStdoutContains("ok journal=" + journalID)
}

func testLiveFSDataPlaneContinuation(t *testing.T, bin, profileName, rootPath, sourcePath, copyPath, movedPath, suffix, fullContent string, deleted *bool) {
	t.Helper()
	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "search-file-content", "--path", rootPath, "--pattern", "ti fs live e2e", "--limit", "5"}, "README.md", 5*time.Minute, "find uploaded file content")
	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "find-files", "--path", rootPath, "--file-name-pattern", "*.md", "--limit", "5"}, "README.md", 2*time.Minute, "find uploaded file by name")

	remoteCopy := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", sourcePath, "--to-remote", copyPath)
	remoteCopy.wantExitCode(0)
	remoteCopy.wantStdoutContains(`"status": "copied"`)

	move := runTI(t, bin, "--profile", profileName, "fs", "move-file", "--from-remote", copyPath, "--to-remote", movedPath)
	move.wantExitCode(0)
	move.wantStdoutContains(`"status": "moved"`)

	downloadPath := filepath.Join(t.TempDir(), "nested", "downloaded.md")
	download := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", movedPath, "--to-local", downloadPath, "--create-parents")
	download.wantExitCode(0)
	download.wantStdoutContains(`"status": "copied"`)
	downloaded, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(downloaded) != fullContent {
		t.Fatalf("downloaded file mismatch: got %q want %q", downloaded, fullContent)
	}

	resumePath := filepath.Join(t.TempDir(), "resume.md")
	if err := os.WriteFile(resumePath, []byte(fullContent[:5]), 0o644); err != nil {
		t.Fatalf("write partial resume file: %v", err)
	}
	resume := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", movedPath, "--to-local", resumePath, "--resume")
	resume.wantExitCode(0)
	resume.wantStdoutContains(`"status": "resumed"`)
	resumed, err := os.ReadFile(resumePath)
	if err != nil {
		t.Fatalf("read resumed file: %v", err)
	}
	if string(resumed) != fullContent {
		t.Fatalf("resumed file mismatch: got %q want %q", resumed, fullContent)
	}

	localTree := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(localTree, "nested"), 0o755); err != nil {
		t.Fatalf("create local tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "alpha.txt"), []byte("alpha "+suffix), 0o644); err != nil {
		t.Fatalf("write local tree file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localTree, "nested", "beta.txt"), []byte("beta "+suffix), 0o644); err != nil {
		t.Fatalf("write nested local tree file: %v", err)
	}
	treeRoot := rootPath + "/tree"
	recursiveUpload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", localTree, "--to-remote", treeRoot, "--recursive")
	recursiveUpload.wantExitCode(0)
	recursiveUpload.wantStdoutContains(`"status": "copied"`)
	readTreeFile := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", treeRoot+"/nested/beta.txt")
	readTreeFile.wantExitCode(0)
	if readTreeFile.stdout != "beta "+suffix {
		readTreeFile.fail("recursive local-to-remote copy should preserve nested file contents")
	}

	treeCopyRoot := rootPath + "/tree-copy"
	recursiveRemoteCopy := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", treeRoot, "--to-remote", treeCopyRoot, "--recursive")
	recursiveRemoteCopy.wantExitCode(0)
	recursiveRemoteCopy.wantStdoutContains(`"status": "copied"`)
	readCopiedTreeFile := runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", treeCopyRoot+"/nested/beta.txt")
	readCopiedTreeFile.wantExitCode(0)
	if readCopiedTreeFile.stdout != "beta "+suffix {
		readCopiedTreeFile.fail("recursive remote-to-remote copy should preserve nested file contents")
	}

	downloadTree := filepath.Join(t.TempDir(), "download-tree")
	recursiveDownload := runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--from-remote", treeRoot, "--to-local", downloadTree, "--recursive")
	recursiveDownload.wantExitCode(0)
	recursiveDownload.wantStdoutContains(`"status": "copied"`)
	downloadedTreeFile, err := os.ReadFile(filepath.Join(downloadTree, "nested", "beta.txt"))
	if err != nil {
		t.Fatalf("read recursive download file: %v", err)
	}
	if string(downloadedTreeFile) != "beta "+suffix {
		t.Fatalf("recursive download mismatch: got %q", downloadedTreeFile)
	}

	packLocalRoot := t.TempDir()
	packOverlayFile := filepath.Join(packLocalRoot, "overlay", "repo", "cache", "item.txt")
	if err := os.MkdirAll(filepath.Dir(packOverlayFile), 0o755); err != nil {
		t.Fatalf("create pack overlay dir: %v", err)
	}
	packContent := "pack portable overlay " + suffix + "\n"
	if err := os.WriteFile(packOverlayFile, []byte(packContent), 0o644); err != nil {
		t.Fatalf("write pack overlay file: %v", err)
	}
	packArchivePath := rootPath + "/packs/portable.tar.gz"
	pack := runTI(t, bin, "--profile", profileName, "fs", "pack-file-system", "--local-root", packLocalRoot, "--remote-root", rootPath, "--mount-profile", "portable", "--archive-path", packArchivePath)
	pack.wantExitCode(0)
	pack.wantStdoutContains(`"status": "packed"`)
	pack.wantStdoutContains(`"archive_path": "` + packArchivePath + `"`)
	unpackLocalRoot := t.TempDir()
	unpack := runTI(t, bin, "--profile", profileName, "fs", "unpack-file-system", "--local-root", unpackLocalRoot, "--remote-root", rootPath, "--mount-profile", "portable", "--archive-path", packArchivePath)
	unpack.wantExitCode(0)
	unpack.wantStdoutContains(`"status": "unpacked"`)
	unpackedPackFile, err := os.ReadFile(filepath.Join(unpackLocalRoot, "overlay", "repo", "cache", "item.txt"))
	if err != nil {
		t.Fatalf("read unpacked pack file: %v", err)
	}
	if string(unpackedPackFile) != packContent {
		t.Fatalf("unpacked pack content mismatch: got %q want %q", unpackedPackFile, packContent)
	}

	deleteMoved := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", movedPath)
	deleteMoved.wantExitCode(0)
	deleteMoved.wantStdoutContains(`"status": "deleted"`)

	deleteRoot := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", rootPath, "--recursive")
	deleteRoot.wantExitCode(0)
	deleteRoot.wantStdoutContains(`"status": "deleted"`)
	*deleted = true
}

func TestLiveFSMountRuntime(t *testing.T) {
	requireLive(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("ti fs FUSE mount live e2e currently runs on macOS or Linux")
	}

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	ensureLiveFSResource(t, bin, profileName)
	suffix := time.Now().UTC().Format("20060102150405")
	remoteRoot := "/ti-e2e-mount-" + suffix
	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatalf("create mount path: %v", err)
	}
	unmounted := false
	remoteDeleted := false
	defer func() {
		if !unmounted {
			cleanupUnmount := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", mountPath, "--ignore-absent", "--force")
			if cleanupUnmount.exitCode != 0 {
				t.Logf("cleanup unmount failed for %s: exit=%d stdout=%s stderr=%s", mountPath, cleanupUnmount.exitCode, cleanupUnmount.stdout, cleanupUnmount.stderr)
			}
		}
		if !remoteDeleted {
			cleanupRemote := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
			if cleanupRemote.exitCode != 0 && cleanupRemote.exitCode != 5 {
				t.Logf("cleanup remote failed for %s: exit=%d stdout=%s stderr=%s", remoteRoot, cleanupRemote.exitCode, cleanupRemote.stdout, cleanupRemote.stderr)
			}
		}
	}()

	createDir := runTI(t, bin, "--profile", profileName, "fs", "create-directory", "--path", remoteRoot, "--mode", "0755")
	createDir.wantExitCode(0)
	localSeed := filepath.Join(t.TempDir(), "README.md")
	seedContent := "hello mounted ti fs " + suffix + "\n"
	if err := os.WriteFile(localSeed, []byte(seedContent), 0o644); err != nil {
		t.Fatalf("write local seed: %v", err)
	}
	upload := runLiveFSSetupCommand(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", localSeed, "--to-remote", remoteRoot+"/README.md")
	upload.wantExitCode(0)

	mount := runTI(t, bin, "--profile", profileName, "fs", "mount", "--mount-path", mountPath, "--remote-path", remoteRoot, "--ready-timeout", "30s")
	mount.wantExitCode(0)
	mount.wantStdoutContains(`"status": "mounted"`)
	mount.wantStdoutContains(`"driver":`)

	waitLiveLocalFile(t, filepath.Join(mountPath, "README.md"), seedContent, 30*time.Second)
	overwriteContent := "overwritten through mounted ti fs " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(mountPath, "README.md"), []byte(overwriteContent), 0o644); err != nil {
		t.Fatalf("overwrite existing remote file through mount failed: %v", err)
	}
	localWrite := "written through mounted ti fs " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(mountPath, "local-write.txt"), []byte(localWrite), 0o644); err != nil {
		t.Fatalf("write through mount failed: %v", err)
	}
	if strings.Contains(mount.stdout, `"driver": "fuse"`) {
		drain := runTI(t, bin, "--profile", profileName, "fs", "drain", "--mount-path", mountPath, "--timeout", "30s")
		drain.wantExitCode(0)
		drain.wantStdoutContains(`"status": "drained"`)
	}
	waitLiveRemoteRead(t, bin, profileName, remoteRoot+"/README.md", overwriteContent, 30*time.Second)
	waitLiveRemoteRead(t, bin, profileName, remoteRoot+"/local-write.txt", localWrite, 30*time.Second)

	unmount := runTI(t, bin, "--profile", profileName, "fs", "umount", "--mount-path", mountPath)
	unmount.wantExitCode(0)
	unmount.wantStdoutContains(`"status": "unmounted"`)
	unmounted = true

	deleteRoot := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
	deleteRoot.wantExitCode(0)
	remoteDeleted = true
}

func TestLiveFSConfigurationFreeAccess(t *testing.T) {
	requireLive(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("ti fs configuration-free mount live e2e currently runs on macOS or Linux")
	}

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	suffix := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102150405"), os.Getpid())
	selected := ensureLiveFSResource(t, bin, profileName)
	var created struct {
		FileSystemID string `json:"file_system_id"`
		RegionCode   string `json:"region_code"`
		FSToken      string `json:"fs_token"`
		TokenID      string `json:"token_id"`
		Status       string `json:"status"`
	}
	createToken := runTI(t, bin, "--profile", profileName, "--region", selected.FSPlacementRegionCode, "fs", "generate-file-system-token",
		"--file-system-id", selected.FSTenantID,
		"--token-name", "ti-e2e-config-free-"+suffix,
		"--ttl", "1h")
	createToken.wantExitCode(0)
	if err := json.Unmarshal([]byte(createToken.stdout), &created); err != nil {
		t.Fatalf("decode configuration-free FS token result: %v", err)
	}
	createToken.stdout = ""
	created.RegionCode = selected.FSPlacementRegionCode
	if created.FileSystemID == "" || created.RegionCode == "" || created.FSToken == "" || created.TokenID == "" {
		t.Fatalf("configuration-free FS token result is incomplete")
	}
	if created.Status != "active" {
		t.Fatalf("generated configuration-free token is in status %q", created.Status)
	}

	deletedToken := false
	defer func() {
		if deletedToken {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "--region", created.RegionCode, "fs", "delete-file-system-token",
			"--file-system-id", created.FileSystemID, "--token-id", created.TokenID)
		if cleanup.exitCode != 0 {
			t.Logf("cleanup configuration-free FS token failed for %q: exit=%d stderr=%s", created.TokenID, cleanup.exitCode, strings.TrimSpace(cleanup.stderr))
		}
	}()

	profile := liveProfile(t)
	cleanHome := t.TempDir()
	authEnv := []string{
		"HOME=" + cleanHome,
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
		"TI_REGION_CODE=" + created.RegionCode,
		"TI_FS_FILE_SYSTEM_ID=",
		"TI_FS_TOKEN=" + created.FSToken,
	}
	waitLiveFSTokenAccess(t, bin, profileName, created.RegionCode, created.FileSystemID, created.FSToken, true, 30*time.Second)
	remoteRoot := "/ti-e2e-token-" + suffix
	remoteDeleted := false
	defer func() {
		if remoteDeleted {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--file-system-id", created.FileSystemID, "--path", remoteRoot, "--recursive")
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup configuration-free remote path failed for %s: exit=%d stderr=%s", remoteRoot, cleanup.exitCode, strings.TrimSpace(cleanup.stderr))
		}
	}()

	createDir := runTIWithInput(t, bin, "", authEnv, "fs", "create-directory", "--path", remoteRoot)
	createDir.wantExitCode(0)
	seedContent := "configuration-free seed " + suffix + "\n"
	seedPath := filepath.Join(t.TempDir(), "seed.txt")
	if err := os.WriteFile(seedPath, []byte(seedContent), 0o600); err != nil {
		t.Fatalf("write configuration-free seed: %v", err)
	}
	upload := runTIWithInput(t, bin, "", authEnv, "fs", "copy-file", "--from-local", seedPath, "--to-remote", remoteRoot+"/seed.txt")
	upload.wantExitCode(0)
	read := runTIWithInput(t, bin, "", authEnv, "fs", "read-file", "--path", remoteRoot+"/seed.txt")
	read.wantExitCode(0)
	if read.stdout != seedContent {
		read.fail("configuration-free data-plane read should match uploaded bytes")
	}

	mountPath := filepath.Join(cleanHome, "mount")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatalf("create configuration-free mount path: %v", err)
	}
	unmounted := false
	locatorEnv := liveFSLocatorEnv(cleanHome)
	defer func() {
		if unmounted {
			return
		}
		cleanup := runTIWithInput(t, bin, "", locatorEnv, "fs", "unmount-file-system", "--mount-path", mountPath, "--ignore-absent", "--force")
		if cleanup.exitCode != 0 {
			t.Logf("cleanup configuration-free mount failed for %s: exit=%d stderr=%s", mountPath, cleanup.exitCode, strings.TrimSpace(cleanup.stderr))
		}
	}()

	mount := runTIWithInput(t, bin, "", authEnv, "fs", "mount-file-system", "--mount-path", mountPath, "--remote-path", remoteRoot, "--ready-timeout", "30s")
	mount.wantExitCode(0)
	waitLiveLocalFile(t, filepath.Join(mountPath, "seed.txt"), seedContent, 30*time.Second)

	mountedContent := "configuration-free mount write " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(mountPath, "mounted.txt"), []byte(mountedContent), 0o600); err != nil {
		t.Fatalf("write through configuration-free mount: %v", err)
	}
	waitLiveRemoteReadWithEnv(t, bin, authEnv, remoteRoot+"/mounted.txt", mountedContent, 30*time.Second)

	directContent := "configuration-free direct write " + suffix + "\n"
	directPath := filepath.Join(t.TempDir(), "direct.txt")
	if err := os.WriteFile(directPath, []byte(directContent), 0o600); err != nil {
		t.Fatalf("write configuration-free direct source: %v", err)
	}
	directUpload := runTIWithInput(t, bin, "", authEnv, "fs", "copy-file", "--from-local", directPath, "--to-remote", remoteRoot+"/direct.txt")
	directUpload.wantExitCode(0)
	waitLiveLocalFile(t, filepath.Join(mountPath, "direct.txt"), directContent, 30*time.Second)

	if strings.Contains(mount.stdout, `"driver": "fuse"`) {
		drain := runTIWithInput(t, bin, "", locatorEnv, "fs", "drain-file-system", "--mount-path", mountPath, "--timeout", "30s")
		drain.wantExitCode(0)
	}
	unmount := runTIWithInput(t, bin, "", locatorEnv, "fs", "unmount-file-system", "--mount-path", mountPath)
	unmount.wantExitCode(0)
	unmounted = true

	for _, path := range []string{
		filepath.Join(cleanHome, ".ti", "config"),
		filepath.Join(cleanHome, ".ti", "credentials"),
		filepath.Join(cleanHome, ".ti", "fs_resources"),
		filepath.Join(cleanHome, ".ti", "fs_credentials"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("configuration-free live command persisted ti configuration at %s: %v", path, err)
		}
	}
	locators, err := filepath.Glob(filepath.Join(cleanHome, ".ti", "mounts", "*.locator.json"))
	if err != nil {
		t.Fatalf("inspect configuration-free mount locators: %v", err)
	}
	if len(locators) != 0 {
		t.Fatalf("successful configuration-free unmount left %d mount locator(s)", len(locators))
	}

	controlHome := t.TempDir()
	controlEnv := []string{
		"HOME=" + controlHome,
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=" + profile.TiDBCloudPublicKey,
		"TIDB_CLOUD_PRIVATE_KEY=" + profile.TiDBCloudPrivateKey,
		"TI_REGION_CODE=" + created.RegionCode,
		"TI_FS_FILE_SYSTEM_ID=",
		"TI_FS_TOKEN=",
	}
	cleanList := runTIWithInput(t, bin, "", controlEnv, "fs", "list-file-systems")
	cleanList.wantExitCode(0)
	cleanList.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
	cleanDescribe := runTIWithInput(t, bin, "", controlEnv, "fs", "describe-file-system", "--file-system-id", created.FileSystemID)
	cleanDescribe.wantExitCode(0)
	cleanDescribe.wantStdoutContains(`"has_local_token": false`)

	tokenPath := filepath.Join(t.TempDir(), "fs-token")
	if err := os.WriteFile(tokenPath, []byte(created.FSToken+"\n"), 0o600); err != nil {
		t.Fatalf("write configuration-free import token: %v", err)
	}
	importEnv := []string{
		"HOME=" + cleanHome,
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
		"TI_REGION_CODE=" + created.RegionCode,
		"TI_FS_FILE_SYSTEM_ID=",
		"TI_FS_TOKEN=",
	}
	imported := runTIWithInput(t, bin, "", importEnv, "fs", "import-file-system-token", "--from-file", tokenPath)
	imported.wantExitCode(0)
	imported.wantStdoutContains(`"file_system_id": "` + created.FileSystemID + `"`)
	storedCredentialEnv := []string{
		"HOME=" + cleanHome,
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
		"TI_REGION_CODE=",
		"TI_FS_FILE_SYSTEM_ID=" + created.FileSystemID,
		"TI_FS_TOKEN=",
	}
	readWithImportedToken := runTIWithInput(t, bin, "", storedCredentialEnv, "fs", "read-file", "--path", remoteRoot+"/seed.txt")
	readWithImportedToken.wantExitCode(0)
	if readWithImportedToken.stdout != seedContent {
		readWithImportedToken.fail("imported local credential should authorize data-plane access")
	}
	if removed, err := fscred.DeleteCredential(cleanHome, config.DefaultProfile, created.FileSystemID); err != nil || !removed {
		t.Fatalf("remove imported credential before control-plane delete: removed=%t err=%v", removed, err)
	}

	deleteRemote := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--file-system-id", created.FileSystemID, "--path", remoteRoot, "--recursive")
	deleteRemote.wantExitCode(0)
	remoteDeleted = true
	deleteToken := runTIWithInput(t, bin, "", controlEnv, "fs", "delete-file-system-token",
		"--file-system-id", created.FileSystemID, "--token-id", created.TokenID)
	deleteToken.wantExitCode(0)
	deleteToken.wantStdoutContains(`"status": "revoked"`)
	deletedToken = true
}

func TestLiveFSWebDAVMountRuntime(t *testing.T) {
	requireLive(t)
	if runtime.GOOS != "darwin" {
		t.Skip("ti fs WebDAV mount live e2e currently runs on macOS")
	}
	if _, err := exec.LookPath("mount_webdav"); err != nil {
		t.Skip("mount_webdav is not available")
	}
	if _, err := exec.LookPath("umount"); err != nil {
		t.Skip("umount is not available")
	}

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	ensureLiveFSResource(t, bin, profileName)
	suffix := time.Now().UTC().Format("20060102150405")
	remoteRoot := "/ti-e2e-webdav-mount-" + suffix
	mountPath := filepath.Join(t.TempDir(), "mount")
	if err := os.MkdirAll(mountPath, 0o755); err != nil {
		t.Fatalf("create mount path: %v", err)
	}
	unmounted := false
	remoteDeleted := false
	defer func() {
		if !unmounted {
			cleanupUnmount := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", mountPath, "--ignore-absent", "--force")
			if cleanupUnmount.exitCode != 0 {
				t.Logf("cleanup unmount failed for %s: exit=%d stdout=%s stderr=%s", mountPath, cleanupUnmount.exitCode, cleanupUnmount.stdout, cleanupUnmount.stderr)
			}
		}
		if !remoteDeleted {
			cleanupRemote := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
			if cleanupRemote.exitCode != 0 && cleanupRemote.exitCode != 5 {
				t.Logf("cleanup remote failed for %s: exit=%d stdout=%s stderr=%s", remoteRoot, cleanupRemote.exitCode, cleanupRemote.stdout, cleanupRemote.stderr)
			}
		}
	}()

	createDir := runTI(t, bin, "--profile", profileName, "fs", "create-directory", "--path", remoteRoot, "--mode", "0755")
	createDir.wantExitCode(0)
	localSeed := filepath.Join(t.TempDir(), "README.md")
	seedContent := "hello webdav mounted ti fs " + suffix + "\n"
	if err := os.WriteFile(localSeed, []byte(seedContent), 0o644); err != nil {
		t.Fatalf("write local seed: %v", err)
	}
	upload := runLiveFSSetupCommand(t, bin, "--profile", profileName, "fs", "copy-file", "--from-local", localSeed, "--to-remote", remoteRoot+"/README.md")
	upload.wantExitCode(0)

	mount := runTI(t, bin, "--profile", profileName, "fs", "mount-file-system", "--mount-path", mountPath, "--remote-path", remoteRoot, "--driver", "webdav", "--ready-timeout", "30s")
	mount.wantExitCode(0)
	mount.wantStdoutContains(`"status": "mounted"`)
	mount.wantStdoutContains(`"driver": "webdav"`)

	waitLiveLocalFile(t, filepath.Join(mountPath, "README.md"), seedContent, 30*time.Second)

	unmount := runTI(t, bin, "--profile", profileName, "fs", "unmount-file-system", "--mount-path", mountPath)
	unmount.wantExitCode(0)
	unmount.wantStdoutContains(`"status": "unmounted"`)
	unmounted = true

	deleteRoot := runTI(t, bin, "--profile", profileName, "fs", "delete-file", "--path", remoteRoot, "--recursive")
	deleteRoot.wantExitCode(0)
	remoteDeleted = true
}

func TestLiveDBClusterLifecycle(t *testing.T) {
	requireLive(t)

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	releaseAutoCreatedLiveFSResource(t, bin, profileName)
	profile := liveProfile(t)

	suffix := time.Now().UTC().Format("20060102150405")
	clusterName := "ti-e2e-" + suffix
	updatedName := clusterName + "-u"
	var clusterID string
	deleted := false
	defer func() {
		if clusterID == "" || deleted {
			return
		}
		cleanup := runTI(t, bin, "--profile", profileName, "db", "delete-db-cluster", "--db-cluster-id", clusterID, "--wait")
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup delete failed for cluster %s: exit=%d stdout=%s stderr=%s", clusterID, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
		}
	}()

	create := runTIWithInput(
		t,
		bin,
		"",
		[]string{
			"HOME=" + t.TempDir(),
			"TI_REGION_CODE=" + profile.PlacementRegionCode,
			"TIDB_CLOUD_PUBLIC_KEY=" + profile.TiDBCloudPublicKey,
			"TIDB_CLOUD_PRIVATE_KEY=" + profile.TiDBCloudPrivateKey,
		},
		"db", "create-db-cluster",
		"--db-cluster-type", "starter",
		"--db-cluster-name", clusterName,
		"--wait",
	)
	create.wantExitCode(0)
	created := decodeLiveCluster(t, create)
	if created.ID == "" || created.DisplayName != clusterName {
		t.Fatalf("unexpected created cluster: %#v\n%s", created, create.stdout)
	}
	clusterID = created.ID
	defer cleanupLiveSQLCredentials(t, clusterID)

	if created.State != "ACTIVE" {
		t.Fatalf("--wait returned cluster in state %q: %#v", created.State, created)
	}
	describe := runTI(t, bin, "--profile", profileName, "db", "describe-db-cluster", "--db-cluster-id", clusterID, "--view", "FULL")
	describe.wantExitCode(0)
	described := decodeLiveCluster(t, describe)
	if described.ID != clusterID || described.DisplayName != clusterName || described.State != "ACTIVE" {
		t.Fatalf("unexpected described cluster: %#v\n%s", described, describe.stdout)
	}
	if described.ClusterPlan != "" && described.ClusterPlan != "STARTER" {
		t.Fatalf("expected STARTER cluster, got %#v", described)
	}
	if project := strings.TrimSpace(created.Labels["tidb.cloud/project"]); project != "" && described.Labels["tidb.cloud/project"] != project {
		t.Fatalf("server project metadata changed between create and describe: created=%#v described=%#v", created.Labels, described.Labels)
	}
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"db", "update-db-cluster", "--db-cluster-id", clusterID, "--db-cluster-name", updatedName},
		{"db", "delete-db-cluster", "--db-cluster-id", clusterID, "--wait"},
		{"db", "create-db-cluster-branch", "--db-cluster-id", clusterID, "--db-cluster-branch-name", "tdc-e2e-dry-run-branch", "--wait"},
		{"db", "create-db-sql-users", "--db-cluster-id", clusterID},
	}, "remote_mutation")

	prepare := runTI(t, bin, "--profile", profileName, "db", "create-db-sql-users", "--db-cluster-id", clusterID)
	prepare.wantExitCode(0)
	prepare.wantStdoutContains(`"read_only"`)
	prepare.wantStdoutContains(`"read_write"`)
	prepare.wantStdoutContains(`"admin"`)

	prepareAgain := runTI(t, bin, "--profile", profileName, "db", "create-db-sql-users", "--db-cluster-id", clusterID)
	prepareAgain.wantExitCode(0)
	prepareAgain.wantStdoutContains(`"exists"`)

	connectionString := runTI(t, bin, "--profile", profileName, "db", "format-db-connection-string", "--db-cluster-id", clusterID, "--read-write", "--database", "test")
	connectionString.wantExitCode(0)
	connectionString.wantStdoutContains(`"format": "mysql-uri"`)
	connectionString.wantStdoutContains(`"access_mode": "read_write"`)
	connectionString.wantStdoutContains(`"connection_string"`)

	connectionEnv := runTI(t, bin, "--profile", profileName, "db", "format-db-connection-string", "--db-cluster-id", clusterID, "--read-only", "--format", "env")
	connectionEnv.wantExitCode(0)
	connectionEnv.wantStdoutContains("TIDB_HOST=")
	connectionEnv.wantStdoutContains("TIDB_ACCESS_MODE=read_only")
	connectionEnv.wantStdoutNotContains(`"connection_string"`)

	waitLiveSQL(t, bin, profileName, clusterID, nil, "default read-write SQL execution")
	waitLiveSQL(t, bin, profileName, clusterID, []string{"--read-only"}, "read-only SQL execution")
	waitLiveSQL(t, bin, profileName, clusterID, []string{"--admin"}, "admin SQL execution")

	branchName := "ti-e2e-branch-" + suffix
	branchID := ""
	branchDeleted := false
	defer func() {
		if branchID == "" || branchDeleted {
			return
		}
		cleanup := runTI(
			t,
			bin,
			"--profile", profileName,
			"db", "delete-db-cluster-branch",
			"--db-cluster-id", clusterID,
			"--db-cluster-branch-id", branchID,
		)
		if cleanup.exitCode != 0 && cleanup.exitCode != 5 {
			t.Logf("cleanup delete failed for branch %s: exit=%d stdout=%s stderr=%s", branchID, cleanup.exitCode, cleanup.stdout, cleanup.stderr)
		}
	}()

	branchCreate := runTI(
		t,
		bin,
		"--profile", profileName,
		"db", "create-db-cluster-branch",
		"--db-cluster-id", clusterID,
		"--db-cluster-branch-name", branchName,
		"--wait",
	)
	branchCreate.wantExitCode(0)
	createdBranch := decodeLiveBranch(t, branchCreate)
	if createdBranch.ID == "" || createdBranch.DisplayName != branchName {
		t.Fatalf("unexpected created branch: %#v\n%s", createdBranch, branchCreate.stdout)
	}
	branchID = createdBranch.ID
	if createdBranch.State != "ACTIVE" {
		t.Fatalf("--wait returned branch in state %q: %#v", createdBranch.State, createdBranch)
	}
	testLiveMutatingDryRuns(t, bin, profileName, [][]string{
		{"db", "delete-db-cluster-branch", "--db-cluster-id", clusterID, "--db-cluster-branch-id", branchID},
	}, "remote_mutation")

	branches := runTI(t, bin, "--profile", profileName, "db", "list-db-cluster-branches", "--db-cluster-id", clusterID, "--page-size", "100")
	branches.wantExitCode(0)
	branches.wantStdoutContains(`"branches"`)
	branches.wantStdoutContains(branchID)

	branchQuery := runTI(t, bin, "--profile", profileName, "db", "list-db-cluster-branches", "--db-cluster-id", clusterID, "--query", "branches[].id")
	branchQuery.wantExitCode(0)
	branchQuery.wantStdoutContains(branchID)

	branchText := runTI(t, bin, "--profile", profileName, "db", "list-db-cluster-branches", "--db-cluster-id", clusterID, "--output", "text")
	branchText.wantExitCode(0)
	branchText.wantStdoutContains("ID")
	branchText.wantStdoutContains(branchName)

	branchDescribe := runTI(t, bin, "--profile", profileName, "db", "describe-db-cluster-branch", "--db-cluster-id", clusterID, "--db-cluster-branch-id", branchID, "--view", "FULL")
	branchDescribe.wantExitCode(0)
	describedBranch := decodeLiveBranch(t, branchDescribe)
	if describedBranch.ID != branchID || describedBranch.DisplayName != branchName {
		t.Fatalf("unexpected described branch: %#v\n%s", describedBranch, branchDescribe.stdout)
	}

	branchDelete := runTI(
		t,
		bin,
		"--profile", profileName,
		"db", "delete-db-cluster-branch",
		"--db-cluster-id", clusterID,
		"--db-cluster-branch-id", branchID,
	)
	branchDelete.wantExitCode(0)
	deletedBranch := decodeLiveBranch(t, branchDelete)
	if deletedBranch.ID != branchID {
		t.Fatalf("delete response did not reference created branch %s:\n%s", branchID, branchDelete.stdout)
	}
	branchDeleted = true

	waitLiveBranchDeleted(t, bin, profileName, clusterID, branchID, 5*time.Minute)

	update := runTI(
		t,
		bin,
		"--profile", profileName,
		"db", "update-db-cluster",
		"--db-cluster-id", clusterID,
		"--db-cluster-name", updatedName,
	)
	update.wantExitCode(0)
	updated := decodeLiveCluster(t, update)
	if updated.ID != clusterID || updated.DisplayName != updatedName {
		t.Fatalf("unexpected updated cluster: %#v\n%s", updated, update.stdout)
	}
	waitLiveCluster(t, bin, profileName, clusterID, func(cluster liveCluster) bool {
		return cluster.ID == clusterID && cluster.DisplayName == updatedName
	}, 3*time.Minute, "show updated display name")

	remove := runTI(
		t,
		bin,
		"--profile", profileName,
		"db", "delete-db-cluster",
		"--db-cluster-id", clusterID,
		"--wait",
	)
	remove.wantExitCode(0)
	removed := decodeLiveCluster(t, remove)
	if removed.ID != clusterID {
		t.Fatalf("delete response did not reference created cluster %s:\n%s", clusterID, remove.stdout)
	}
	if removed.State != "DELETED" {
		t.Fatalf("--wait returned cluster in state %q: %#v", removed.State, removed)
	}
	deleted = true
}

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("TI_LIVE") != "1" {
		t.Skip("TI_LIVE=1 is required; run make live-e2e")
	}
}

type liveCluster struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	State       string            `json:"state"`
	ClusterPlan string            `json:"cluster_plan"`
	Labels      map[string]string `json:"labels"`
}

type liveBranch struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	ClusterID   string `json:"cluster_id"`
	State       string `json:"state"`
}

type liveVaultToken struct {
	Token   string `json:"token"`
	TokenID string `json:"token_id"`
	GrantID string `json:"grant_id"`
}

type liveGitWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	RootPath    string `json:"root_path"`
}

type liveGitObjectPack struct {
	PackID string `json:"pack_id"`
}

func decodeLiveCluster(t *testing.T, result commandResult) liveCluster {
	t.Helper()
	var cluster liveCluster
	if err := json.Unmarshal([]byte(result.stdout), &cluster); err != nil {
		t.Fatalf("decode cluster output: %v\n%s", err, result.stdout)
	}
	return cluster
}

func decodeLiveBranch(t *testing.T, result commandResult) liveBranch {
	t.Helper()
	var branch liveBranch
	if err := json.Unmarshal([]byte(result.stdout), &branch); err != nil {
		t.Fatalf("decode branch output: %v\n%s", err, result.stdout)
	}
	return branch
}

func decodeLiveVaultToken(t *testing.T, result commandResult) liveVaultToken {
	t.Helper()
	var token liveVaultToken
	if err := json.Unmarshal([]byte(result.stdout), &token); err != nil {
		t.Fatalf("decode vault token output: %v\n%s", err, result.stdout)
	}
	return token
}

func decodeLiveGitWorkspace(t *testing.T, result commandResult) liveGitWorkspace {
	t.Helper()
	var workspace liveGitWorkspace
	if err := json.Unmarshal([]byte(result.stdout), &workspace); err != nil {
		t.Fatalf("decode git workspace output: %v\n%s", err, result.stdout)
	}
	return workspace
}

func decodeLiveGitObjectPack(t *testing.T, result commandResult) liveGitObjectPack {
	t.Helper()
	var pack liveGitObjectPack
	if err := json.Unmarshal([]byte(result.stdout), &pack); err != nil {
		t.Fatalf("decode git object pack output: %v\n%s", err, result.stdout)
	}
	return pack
}

func waitLiveCluster(t *testing.T, bin, profileName, clusterID string, ready func(liveCluster) bool, timeout time.Duration, description string) liveCluster {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last liveCluster
	for {
		describe := runTI(t, bin, "--profile", profileName, "db", "describe-db-cluster", "--db-cluster-id", clusterID, "--view", "FULL")
		describe.wantExitCode(0)
		last = decodeLiveCluster(t, describe)
		if ready(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cluster %s to %s; last=%#v", clusterID, description, last)
		}
		time.Sleep(10 * time.Second)
	}
}

func waitLiveSQL(t *testing.T, bin, profileName, clusterID string, modeArgs []string, description string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	var last commandResult
	for {
		args := []string{"--profile", profileName, "db", "execute-sql-statement", "--db-cluster-id", clusterID}
		args = append(args, modeArgs...)
		args = append(args, "--sql", "select 1")
		last = runTI(t, bin, args...)
		if last.exitCode == 0 {
			last.wantStdoutContains(`"transport": "https"`)
			last.wantStdoutContains(`"row_count": 1`)
			return
		}
		if time.Now().After(deadline) {
			last.fail("timed out waiting for %s; got exit code %d", description, last.exitCode)
		}
		time.Sleep(10 * time.Second)
	}
}

func waitLiveFSResult(t *testing.T, bin string, args []string, want string, timeout time.Duration, description string) commandResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last commandResult
	for {
		last = runTI(t, bin, args...)
		if last.exitCode == 0 && strings.Contains(last.stdout, want) {
			return last
		}
		if time.Now().After(deadline) {
			last.fail("timed out waiting for ti fs to %s", description)
		}
		time.Sleep(5 * time.Second)
	}
}

func waitLiveLocalFile(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("content mismatch: got %q", data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for mounted file %s: %v", path, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func waitLiveRemoteRead(t *testing.T, bin, profileName, remotePath, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last commandResult
	for {
		last = runTI(t, bin, "--profile", profileName, "fs", "read-file", "--path", remotePath)
		if last.exitCode == 0 && last.stdout == want {
			return
		}
		if time.Now().After(deadline) {
			last.fail("timed out waiting for remote file %s to match mounted write", remotePath)
		}
		time.Sleep(1 * time.Second)
	}
}

func waitLiveRemoteReadWithEnv(t *testing.T, bin string, env []string, remotePath, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last commandResult
	for {
		last = runTIWithInput(t, bin, "", env, "fs", "read-file", "--path", remotePath)
		if last.exitCode == 0 && last.stdout == want {
			return
		}
		if time.Now().After(deadline) {
			last.fail("timed out waiting for configuration-free remote file %s to match mounted write", remotePath)
		}
		time.Sleep(1 * time.Second)
	}
}

func liveFSTokenEnv(profile *config.Profile, home string) []string {
	return []string{
		"HOME=" + home,
		"TI_LOGGING=off",
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
		"TI_FS_FILE_SYSTEM_ID=",
		"TI_FS_TOKEN=" + profile.FSAPIKey,
		"TI_REGION_CODE=" + profile.FSPlacementRegionCode,
	}
}

func waitLiveFSInventoryAbsent(t *testing.T, bin string, env []string, fileSystemID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		result := runTIWithInput(t, bin, "", env, "fs", "list-file-systems")
		result.wantExitCode(0)
		var inventory struct {
			FileSystems []struct {
				FileSystemID string `json:"file_system_id"`
			} `json:"file_systems"`
		}
		if err := json.Unmarshal([]byte(result.stdout), &inventory); err != nil {
			t.Fatalf("decode post-delete live FS inventory: %v", err)
		}
		found := false
		for _, resource := range inventory.FileSystems {
			if resource.FileSystemID == fileSystemID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file system %q remained in remote inventory after deletion timeout", fileSystemID)
		}
		time.Sleep(2 * time.Second)
	}
}

func liveFSLocatorEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"TI_LOGGING=off",
		"TI_PROFILE=",
		"TIDB_CLOUD_PUBLIC_KEY=",
		"TIDB_CLOUD_PRIVATE_KEY=",
		"TI_FS_FILE_SYSTEM_ID=",
		"TI_FS_TOKEN=",
		"TI_REGION_CODE=",
	}
}

func waitLiveBranchDeleted(t *testing.T, bin, profileName, clusterID, branchID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		describe := runTI(t, bin, "--profile", profileName, "db", "describe-db-cluster-branch", "--db-cluster-id", clusterID, "--db-cluster-branch-id", branchID)
		switch describe.exitCode {
		case 0:
			branch := decodeLiveBranch(t, describe)
			if branch.ID != branchID {
				t.Fatalf("post-delete read returned a different branch: %#v", branch)
			}
			if branch.State == "DELETED" {
				return
			}
		case 5:
			return
		case 4:
			return
		default:
			describe.fail("post-delete branch read should return deleted branch state, not found, or no longer readable; got exit code %d", describe.exitCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for branch %s to be deleted", branchID)
		}
		time.Sleep(5 * time.Second)
	}
}

func cleanupLiveSQLCredentials(t *testing.T, clusterID string) {
	t.Helper()
	if clusterID == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Logf("cannot determine home directory for SQL credential cleanup: %v", err)
		return
	}
	path := filepath.Join(home, ".ti", "db_users", clusterID)
	if err := os.RemoveAll(path); err != nil {
		t.Logf("cleanup SQL credentials failed for %s: %v", path, err)
	}
}

func ensureLiveFSResource(t *testing.T, bin, profileName string) *config.Profile {
	t.Helper()
	liveFSResourceMu.Lock()
	defer liveFSResourceMu.Unlock()

	profile := liveProfile(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("determine home directory: %v", err)
	}
	if err := fscred.MigrateNameRegistry(home, profile); err != nil {
		t.Fatalf("migrate live fs resource: %v", err)
	}
	requestedID := strings.TrimSpace(os.Getenv("TI_LIVE_FS_ID"))
	list := runTI(t, bin, "--profile", profileName, "fs", "list-file-systems")
	list.wantExitCode(0)
	var inventory struct {
		RegionCode  string `json:"region_code"`
		FileSystems []struct {
			FileSystemID  string `json:"file_system_id"`
			RegionCode    string `json:"region_code"`
			HasLocalToken bool   `json:"has_local_token"`
		} `json:"file_systems"`
	}
	if err := json.Unmarshal([]byte(list.stdout), &inventory); err != nil {
		t.Fatalf("decode live fs inventory: %v", err)
	}
	var unusableResources []string
	for _, resource := range inventory.FileSystems {
		if !resource.HasLocalToken || (requestedID != "" && requestedID != resource.FileSystemID) {
			continue
		}
		selected := resolveLiveFSResourceByID(t, profile, resource.FileSystemID)
		liveFSSelectedID = resource.FileSystemID
		waitLiveFSReady(t, bin, profileName, selected, 10*time.Minute)
		return selected
	}

	for _, resource := range inventory.FileSystems {
		if requestedID != "" && requestedID != resource.FileSystemID {
			continue
		}
		regionCode := resource.RegionCode
		if regionCode == "" {
			regionCode = inventory.RegionCode
		}
		if regionCode == "" {
			regionCode = profile.PlacementRegionCode
		}
		generate := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "generate-file-system-token",
			"--file-system-id", resource.FileSystemID,
			"--token-name", fmt.Sprintf("ti-e2e-session-%d", time.Now().UnixNano()),
			"--ttl", "1h",
			"--store-locally")
		generate.wantExitCode(0)
		var generated struct {
			TokenID string `json:"token_id"`
		}
		if err := json.Unmarshal([]byte(generate.stdout), &generated); err != nil || generated.TokenID == "" {
			t.Fatalf("decode temporary live FS token: %v\n%s", err, generate.stdout)
		}
		probe := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "list-files", "--file-system-id", resource.FileSystemID, "--path", "/")
		if probe.exitCode != 0 {
			cleanup := runTI(t, bin, "--profile", profileName, "--region", regionCode, "fs", "delete-file-system-token",
				"--file-system-id", resource.FileSystemID, "--token-id", generated.TokenID)
			if cleanup.exitCode != 0 {
				cleanup.fail("clean up temporary token for unusable live FS %s", resource.FileSystemID)
			}
			unusableResources = append(unusableResources, fmt.Sprintf("%s: %s", resource.FileSystemID, strings.TrimSpace(probe.stderr)))
			if requestedID != "" {
				t.Fatalf("TI_LIVE_FS_ID %q is not data-plane accessible: %s", requestedID, strings.TrimSpace(probe.stderr))
			}
			continue
		}
		liveFSTokenAutoCreatedID = generated.TokenID
		liveFSTokenAutoFileSystemID = resource.FileSystemID
		liveFSSelectedID = resource.FileSystemID
		selected := resolveLiveFSResourceByID(t, profile, resource.FileSystemID)
		return selected
	}

	if requestedID != "" {
		t.Fatalf("TI_LIVE_FS_ID %q is not remotely visible", requestedID)
	}
	if len(inventory.FileSystems) > 0 {
		t.Fatalf("no remotely visible Filesystem has a working data plane:\n%s", strings.Join(unusableResources, "\n"))
	}
	create := runTI(t, bin, "--profile", profileName, "fs", "create-file-system", "--wait")
	create.wantExitCode(0)
	create.wantStdoutContains(`"credentials_stored": true`)
	create.wantStdoutContains(`"status": "ready"`)
	var created struct {
		FileSystemID string `json:"file_system_id"`
	}
	if err := json.Unmarshal([]byte(create.stdout), &created); err != nil || created.FileSystemID == "" {
		t.Fatalf("decode created live fs resource: %v", err)
	}
	liveFSResourceAutoCreatedID = created.FileSystemID
	liveFSSelectedID = created.FileSystemID
	selected := resolveLiveFSResourceByID(t, profile, created.FileSystemID)
	return selected
}

func waitLiveFSReady(t *testing.T, bin, profileName string, profile *config.Profile, timeout time.Duration) {
	t.Helper()
	client := liveFSClient(t, profile, authz.FSVolumeRead)
	probeLocalPath := filepath.Join(t.TempDir(), "ready.txt")
	if err := os.WriteFile(probeLocalPath, []byte("ti fs live readiness probe\n"), 0o600); err != nil {
		t.Fatalf("write ti fs readiness probe: %v", err)
	}
	probeRemotePath := fmt.Sprintf("/ti-e2e-readiness-%d-%d.txt", os.Getpid(), time.Now().UnixNano())
	defer func() {
		cleanup := runLiveFSSetupCommand(t, bin, "--profile", profileName, "fs", "delete-file", "--file-system-id", profile.FSTenantID, "--path", probeRemotePath)
		if cleanup.exitCode != 0 && !isLiveFSNotFound(cleanup.stderr) {
			t.Logf("cleanup ti fs readiness probe failed: exit=%d stderr=%s", cleanup.exitCode, strings.TrimSpace(cleanup.stderr))
		}
	}()
	deadline := time.Now().Add(timeout)
	var lastStatus apifs.StatusResponse
	var lastErr error
	var lastProbe commandResult
	consecutiveWriteProbes := 0
	for {
		status, err := client.Status(context.Background())
		if err == nil {
			lastStatus = status
			state := strings.ToLower(strings.TrimSpace(status.Status))
			if state == "" || (!strings.Contains(state, "provision") && !strings.Contains(state, "delet")) {
				lastProbe = runTI(t, bin, "--profile", profileName, "fs", "copy-file", "--file-system-id", profile.FSTenantID, "--from-local", probeLocalPath, "--to-remote", probeRemotePath, "--overwrite")
				if lastProbe.exitCode == 0 {
					cleanup := runLiveFSSetupCommand(t, bin, "--profile", profileName, "fs", "delete-file", "--file-system-id", profile.FSTenantID, "--path", probeRemotePath)
					if cleanup.exitCode != 0 && !isLiveFSNotFound(cleanup.stderr) {
						cleanup.fail("delete ti fs readiness probe")
					}
					consecutiveWriteProbes++
					if consecutiveWriteProbes >= 5 {
						return
					}
				} else {
					consecutiveWriteProbes = 0
					if !isLiveFSReadinessError(lastProbe.stderr) {
						lastProbe.fail("probe ti fs data-plane readiness")
					}
				}
			}
		} else {
			lastErr = err
			if !isLiveFSReadinessError(err.Error()) {
				t.Fatalf("check ti fs readiness for profile %q failed: %v", profile.Name, err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for tdc fs resource %q in profile %q to become data-plane ready; last_status=%#v last_error=%v last_probe_stderr=%q", profile.FSTenantID, profile.Name, lastStatus, lastErr, strings.TrimSpace(lastProbe.stderr))
		}
		time.Sleep(5 * time.Second)
	}
}

func isLiveFSReadinessError(stderr string) bool {
	message := strings.ToLower(stderr)
	return strings.Contains(message, "storage backend unavailable") ||
		strings.Contains(message, "http 503") ||
		strings.Contains(message, "provision") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "network connectivity") ||
		strings.Contains(message, ": eof") ||
		strings.Contains(message, "timeout") ||
		strings.Contains(message, "unexpected eof")
}

func runLiveFSSetupCommand(t *testing.T, bin string, args ...string) commandResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		result := runTI(t, bin, args...)
		if result.exitCode == 0 || !isLiveFSReadinessError(result.stderr) || time.Now().After(deadline) {
			return result
		}
		time.Sleep(5 * time.Second)
	}
}

func isLiveFSNotFound(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "not found")
}

func TestIsLiveFSReadinessError(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"ti [ERROR]: fs ls: storage backend unavailable; contact support",
		"ti [ERROR]: fs cp: HTTP 503:",
		"resource is still provisioning",
		"503 Service Unavailable",
		"connection reset by peer",
		"dial tcp: lookup drive9.ai: no such host",
		"API request failed: check network connectivity and try again",
		"status API request failed: EOF",
		"i/o timeout",
	} {
		if !isLiveFSReadinessError(message) {
			t.Fatalf("expected readiness error for %q", message)
		}
	}
	if isLiveFSReadinessError("ti [ERROR]: authentication required") {
		t.Fatal("authentication errors must fail readiness immediately")
	}
}

func cleanupAutoCreatedLiveFSResource() {
	if os.Getenv("TI_LIVE") != "1" {
		return
	}
	bin := os.Getenv("TI_E2E_BIN")
	if bin == "" {
		_, _ = fmt.Fprintln(os.Stderr, "ti live e2e cleanup warning: TI_E2E_BIN is not set; cannot delete auto-created ti fs resource")
		return
	}
	profileName := liveProfileNameFromEnv()
	if liveFSTokenAutoCreatedID != "" && liveFSTokenAutoFileSystemID != "" {
		cmd := exec.Command(
			bin,
			"--profile", profileName,
			"fs", "delete-file-system-token",
			"--file-system-id", liveFSTokenAutoFileSystemID,
			"--token-id", liveFSTokenAutoCreatedID,
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ti live e2e cleanup warning: delete temporary FS token %q failed: %v\n%s", liveFSTokenAutoCreatedID, err, string(output))
		}
	}
	if liveFSResourceAutoCreatedID == "" {
		return
	}
	fileSystemID := liveFSResourceAutoCreatedID
	cmd := exec.Command(
		bin,
		"--profile", profileName,
		"fs", "delete-file-system",
		"--file-system-id", fileSystemID,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "tdc live e2e cleanup warning: delete tdc fs resource %q failed: %v\n%s", fileSystemID, err, string(output))
	}
}

func releaseAutoCreatedLiveFSResource(t *testing.T, bin, profileName string) {
	t.Helper()
	liveFSResourceMu.Lock()
	defer liveFSResourceMu.Unlock()
	if liveFSTokenAutoCreatedID != "" && liveFSTokenAutoFileSystemID != "" {
		result := runTI(
			t,
			bin,
			"--profile", profileName,
			"fs", "delete-file-system-token",
			"--file-system-id", liveFSTokenAutoFileSystemID,
			"--token-id", liveFSTokenAutoCreatedID,
		)
		result.wantExitCode(0)
		liveFSTokenAutoCreatedID = ""
		liveFSTokenAutoFileSystemID = ""
		liveFSSelectedID = ""
	}
	if liveFSResourceAutoCreatedID == "" {
		return
	}
	fileSystemID := liveFSResourceAutoCreatedID
	result := runTI(
		t,
		bin,
		"--profile", profileName,
		"fs", "delete-file-system",
		"--file-system-id", fileSystemID,
	)
	result.wantExitCode(0)
	liveFSResourceAutoCreatedID = ""
	liveFSSelectedID = ""
}

func liveProfileName(t *testing.T) string {
	t.Helper()
	profileName := liveProfileNameFromEnv()
	if profileName != defaultLiveProfile {
		t.Fatalf("live e2e must use profile %q, got %q", defaultLiveProfile, profileName)
	}
	return profileName
}

func liveProfileNameFromEnv() string {
	profileName := os.Getenv("TI_PROFILE")
	if profileName == "" {
		profileName = defaultLiveProfile
	}
	return profileName
}

func liveProfile(t *testing.T) *config.Profile {
	t.Helper()
	liveProfileConfigureMu.Lock()
	defer liveProfileConfigureMu.Unlock()
	profileName := liveProfileName(t)
	load := func() (*config.Profile, error) {
		return auth.LoadProfile(context.Background(), config.LoadOptions{
			Profile:         profileName,
			ProfileExplicit: true,
		})
	}
	profile, err := load()
	if err != nil {
		t.Fatalf("load live e2e profile %q: %v\nconfigure it with: bin/ti configure --profile %s", profileName, err, profileName)
	}
	return profile
}

func liveDigestClient(t *testing.T, profile *config.Profile, endpoint endpoints.Endpoint, permission authz.Permission) *api.Client {
	t.Helper()
	client, err := api.NewDigestClient(profile, endpoint, permission, api.Options{
		Timeout:    30 * time.Second,
		MaxRetries: 1,
		UserAgent:  "ti-live-e2e",
	})
	if err != nil {
		t.Fatalf("create live API client for %s: %v", endpoint.Service, err)
	}
	return client
}

func liveFSClient(t *testing.T, profile *config.Profile, permission authz.Permission) *apifs.Client {
	t.Helper()
	provider := profile.FSCloudProvider
	regionCode := profile.FSRegionCode
	if provider == "" {
		provider = profile.CloudProvider
	}
	if regionCode == "" {
		regionCode = profile.RegionCode
	}
	endpoint, err := endpoints.NewResolver().ResolveFS(provider, regionCode)
	if err != nil {
		t.Fatalf("resolve live ti fs endpoint: %v", err)
	}
	client, err := api.NewBearerClient(profile.Name, profile.FSAPIKey, endpoint, permission, api.Options{
		Timeout:    45 * time.Second,
		MaxRetries: 1,
		UserAgent:  "ti-live-e2e",
	})
	if err != nil {
		t.Fatalf("create live ti fs client: %v", err)
	}
	return apifs.New(client)
}

func initiateLiveUpload(t *testing.T, client *apifs.Client, remotePath, localPath string) string {
	t.Helper()
	file, err := os.Open(localPath)
	if err != nil {
		t.Fatalf("open resume upload source: %v", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat resume upload source: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	plan, err := client.InitiateUploadFromReader(ctx, remotePath, file, info.Size(), apifs.UploadFileOptions{})
	if err != nil {
		t.Fatalf("initiate live upload for resume: %v", err)
	}
	if plan.UploadID == "" {
		t.Fatalf("live upload plan missing upload id: %#v", plan)
	}
	return plan.UploadID
}

func liveGETJSON(t *testing.T, client *api.Client, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	req, err := client.NewRequest(ctx, "GET", path, nil)
	if err != nil {
		t.Fatalf("build live request %s: %v", path, err)
	}

	var payload any
	if err := client.DoJSON(req, &payload); err != nil {
		t.Fatalf("live GET %s failed: %v", path, err)
	}
	if payload == nil {
		t.Fatalf("live GET %s returned empty JSON payload", path)
	}
	switch typed := payload.(type) {
	case map[string]any:
		if len(typed) == 0 {
			t.Fatalf("live GET %s returned empty JSON object", path)
		}
	case []any:
		if len(typed) == 0 {
			t.Fatalf("live GET %s returned empty JSON array", path)
		}
	default:
		if strings.TrimSpace(path) == "" {
			t.Fatalf("live GET returned unexpected scalar payload for empty path")
		}
	}
}
