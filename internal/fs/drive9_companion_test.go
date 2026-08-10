package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

type fakeDrive9Call struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func TestDrive9CreateFileSystemStoresRegistryCredentialsAndUsesCanonicalRegion(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("DRIVE9_API_KEY", "ambient-drive9-key")
	profile := testProfile()

	result, err := testCompanionService(home, companion).CreateFileSystem(context.Background(), CreateFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
	})
	if err != nil {
		t.Fatalf("CreateFileSystem failed: %v", err)
	}
	if result.FileSystemName != "workspace" || result.TenantID != "tenant-1" || result.RegionCode != "aws-us-east-1" || result.FSToken != "fs-secret" || !result.CredentialsStored {
		t.Fatalf("unexpected result: %#v", result)
	}

	configDoc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if got := configDoc["stage"]; got.FSResourceName != "" || got.FSTenantID != "" || got.FSRegionCode != "" {
		t.Fatalf("unexpected fs config: %#v", got)
	}
	credentialsDoc, err := store.ReadCredentials(home)
	if err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	if got := credentialsDoc["stage"]; got.FSAPIKey != "" {
		t.Fatalf("fs api key must not be stored flat under profile: %#v", got)
	}
	resource, err := fscred.Get(home, "stage", "workspace")
	if err != nil {
		t.Fatalf("Get registry resource failed: %v", err)
	}
	if resource.TenantID != "tenant-1" || resource.RegionCode != "aws-us-east-1" || resource.APIKey != "fs-secret" {
		t.Fatalf("unexpected registry resource: %#v", resource)
	}

	createCall := requireFakeDrive9Call(t, recordPath, "create")
	wantArgs := []string{"create", "--json", "--name", "workspace", "--region-code", "aws-us-east-1"}
	if fmt.Sprint(createCall.Args) != fmt.Sprint(wantArgs) {
		t.Fatalf("create args = %#v, want %#v", createCall.Args, wantArgs)
	}
	if createCall.Env["DRIVE9_REGION_CODE"] != "aws-us-east-1" || createCall.Env["DRIVE9_SERVER"] != "https://fs.test" {
		t.Fatalf("unexpected region/server env: %#v", createCall.Env)
	}
	if createCall.Env["DRIVE9_PUBLIC_KEY"] != "public" || createCall.Env["DRIVE9_PRIVATE_KEY"] != "private" {
		t.Fatalf("missing TiDB Cloud keys in create env: %#v", createCall.Env)
	}
	if _, ok := createCall.Env["DRIVE9_API_KEY"]; ok {
		t.Fatalf("create should not pass an fs api key, env=%#v", createCall.Env)
	}
	wantHome, err := fscred.CompanionHome(home, "stage", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if createCall.Env["HOME"] != wantHome {
		t.Fatalf("create HOME = %q, want %q", createCall.Env["HOME"], wantHome)
	}
}

func TestDrive9CreateFileSystemWaitsUntilReady(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_STAT_FAILURE_SEQUENCE", filepath.Join(t.TempDir(), "stat-attempted"))

	service := testCompanionService(home, companion)
	service.FSReadyWaitTimeout = time.Second
	service.FSReadyWaitPollInterval = time.Millisecond
	result, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{
		Profile:        testProfile(),
		FileSystemName: "workspace",
		WaitUntilReady: true,
	})
	if err != nil {
		t.Fatalf("CreateFileSystem failed: %v", err)
	}
	if result.Status != "ready" || !result.CredentialsStored {
		t.Fatalf("unexpected result: %#v", result)
	}
	statCalls := 0
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "fs" && call.Args[1] == "stat" {
			statCalls++
			if call.Env["DRIVE9_API_KEY"] != "fs-secret" {
				t.Fatalf("readiness stat used wrong credentials: %#v", call.Env)
			}
		}
	}
	if statCalls != 2 {
		t.Fatalf("readiness stat calls = %d, want 2", statCalls)
	}
}

func TestDrive9CreateFileSystemReadyTimeoutPreservesCredentials(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_STAT_ALWAYS_FAIL", "1")

	service := testCompanionService(home, companion)
	service.FSReadyWaitTimeout = 10 * time.Millisecond
	service.FSReadyWaitPollInterval = time.Millisecond
	_, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{
		Profile:        testProfile(),
		FileSystemName: "workspace",
		WaitUntilReady: true,
	})
	if apperr.CodeFor(err) != "fs.ready_wait_timeout" {
		t.Fatalf("unexpected error: %v", err)
	}
	resource, getErr := fscred.Get(home, "stage", "workspace")
	if getErr != nil || resource.APIKey != "fs-secret" {
		t.Fatalf("readiness timeout removed stored credentials: resource=%#v err=%v", resource, getErr)
	}
}

func TestDrive9CreateFileSystemFromEnvironmentProfileStoresDefaultProfile(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	profile := &config.Profile{
		Name:                config.DefaultProfile,
		Source:              "env",
		PlacementRegionCode: "aws-us-east-1",
		CloudProvider:       "aws",
		RegionCode:          "us-east-1",
		TiDBCloudPublicKey:  "env-public",
		TiDBCloudPrivateKey: "env-private",
	}

	if _, err := testCompanionService(home, companion).CreateFileSystem(context.Background(), CreateFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
	}); err != nil {
		t.Fatalf("CreateFileSystem failed: %v", err)
	}

	configDoc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if got := configDoc[config.DefaultProfile]; got.FSResourceName != "" || got.FSTenantID != "" {
		t.Fatalf("expected fs config under default profile, got %#v", got)
	}
	if _, ok := configDoc["env"]; ok {
		t.Fatalf("did not expect generated [env] config section: %#v", configDoc["env"])
	}
	credentialsDoc, err := store.ReadCredentials(home)
	if err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	if got := credentialsDoc[config.DefaultProfile]; got.FSAPIKey != "" {
		t.Fatalf("did not expect flat fs api key under default profile, got %#v", got)
	}
	if _, ok := credentialsDoc["env"]; ok {
		t.Fatalf("did not expect generated [env] credentials section: %#v", credentialsDoc["env"])
	}
}

func TestDrive9CreateSecondFileSystemUsesIndependentCompanionHome(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	profile := testProfile()
	service := testCompanionService(home, companion)
	if _, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: profile, FileSystemName: "workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: profile, FileSystemName: "scratch"}); err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	repeated, err := service.CreateFileSystem(context.Background(), CreateFileSystemOptions{Profile: profile, FileSystemName: "scratch"})
	if err != nil {
		t.Fatalf("repeat create scratch: %v", err)
	}
	if repeated.Status != "exists" || repeated.FSToken != "fs-secret" || !repeated.CredentialsStored {
		t.Fatalf("unexpected repeated create result: %#v", repeated)
	}
	resources, err := fscred.List(home, profile.Name)
	if err != nil || len(resources) != 2 {
		t.Fatalf("resources=%#v err=%v", resources, err)
	}
	calls := readFakeDrive9Calls(t, recordPath)
	homes := map[string]bool{}
	for _, call := range calls {
		if len(call.Args) > 0 && call.Args[0] == "create" {
			homes[call.Env["HOME"]] = true
		}
	}
	if len(homes) != 2 {
		t.Fatalf("expected two resource-scoped companion homes, got %#v", homes)
	}
	createCalls := 0
	for _, call := range calls {
		if len(call.Args) > 0 && call.Args[0] == "create" {
			createCalls++
		}
	}
	if createCalls != 2 {
		t.Fatalf("idempotent create invoked Drive9 %d times, want 2 total calls", createCalls)
	}
}

func TestDrive9DeleteFileSystemDeletesOnlySelectedRegistryResource(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	profile := dataProfile()
	if err := store.WriteProfile(home, profile.Name, store.ConfigProfile{RegionCode: profile.PlacementRegionCode}, store.CredentialsProfile{TiDBCloudPublicKey: profile.TiDBCloudPublicKey, TiDBCloudPrivateKey: profile.TiDBCloudPrivateKey}); err != nil {
		t.Fatal(err)
	}
	if err := fscred.Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "fs-secret"); err != nil {
		t.Fatal(err)
	}
	if err := fscred.Store(home, profile, "scratch", "tenant-2", "aws", "aws-us-east-1", "fs-secret-2"); err != nil {
		t.Fatal(err)
	}
	result, err := testCompanionService(home, companion).DeleteFileSystem(context.Background(), DeleteFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
	})
	if err != nil {
		t.Fatalf("DeleteFileSystem failed: %v", err)
	}
	if !result.CredentialsRemoved || result.Status != "deleting" || result.RemoteDeletionState != "deleting" {
		t.Fatalf("unexpected delete result: %#v", result)
	}
	deleteCall := requireFakeDrive9Call(t, recordPath, "delete")
	if fmt.Sprint(deleteCall.Args) != fmt.Sprint([]string{"delete", "--json", "--yes"}) {
		t.Fatalf("delete args = %#v", deleteCall.Args)
	}
	if deleteCall.Env["DRIVE9_API_KEY"] != "fs-secret" || deleteCall.Env["DRIVE9_PUBLIC_KEY"] != "public" || deleteCall.Env["DRIVE9_PRIVATE_KEY"] != "private" {
		t.Fatalf("missing delete env: %#v", deleteCall.Env)
	}

	configDoc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if got := configDoc["stage"]; got.FSResourceName != "" || got.FSTenantID != "" || got.FSRegionCode != "" {
		t.Fatalf("unexpected config after delete: %#v", got)
	}
	credentialsDoc, err := store.ReadCredentials(home)
	if err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	if got := credentialsDoc["stage"]; got.FSAPIKey != "" || got.TiDBCloudPublicKey != "public" {
		t.Fatalf("unexpected credentials after delete: %#v", got)
	}
	if _, err := fscred.Get(home, "stage", "workspace"); apperr.CodeFor(err) != "fs.resource_not_found" {
		t.Fatalf("deleted resource still exists: %v", err)
	}
	if resource, err := fscred.Get(home, "stage", "scratch"); err != nil || resource.APIKey != "fs-secret-2" {
		t.Fatalf("unrelated resource was changed: resource=%#v err=%v", resource, err)
	}
}

func TestDrive9CheckFileSystemUsesSelectedResource(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)

	profile := dataProfile()
	profile.TiDBCloudPublicKey = ""
	profile.TiDBCloudPrivateKey = ""
	result, err := testCompanionService(t.TempDir(), companion).CheckFileSystem(context.Background(), CheckFileSystemOptions{Profile: profile})
	if err != nil {
		t.Fatalf("CheckFileSystem failed: %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("expected passed check, got %#v", result)
	}
	if !hasCheck(result.Checks, "remote_status", "passed") {
		t.Fatalf("expected passed remote status check: %#v", result.Checks)
	}
	found := false
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "fs" && call.Args[1] == "stat" {
			found = true
			if call.Env["DRIVE9_API_KEY"] != "fs-secret" {
				t.Fatalf("remote stat used wrong api key: %#v", call.Env)
			}
		}
	}
	if !found {
		t.Fatal("expected remote stat call")
	}
}

func TestDrive9DataPlaneCommandsTranslateToCompanion(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	localFile := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(localFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := testCompanionService(home, companion)

	result, err := service.CopyFile(context.Background(), CopyFileOptions{
		Profile:   dataProfile(),
		FromLocal: localFile,
		ToRemote:  "/workspace/README.md",
		Overwrite: true,
		Tags:      map[string]string{"owner": "agent"},
	})
	if err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}
	if result.SourcePath != localFile || result.TargetPath != "/workspace/README.md" || result.Status != "copied" {
		t.Fatalf("unexpected copy result: %#v", result)
	}
	data, err := service.ReadFile(context.Background(), ReadFileOptions{Profile: dataProfile(), Path: "/workspace/README.md"})
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "file bytes" {
		t.Fatalf("read data = %q", data)
	}

	cpCall := requireFakeDrive9Call(t, recordPath, "fs", "cp")
	if !containsArg(cpCall.Args, drive9RemoteMust("/workspace/README.md")) || !containsArg(cpCall.Args, "--tag") {
		t.Fatalf("copy args missing remote path/tag: %#v", cpCall.Args)
	}
	if cpCall.Env["DRIVE9_API_KEY"] != "fs-secret" {
		t.Fatalf("copy did not receive fs api key: %#v", cpCall.Env)
	}
	if _, ok := cpCall.Env["DRIVE9_PUBLIC_KEY"]; ok {
		t.Fatalf("data-plane copy should not pass TiDB Cloud public key: %#v", cpCall.Env)
	}
	catCall := requireFakeDrive9Call(t, recordPath, "fs", "cat")
	if fmt.Sprint(catCall.Args) != fmt.Sprint([]string{"fs", "cat", drive9RemoteMust("/workspace/README.md")}) {
		t.Fatalf("cat args = %#v", catCall.Args)
	}
}

func TestDrive9CopyDoesNotTreatNotFoundAfterTransientFailureAsSuccess(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	sequencePath := filepath.Join(t.TempDir(), "copy-attempted")
	t.Setenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE", sequencePath)

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:   dataProfile(),
		FromLocal: filepath.Join(t.TempDir(), "input.txt"),
		ToRemote:  "/workspace/input.txt",
		Overwrite: true,
	})
	if err == nil {
		t.Fatal("copy should fail when a transient error is followed by not found")
	}
	if !isDrive9NotFound(err) {
		t.Fatalf("copy error = %v, want final not-found error", err)
	}
}

func TestDrive9CopyDoesNotRetryNonReplayableStreamsOrAppend(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CopyFileOptions
	}{
		{name: "stdin", opts: CopyFileOptions{FromStdin: true, ToRemote: "/workspace/stdin.txt"}},
		{name: "stdout", opts: CopyFileOptions{FromRemote: "/workspace/stdout.txt", ToStdout: true}},
		{name: "append", opts: CopyFileOptions{FromLocal: filepath.Join(t.TempDir(), "append.txt"), ToRemote: "/workspace/append.txt", Append: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			t.Setenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE", filepath.Join(t.TempDir(), "copy-attempted"))
			tc.opts.Profile = dataProfile()

			if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), tc.opts); err == nil {
				t.Fatal("copy should return the companion error")
			}
			calls := readFakeDrive9Calls(t, recordPath)
			cpCalls := 0
			for _, call := range calls {
				if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
					cpCalls++
				}
			}
			if cpCalls != 1 {
				t.Fatalf("fs cp calls = %d, want exactly one for non-replayable operation", cpCalls)
			}
		})
	}
}

func TestDrive9MissingCompanionIsActionable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Service{CompanionPath: filepath.Join(t.TempDir(), "missing-ti-drive9")}.ReadFile(context.Background(), ReadFileOptions{
		Profile: dataProfile(),
		Path:    "/workspace/README.md",
	})
	if err == nil {
		t.Fatal("expected missing companion error")
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "ti fs requires the Drive9 companion binary") {
		t.Fatalf("unexpected error: %q", message)
	}
}

func TestDrive9EndpointFailurePreventsCompanionExecution(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	resolver := endpoints.Resolver{FSManifest: &endpoints.FSRegionManifest{Regions: []endpoints.FSRegionManifestEntry{
		{
			RegionCode:    "aws-us-west-2",
			Mode:          endpoints.DefaultFSMode,
			ServerURL:     "https://fs-west.test",
			CloudProvider: "aws",
			TiDBRegion:    "us-west-2",
		},
	}}}
	_, err := (Service{HomeDir: t.TempDir(), CompanionPath: companion, Resolver: resolver}).ReadFile(context.Background(), ReadFileOptions{
		Profile: dataProfile(),
		Path:    "/workspace/README.md",
	})
	if apperr.CodeFor(err) != "api.fs_endpoint_unsupported" {
		t.Fatalf("expected unsupported endpoint error, got %v", err)
	}
	if data, readErr := os.ReadFile(recordPath); readErr == nil && len(data) != 0 {
		t.Fatalf("endpoint failure invoked Drive9: %s", data)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
}

func TestDrive9MountLocatorRoutesDrainAndUnmountWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	mountPath := filepath.Join(t.TempDir(), "workspace")
	profile := dataProfile()

	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
	}); err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	locator, locatorPath, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read mount locator: %v", err)
	}
	if locator.FileSystemName != "workspace" || locator.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected locator: %#v", locator)
	}
	data, err := os.ReadFile(locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), profile.FSAPIKey) {
		t.Fatalf("mount locator leaked FS token: %s", data)
	}

	localProfile := &config.Profile{Name: "default", HomeDir: home}
	if _, err := service.DrainFileSystem(context.Background(), DrainFileSystemOptions{
		Profile:   localProfile,
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err != nil {
		t.Fatalf("DrainFileSystem failed: %v", err)
	}
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   localProfile,
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err != nil {
		t.Fatalf("UnmountFileSystem failed: %v", err)
	}
	if _, _, err := mountlocator.Read(home, mountPath); !os.IsNotExist(err) {
		t.Fatalf("locator was not removed after unmount: %v", err)
	}

	for _, prefix := range [][]string{{"mount"}, {"mount", "drain"}, {"umount"}} {
		call := requireFakeDrive9Call(t, recordPath, prefix...)
		if call.Env["HOME"] != locator.CompanionHome {
			t.Fatalf("%v used HOME %q, want %q", prefix, call.Env["HOME"], locator.CompanionHome)
		}
	}
}

func TestDrive9FailedUnmountPreservesMountLocator(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	mountPath := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
	}); err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}

	service.CompanionPath = filepath.Join(t.TempDir(), "missing-ti-drive9")
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   &config.Profile{Name: "default", HomeDir: home},
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err == nil {
		t.Fatal("expected unmount failure")
	}
	if _, _, err := mountlocator.Read(home, mountPath); err != nil {
		t.Fatalf("failed unmount removed mount locator: %v", err)
	}
}

func TestDryRunCreateFileSystemUsesRedactedProvisionShape(t *testing.T) {
	profile := testProfile()
	result, err := Service{Resolver: supportedFSManifestResolver("https://fs.test")}.DryRunCreateFileSystem(context.Background(), "ti fs create-file-system", CreateFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
		WaitUntilReady: true,
	})
	if err != nil {
		t.Fatalf("DryRunCreateFileSystem failed: %v", err)
	}
	if result.Operation != "create_file_system" || result.Request.Path != "/v1/provision" {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	bodyBytes, err := json.Marshal(result.Request.Body)
	if err != nil {
		t.Fatalf("marshal dry-run body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode dry-run body: %v", err)
	}
	if body["public_key"] != "[configured]" || body["private_key"] != "[redacted]" {
		t.Fatalf("dry-run leaked credentials: %#v", body)
	}
	if _, ok := body["tidbcloud_spending_limit"]; ok {
		t.Fatalf("dry-run should not include spending limit: %#v", body)
	}
	if !hasDryRunCheck(result.Checks, "endpoint_selection", "passed") {
		t.Fatalf("expected endpoint dry-run check: %#v", result.Checks)
	}
	if !hasDryRunCheck(result.Checks, "post_create_wait", "passed") {
		t.Fatalf("expected readiness wait dry-run check: %#v", result.Checks)
	}
}

func TestDryRunDeleteFileSystemReportsRegistryFiles(t *testing.T) {
	home := t.TempDir()
	profile := dataProfile()
	if err := fscred.Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "fs-secret"); err != nil {
		t.Fatal(err)
	}
	result, err := (Service{HomeDir: home, Resolver: supportedFSManifestResolver("https://fs.test")}).DryRunDeleteFileSystem(context.Background(), "ti fs delete-file-system", DeleteFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
	})
	if err != nil {
		t.Fatalf("DryRunDeleteFileSystem failed: %v", err)
	}
	paths, err := fscred.Paths(home, profile.Name, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "local_resource_registry" {
			found = strings.Contains(check.Message, paths.Config) && strings.Contains(check.Message, paths.Credentials)
		}
	}
	if !found {
		t.Fatalf("dry-run did not report registry files: %#v", result.Checks)
	}
	if _, err := fscred.Get(home, profile.Name, "workspace"); err != nil {
		t.Fatalf("dry-run removed registry resource: %v", err)
	}
}

func TestDrive9VaultHelpers(t *testing.T) {
	scopes, err := drive9VaultGrantScopes([]string{"db-prod", "db-prod/DB_URL", "/n/vault/canonical/TOKEN"})
	if err != nil {
		t.Fatalf("drive9VaultGrantScopes failed: %v", err)
	}
	want := []string{"db-prod", "db-prod/DB_URL", "canonical/TOKEN"}
	if fmt.Sprint(scopes) != fmt.Sprint(want) {
		t.Fatalf("scopes = %#v, want %#v", scopes, want)
	}
	if !isTransientDrive9Error(fmt.Errorf(`vault rm: Delete "https://example/v1/vault/secrets/x": EOF`)) {
		t.Fatal("EOF should be treated as transient")
	}
	if isTransientDrive9Error(fmt.Errorf("vault rm: not found")) {
		t.Fatal("not found should not be treated as transient")
	}
}

func buildFakeDrive9(t *testing.T) (binPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "calls.jsonl")
	sourcePath := filepath.Join(dir, "fake_drive9.go")
	source := `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type callRecord struct {
	Args []string          ` + "`json:\"args\"`" + `
	Env  map[string]string ` + "`json:\"env\"`" + `
}

func main() {
	args := os.Args[1:]
	record(args)
	if len(args) > 0 && args[0] == "--help" {
		fmt.Println("fake drive9 help")
		return
	}
	if len(args) == 0 {
		return
	}
	switch {
	case args[0] == "create":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"tenant_id":      "tenant-1",
			"api_key":        "fs-secret",
			"status":         "active",
			"cloud_provider": "aws",
			"region_code":    os.Getenv("DRIVE9_REGION_CODE"),
			"server":         os.Getenv("DRIVE9_SERVER"),
		})
	case args[0] == "delete":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "deleting"})
	case len(args) >= 2 && args[0] == "fs" && args[1] == "cat":
		fmt.Fprint(os.Stdout, "file bytes")
	case len(args) >= 2 && args[0] == "fs" && args[1] == "cp" && os.Getenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE") != "":
		sequencePath := os.Getenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE")
		if _, err := os.Stat(sequencePath); os.IsNotExist(err) {
			_ = os.WriteFile(sequencePath, []byte("attempted"), 0600)
			fmt.Fprintln(os.Stderr, "fs cp: unexpected EOF")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "fs cp: remote resource not found")
		os.Exit(1)
	case len(args) >= 2 && args[0] == "fs" && args[1] == "stat":
		if os.Getenv("TI_FAKE_DRIVE9_STAT_ALWAYS_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "fs stat: storage backend unavailable; resource is still provisioning")
			os.Exit(1)
		}
		if sequencePath := os.Getenv("TI_FAKE_DRIVE9_STAT_FAILURE_SEQUENCE"); sequencePath != "" {
			if _, err := os.Stat(sequencePath); os.IsNotExist(err) {
				_ = os.WriteFile(sequencePath, []byte("attempted"), 0600)
				fmt.Fprintln(os.Stderr, "fs stat: storage backend unavailable; resource is still provisioning")
				os.Exit(1)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"path": ":/", "size": 12, "isdir": false, "revision": 3})
	default:
		return
	}
}

func record(args []string) {
	path := os.Getenv("TI_FAKE_DRIVE9_RECORD")
	if path == "" {
		return
	}
	env := map[string]string{}
	for _, key := range []string{"DRIVE9_API_KEY", "DRIVE9_SERVER", "DRIVE9_REGION_CODE", "DRIVE9_PUBLIC_KEY", "DRIVE9_PRIVATE_KEY", "DRIVE9_VAULT_TOKEN", "HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(callRecord{Args: args, Env: env})
}
`
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write fake companion source: %v", err)
	}
	binPath = filepath.Join(dir, "ti-drive9")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binPath, sourcePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake companion: %v\n%s", err, output)
	}
	return binPath, recordPath
}

func readFakeDrive9Calls(t *testing.T, recordPath string) []fakeDrive9Call {
	t.Helper()
	file, err := os.Open(recordPath)
	if err != nil {
		t.Fatalf("open fake companion record: %v", err)
	}
	defer file.Close()
	var calls []fakeDrive9Call
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call fakeDrive9Call
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			t.Fatalf("decode fake companion record %q: %v", scanner.Text(), err)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fake companion record: %v", err)
	}
	return calls
}

func requireFakeDrive9Call(t *testing.T, recordPath string, prefix ...string) fakeDrive9Call {
	t.Helper()
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, prefix) {
			return call
		}
	}
	t.Fatalf("missing fake companion call with prefix %#v", prefix)
	return fakeDrive9Call{}
}

func hasArgPrefix(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func testProfile() *config.Profile {
	return &config.Profile{
		Name:                "stage",
		PlacementRegionCode: "aws-us-east-1",
		CloudProvider:       "aws",
		RegionCode:          "us-east-1",
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
	}
}

func dataProfile() *config.Profile {
	profile := testProfile()
	profile.FSResourceName = "workspace"
	profile.FSTenantID = "tenant-1"
	profile.FSCloudProvider = "aws"
	profile.FSRegionCode = "us-east-1"
	profile.FSPlacementRegionCode = "aws-us-east-1"
	profile.FSAPIKey = "fs-secret"
	return profile
}

func testCompanionService(home, companion string) Service {
	return Service{
		HomeDir:       home,
		CompanionPath: companion,
		Resolver:      supportedFSManifestResolver("https://fs.test"),
	}
}

func supportedFSManifestResolver(baseURL string) endpoints.Resolver {
	return endpoints.Resolver{
		FSManifest: &endpoints.FSRegionManifest{
			Regions: []endpoints.FSRegionManifestEntry{
				{
					RegionCode:    "aws-us-east-1",
					Mode:          endpoints.DefaultFSMode,
					ServerURL:     baseURL,
					CloudProvider: "aws",
					TiDBRegion:    "us-east-1",
				},
			},
		},
	}
}

func TestScrubTICredEnvRemovesCanonicalLegacyAndCompanionSecrets(t *testing.T) {
	base := []string{
		"SAFE=value",
		"TIDB_CLOUD_PUBLIC_KEY=canonical-public",
		"TIDB_CLOUD_PRIVATE_KEY=canonical-private",
		"TI_FS_TOKEN=canonical-fs",
		"TI_VAULT_TOKEN=canonical-vault",
		"TI_FS_API_KEY=internal-fs",
		"TDC_PUBLIC_KEY=legacy-public",
		"TDC_PRIVATE_KEY=legacy-private",
		"TDC_FS_TOKEN=legacy-fs",
		"TDC_VAULT_TOKEN=legacy-vault",
		"DRIVE9_PUBLIC_KEY=companion-public",
		"DRIVE9_PRIVATE_KEY=companion-private",
		"DRIVE9_API_KEY=companion-fs",
		"DRIVE9_VAULT_TOKEN=companion-vault",
	}
	got := scrubTICredEnv(base)
	if len(got) != 1 || got[0] != "SAFE=value" {
		t.Fatalf("credential environment was not scrubbed: %#v", got)
	}
}

func hasCheck(checks []Check, name, status string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func hasDryRunCheck(checks []dryrun.Check, name, status string) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}
