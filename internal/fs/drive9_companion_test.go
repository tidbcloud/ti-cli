package fs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

func TestImportFileSystemTokenValidatesStatusAndStoresCredential(t *testing.T) {
	token := fsTestToken(t, "tenant-import")
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_EXPECT_API_KEY", token)
	profile := testProfile()
	profile.HomeDir = home
	service := testCompanionService(home, companion)
	result, err := service.ImportFileSystemToken(context.Background(), ImportFileSystemTokenOptions{Profile: profile, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if result.FileSystemID != "tenant-import" || !result.CredentialsStored || result.Status != "imported" {
		t.Fatalf("result = %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "tenant-import")
	if err != nil || credential.APIKey != token {
		t.Fatalf("credential=%#v err=%v", credential, err)
	}
	validationCall := requireFakeDrive9Call(t, recordPath, "fs", "stat")
	if validationCall.Env["DRIVE9_API_KEY"] != token {
		t.Fatalf("token validation used wrong credential: %#v", validationCall.Env)
	}
	if strings.HasPrefix(validationCall.Env["HOME"], filepath.Join(home, store.TIDirName)) {
		t.Fatalf("token validation persisted companion state under the ti home: %#v", validationCall.Env)
	}
	if _, err := os.Stat(validationCall.Env["HOME"]); !os.IsNotExist(err) {
		t.Fatalf("temporary token validation HOME was not removed: %q, err=%v", validationCall.Env["HOME"], err)
	}
}

func TestImportFileSystemTokenRejectsRemoteValidationFailureWithoutWriting(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_EXPECT_API_KEY", fsTestToken(t, "tenant-other"))
	profile := testProfile()
	profile.HomeDir = home
	_, err := testCompanionService(home, companion).ImportFileSystemToken(context.Background(), ImportFileSystemTokenOptions{
		Profile: profile,
		Token:   fsTestToken(t, "tenant-rejected"),
	})
	if err == nil {
		t.Fatal("import should reject a token refused by Drive9")
	}
	if _, getErr := fscred.GetCredential(home, profile.Name, "tenant-rejected"); apperr.CodeFor(getErr) != "fs.credential_not_found" {
		t.Fatalf("rejected import wrote credentials: %v", getErr)
	}
}

func TestImportFileSystemTokenRejectsAssertedIDMismatchBeforeRemoteValidation(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	profile := testProfile()
	profile.HomeDir = home
	_, err := testCompanionService(home, companion).ImportFileSystemToken(context.Background(), ImportFileSystemTokenOptions{
		Profile:      profile,
		FileSystemID: "tenant-other",
		Token:        fsTestToken(t, "tenant-import"),
	})
	if apperr.CodeFor(err) != "fs.token_file_system_mismatch" {
		t.Fatalf("error = %v, want fs.token_file_system_mismatch", err)
	}
	if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("ID mismatch invoked Drive9: %v", statErr)
	}
	if _, getErr := fscred.GetCredential(home, profile.Name, "tenant-import"); apperr.CodeFor(getErr) != "fs.credential_not_found" {
		t.Fatalf("ID mismatch wrote credentials: %v", getErr)
	}
}

func TestImportFileSystemTokenIsIdempotentWithoutRewritingCredential(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	token := fsTestToken(t, "tenant-import")
	t.Setenv("TI_FAKE_DRIVE9_EXPECT_API_KEY", token)
	profile := testProfile()
	profile.HomeDir = home
	service := testCompanionService(home, companion)
	opts := ImportFileSystemTokenOptions{Profile: profile, Token: token}
	if _, err := service.ImportFileSystemToken(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	paths, err := fscred.CredentialPath(home, profile.Name, "tenant-import")
	if err != nil {
		t.Fatal(err)
	}
	wantModTime := time.Unix(1, 0)
	if err := os.Chtimes(paths.Credentials, wantModTime, wantModTime); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportFileSystemToken(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(wantModTime) {
		t.Fatalf("idempotent import rewrote credentials: modtime=%s", info.ModTime())
	}
}

func TestImportFileSystemTokenReplaceCanUpdateStoredRegionAfterValidation(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	oldToken := fsTestTokenVariant(t, "tenant-import", "old")
	newToken := fsTestTokenVariant(t, "tenant-import", "new")
	profile := testProfile()
	profile.HomeDir = home
	if _, err := fscred.StoreCredential(home, profile, "tenant-import", "aws-us-east-1", oldToken, false); err != nil {
		t.Fatal(err)
	}
	profile.PlacementRegionCode = "aws-us-west-2"
	profile.RegionCode = "us-west-2"
	t.Setenv("TI_FAKE_DRIVE9_EXPECT_API_KEY", newToken)
	resolver := endpoints.Resolver{FSManifest: &endpoints.FSRegionManifest{Regions: []endpoints.FSRegionManifestEntry{
		{RegionCode: "aws-us-west-2", Mode: endpoints.DefaultFSMode, ServerURL: "https://fs-west.test", CloudProvider: "aws", TiDBRegion: "us-west-2"},
	}}}
	service := Service{HomeDir: home, CompanionPath: companion, Resolver: resolver}
	if _, err := service.ImportFileSystemToken(context.Background(), ImportFileSystemTokenOptions{Profile: profile, Token: newToken}); apperr.CodeFor(err) != "fs.credential_import_conflict" {
		t.Fatalf("import conflict error = %v", err)
	}
	result, err := service.ImportFileSystemToken(context.Background(), ImportFileSystemTokenOptions{Profile: profile, Token: newToken, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.RegionCode != "aws-us-west-2" {
		t.Fatalf("result = %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "tenant-import")
	if err != nil || credential.APIKey != newToken || credential.RegionCode != "aws-us-west-2" {
		t.Fatalf("credential=%#v err=%v", credential, err)
	}
}

func fsTestToken(t *testing.T, tenantID string) string {
	return fsTestTokenVariant(t, tenantID, "signature")
}

func fsTestTokenVariant(t *testing.T, tenantID, signature string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payloadBytes, err := json.Marshal(map[string]any{"tenant_id": tenantID, "token_version": 1, "iat": 1})
	if err != nil {
		t.Fatal(err)
	}
	jwt := header + "." + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + signature
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
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

func TestDrive9MountSuppressesCompanionSuccessChatter(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := testCompanionService(home, companion)
	service.Stdout = &stdout
	service.Stderr = &stderr

	result, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      filepath.Join(t.TempDir(), "workspace"),
		RemotePath:     "/",
	})
	if err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("companion mount output leaked to ti output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if result.Driver != "webdav" || result.Status != "mounted" {
		t.Fatalf("mount result did not retain captured companion state: %#v", result)
	}
}

func TestDrive9MountPreservesCapturedFailureDiagnostics(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL", "1")
	_, err := testCompanionService(t.TempDir(), companion).MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      filepath.Join(t.TempDir(), "workspace"),
		RemotePath:     "/",
	})
	if err == nil {
		t.Fatal("expected mount failure")
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "background mount exited before becoming ready") {
		t.Fatalf("mount failure lost companion diagnostics: %q", message)
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

func TestDryRunCreateFileSystemUsesAdminTenantMetadataShape(t *testing.T) {
	profile := testProfile()
	displayName := "agent-workspace"
	result, err := Service{Resolver: supportedFSManifestResolver("https://fs.test")}.DryRunCreateFileSystem(context.Background(), "ti fs create-file-system", CreateFileSystemOptions{
		Profile:        profile,
		WaitUntilReady: true,
		DisplayName:    &displayName,
		Labels:         map[string]string{"environment": "production"},
	})
	if err != nil {
		t.Fatalf("DryRunCreateFileSystem failed: %v", err)
	}
	if result.Operation != "create_file_system" || result.Request.Path != "/v1/admin/tenants" {
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
	if body["display_name"] != displayName {
		t.Fatalf("dry-run lost metadata: %#v", body)
	}
	if _, ok := body["public_key"]; ok {
		t.Fatalf("dry-run leaked credentials: %#v", body)
	}
	if labels, ok := body["label"].(map[string]any); !ok || labels["environment"] != "production" {
		t.Fatalf("dry-run lost labels: %#v", body)
	}
	if !hasDryRunCheck(result.Checks, "endpoint_selection", "passed") {
		t.Fatalf("expected endpoint dry-run check: %#v", result.Checks)
	}
	if !hasDryRunCheck(result.Checks, "post_create_wait", "passed") {
		t.Fatalf("expected readiness wait dry-run check: %#v", result.Checks)
	}
}

func TestDryRunDeleteFileSystemReportsCredentialFile(t *testing.T) {
	home := t.TempDir()
	profile := dataProfile()
	if _, err := fscred.StoreCredential(home, profile, "tenant-1", "aws-us-east-1", "fs-secret", false); err != nil {
		t.Fatal(err)
	}
	result, err := (Service{HomeDir: home, Resolver: supportedFSManifestResolver("https://fs.test")}).DryRunDeleteFileSystem(context.Background(), "ti fs delete-file-system", DeleteFileSystemOptions{
		Profile:      profile,
		FileSystemID: "tenant-1",
	})
	if err != nil {
		t.Fatalf("DryRunDeleteFileSystem failed: %v", err)
	}
	paths, err := fscred.CredentialPath(home, profile.Name, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "local_credentials" {
			found = strings.Contains(check.Message, paths.Credentials)
		}
	}
	if !found {
		t.Fatalf("dry-run did not report registry files: %#v", result.Checks)
	}
	if _, err := fscred.GetCredential(home, profile.Name, "tenant-1"); err != nil {
		t.Fatalf("dry-run removed credential: %v", err)
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
		if path := os.Getenv("TI_FAKE_DRIVE9_BREAK_CREDENTIAL_ROOT"); path != "" {
			_ = os.RemoveAll(path)
			_ = os.WriteFile(path, []byte("not a directory"), 0600)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"tenant_id":      "tenant-1",
			"api_key":        "fs-secret",
			"status":         "active",
			"cloud_provider": "aws",
			"region_code":    os.Getenv("DRIVE9_REGION_CODE"),
			"server":         os.Getenv("DRIVE9_SERVER"),
		})
	case args[0] == "mount" && (len(args) == 1 || args[1] != "drain"):
		fmt.Fprintln(os.Stderr, "component: drive9 mount")
		fmt.Fprintln(os.Stderr, "drive9: mount mode: webdav")
		if os.Getenv("TI_FAKE_DRIVE9_MOUNT_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "mount: drive9 mount: background mount exited before becoming ready")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "drive9: mount running in background")
		fmt.Fprintln(os.Stderr, "drive9: unmount with drive9 umount /workspace")
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "delete":
		if os.Getenv("TI_FAKE_DRIVE9_NOT_FOUND") == "1" {
			fmt.Fprintln(os.Stderr, "delete admin tenant: HTTP 404: tenant not found")
			os.Exit(1)
		}
		if os.Getenv("TI_FAKE_DRIVE9_DELETE_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "admin tenant delete: backend unavailable")
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"tenant_id": "tenant-1", "status": "deleting"})
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "list":
		switch os.Getenv("TI_FAKE_DRIVE9_LIST_MODE") {
		case "empty":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"tenants": []any{}, "page": 1, "page_size": 100})
		case "mixed-local-token":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
					{"tenant_id": "tenant-2", "status": "active", "kind": "live"},
				},
				"page": 1, "page_size": 100,
			})
		case "malformed":
			fmt.Fprint(os.Stdout, "{")
		case "paginate":
			if flagValue(args, "--page") == "1" {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"tenants": []map[string]any{{"tenant_id": "tenant-2", "status": "active", "kind": "live"}},
					"page": 1, "page_size": 100, "next_page": 2,
				})
			} else {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"tenants": []map[string]any{{"tenant_id": "tenant-1", "status": "active", "kind": "live"}},
					"page": 2, "page_size": 100,
				})
			}
		case "regress":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []any{}, "page": 1, "page_size": 100, "next_page": 1,
			})
		case "page-mismatch":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []any{}, "page": 2, "page_size": 100,
			})
		case "duplicate":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
				},
				"page": 1, "page_size": 100,
			})
		default:
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{{"tenant_id": "tenant-1", "status": "active", "kind": "live"}},
				"page": 1, "page_size": 100,
			})
		}
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "get":
		if os.Getenv("TI_FAKE_DRIVE9_NOT_FOUND") == "1" {
			fmt.Fprintln(os.Stderr, "get admin tenant: HTTP 404: tenant not found")
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"tenant_id": "tenant-1", "status": "active", "kind": "live"})
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
		if expected := os.Getenv("TI_FAKE_DRIVE9_EXPECT_API_KEY"); expected != "" && os.Getenv("DRIVE9_API_KEY") != expected {
			fmt.Fprintln(os.Stderr, "fs stat: unauthorized")
			os.Exit(1)
		}
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

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
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
