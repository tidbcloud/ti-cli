package fs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/credentialenv"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

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

func TestDrive9CopyUploadWithoutOverwriteRejectsExistingRemoteTarget(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	// The fake companion's `fs stat` succeeds by default, i.e. the target exists.

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:   dataProfile(),
		FromLocal: filepath.Join(t.TempDir(), "replacement.txt"),
		ToRemote:  "/workspace/existing.txt",
	})
	if apperr.CodeFor(err) != "fs.target_exists" {
		t.Fatalf("copy error = %v, want fs.target_exists", err)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, []string{"fs", "cp"}) {
			t.Fatalf("blocked copy still invoked companion: %#v", call.Args)
		}
	}
}

func TestDrive9CopyUploadGuardBypassedForOverwriteAppendAndResume(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CopyFileOptions)
	}{
		{name: "overwrite", mutate: func(o *CopyFileOptions) { o.Overwrite = true }},
		{name: "append", mutate: func(o *CopyFileOptions) { o.Append = true }},
		{name: "resume", mutate: func(o *CopyFileOptions) { o.Resume = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			opts := CopyFileOptions{
				Profile:   dataProfile(),
				FromLocal: filepath.Join(t.TempDir(), "input.txt"),
				ToRemote:  "/workspace/existing.txt",
			}
			tc.mutate(&opts)
			if _, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), opts); err != nil {
				t.Fatalf("copy failed: %v", err)
			}
			requireFakeDrive9Call(t, recordPath, "fs", "cp")
		})
	}
}

func TestDrive9CopyUploadGuardFailsClosedOnStatError(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_STAT_ALWAYS_FAIL", "1")

	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile:   dataProfile(),
		FromLocal: filepath.Join(t.TempDir(), "input.txt"),
		ToRemote:  "/workspace/existing.txt",
	})
	if err == nil {
		t.Fatal("copy should fail when the target probe cannot determine existence")
	}
	if isDrive9NotFound(err) {
		t.Fatalf("copy error = %v, want non-not-found probe failure", err)
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "backend unavailable") {
		t.Fatalf("probe failure = %q, want the stat error to propagate", message)
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
			t.Setenv("TI_FAKE_DRIVE9_STAT_NOT_FOUND", "1")
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

func TestDrive9LayerForkChainAndDeleteTranslateToCompanion(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	profile := dataProfile()

	forked, err := service.ForkLayer(context.Background(), ForkLayerOptions{
		Profile: profile, ParentLayerRef: "research-base", LayerID: "child-id", LayerName: "style-analyst", CheckpointID: "seed", ActorID: "hermes",
	})
	if err != nil {
		t.Fatalf("ForkLayer failed: %v", err)
	}
	if forked.LayerID != "child-id" || forked.ParentLayerID != "parent-id" || forked.OriginCheckpointID != "seed" || forked.Depth != 1 {
		t.Fatalf("unexpected fork response: %#v", forked)
	}
	forkCall := requireFakeDrive9Call(t, recordPath, "fs", "layer", "fork")
	wantFork := []string{"fs", "layer", "fork", "--json", "--id", "child-id", "--name", "style-analyst", "--checkpoint", "seed", "--actor", "hermes", "research-base"}
	if fmt.Sprint(forkCall.Args) != fmt.Sprint(wantFork) {
		t.Fatalf("fork args = %#v, want %#v", forkCall.Args, wantFork)
	}

	chain, err := service.ListLayerChain(context.Background(), ListLayerChainOptions{Profile: profile, LayerRef: "style-analyst"})
	if err != nil {
		t.Fatalf("ListLayerChain failed: %v", err)
	}
	if len(chain.Chain) != 2 || chain.Chain[0].LayerID != "root-id" || chain.Chain[1].LimitSeq != 12 {
		t.Fatalf("unexpected chain response: %#v", chain)
	}
	chainCall := requireFakeDrive9Call(t, recordPath, "fs", "layer", "chain")
	wantChain := []string{"fs", "layer", "chain", "--json", "style-analyst"}
	if fmt.Sprint(chainCall.Args) != fmt.Sprint(wantChain) {
		t.Fatalf("chain args = %#v, want %#v", chainCall.Args, wantChain)
	}

	deleted, err := service.DeleteLayer(context.Background(), DeleteLayerOptions{Profile: profile, LayerRef: "style-brief", Cascade: true})
	if err != nil {
		t.Fatalf("DeleteLayer failed: %v", err)
	}
	if deleted.Status != "abandoned" || !deleted.Cascade || deleted.LayerRef != "style-brief" {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}
	deleteCall := requireFakeDrive9Call(t, recordPath, "fs", "layer", "delete")
	wantDelete := []string{"fs", "layer", "delete", "--cascade", "style-brief"}
	if fmt.Sprint(deleteCall.Args) != fmt.Sprint(wantDelete) {
		t.Fatalf("delete args = %#v, want %#v", deleteCall.Args, wantDelete)
	}
}

func TestDrive9LayerTipForkAndLeafDeleteOmitOptionalFlags(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	profile := dataProfile()

	forked, err := service.ForkLayer(context.Background(), ForkLayerOptions{Profile: profile, ParentLayerRef: "parent-id"})
	if err != nil {
		t.Fatalf("ForkLayer failed: %v", err)
	}
	if forked.LayerID == "" {
		t.Fatalf("fork response did not include a generated child ID: %#v", forked)
	}
	forkCall := requireFakeDrive9Call(t, recordPath, "fs", "layer", "fork")
	wantFork := []string{"fs", "layer", "fork", "--json", "parent-id"}
	if fmt.Sprint(forkCall.Args) != fmt.Sprint(wantFork) {
		t.Fatalf("tip fork args = %#v, want %#v", forkCall.Args, wantFork)
	}

	deleted, err := service.DeleteLayer(context.Background(), DeleteLayerOptions{Profile: profile, LayerRef: "leaf-id"})
	if err != nil {
		t.Fatalf("DeleteLayer failed: %v", err)
	}
	if deleted.Cascade {
		t.Fatalf("leaf delete unexpectedly enabled cascade: %#v", deleted)
	}
	deleteCall := requireFakeDrive9Call(t, recordPath, "fs", "layer", "delete")
	wantDelete := []string{"fs", "layer", "delete", "leaf-id"}
	if fmt.Sprint(deleteCall.Args) != fmt.Sprint(wantDelete) {
		t.Fatalf("leaf delete args = %#v, want %#v", deleteCall.Args, wantDelete)
	}
}

func TestDrive9LayerWorkflowRejectsIncompatibleCompanion(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_OLD_LAYER", "1")
	_, err := testCompanionService(t.TempDir(), companion).ForkLayer(context.Background(), ForkLayerOptions{Profile: dataProfile(), ParentLayerRef: "parent"})
	if apperr.CodeFor(err) != "fs.companion_incompatible" {
		t.Fatalf("error = %v, want fs.companion_incompatible", err)
	}
}

func TestDrive9DeleteLayerPreservesDescendantConflict(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_DELETE_DESCENDANT", "1")
	_, err := testCompanionService(t.TempDir(), companion).DeleteLayer(context.Background(), DeleteLayerOptions{Profile: dataProfile(), LayerRef: "parent"})
	if err == nil || !strings.Contains(apperr.MessageFor(err), "live descendants") {
		t.Fatalf("delete descendant conflict was not preserved: %v", err)
	}
}

func TestDrive9LayerCheckpointMountIsReadOnlyAndRecorded(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	mountPath := filepath.Join(t.TempDir(), "checkpoint")
	checkpointService := testCompanionService(home, companion)
	checkpointService.mountPointActive = trueMountEvidence
	result, err := checkpointService.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, RemotePath: "/research/q3-market", Driver: "fuse", LayerRef: "style-analyst", CheckpointID: "v5",
	})
	if err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	if !result.ReadOnly || result.LayerRef != "style-analyst" || result.CheckpointID != "v5" || result.Driver != "fuse" {
		t.Fatalf("unexpected layer mount result: %#v", result)
	}
	mountCall := requireFakeDrive9CallWithoutArg(t, recordPath, "--help", "mount")
	want := []string{"mount", "--mode", "fuse", "--read-only", "--layer", "style-analyst", "--checkpoint", "v5", ":/research/q3-market", mountPath}
	if fmt.Sprint(mountCall.Args) != fmt.Sprint(want) {
		t.Fatalf("mount args = %#v, want %#v", mountCall.Args, want)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read mount locator: %v", err)
	}
	if locator.LayerRef != "style-analyst" || locator.CheckpointID != "v5" || !locator.ReadOnly || locator.RemotePath != "/research/q3-market" {
		t.Fatalf("unexpected layer mount locator: %#v", locator)
	}
}

func TestLayerMountValidationRejectsUnsupportedInputs(t *testing.T) {
	profile := dataProfile()
	service := Service{}
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{Profile: profile, MountPath: "/tmp/x", Driver: "fuse", CheckpointID: "v5"}); apperr.CodeFor(err) != "fs.checkpoint_requires_layer" {
		t.Fatalf("checkpoint-only error = %v", err)
	}
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{Profile: profile, MountPath: "/tmp/x", Driver: "webdav", LayerRef: "layer"}); apperr.CodeFor(err) != "fs.layer_mount_requires_fuse" {
		t.Fatalf("webdav layer error = %v", err)
	}
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{Profile: profile, MountPath: "/tmp/x", Driver: "fuse", LayerRef: "layer", CheckpointID: "v5", ReadOnlySet: true}); apperr.CodeFor(err) != "fs.checkpoint_mount_read_only" {
		t.Fatalf("explicit writable checkpoint error = %v", err)
	}
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{Profile: profile, MountPath: "/tmp/x", Driver: "fuse", LayerRef: "layer", CheckpointID: "v5", WriteBackCache: true, WriteBackCacheSet: true}); apperr.CodeFor(err) != "fs.checkpoint_mount_read_only" {
		t.Fatalf("checkpoint write-back error = %v", err)
	}
}

func TestLayerReferenceValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		code  string
	}{
		{name: "empty", value: " ", code: "fs.missing_layer_reference"},
		{name: "slash", value: "parent/child", code: "fs.invalid_layer_reference"},
		{name: "backslash", value: `parent\child`, code: "fs.invalid_layer_reference"},
		{name: "control", value: "parent\nchild", code: "fs.invalid_layer_reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateLayerReference(tc.value, "--layer-ref"); apperr.CodeFor(err) != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
	if err := ValidateLayerReference("tag:run=123", "--layer-ref"); err != nil {
		t.Fatalf("valid tag reference failed: %v", err)
	}
}

func TestDrive9RecursiveLayerCopyIsRejectedLocally(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	_, err := testCompanionService(t.TempDir(), companion).CopyFile(context.Background(), CopyFileOptions{
		Profile: dataProfile(), FromLocal: ".", ToRemote: "/research/q3-market", Recursive: true, LayerID: "research-base",
	})
	if apperr.CodeFor(err) != "fs.recursive_layer_copy_unsupported" {
		t.Fatalf("error = %v, want fs.recursive_layer_copy_unsupported", err)
	}
	if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("rejected copy invoked companion: %v", statErr)
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
	if message := apperr.MessageFor(err); !strings.Contains(message, "ti requires the Drive9 companion binary") {
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

// TestDrive9VaultMountLocatorWriteFailureTeardownAdvisesCompanion mirrors the fs
// mount case for vault mounts, and asserts through apperr.MessageFor because that
// is what the CLI renders: a fmt.Errorf wrapper would hide the advice.
func TestDrive9VaultMountLocatorWriteFailureTeardownAdvisesCompanion(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	if err := os.MkdirAll(filepath.Join(home, ".ti"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ti", "mounts"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	_, err := service.MountVault(context.Background(), VaultMountOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "vault"), VaultToken: "vault-token",
	})
	if err == nil {
		t.Fatal("vault mount with a failing locator write reported success")
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "may still be running") || !strings.Contains(message, "ti-drive9 umount") {
		t.Fatalf("vault teardown message = %q, want running warning and companion stop advice", message)
	}
	if strings.Contains(message, "ti fs-vault unmount-vault") {
		t.Fatalf("vault teardown message points at a locator that does not exist: %q", message)
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

func TestParseDrive9LSPreservesNamesWithSpaces(t *testing.T) {
	// Mirrors `drive9 fs ls -l` output: kind and size columns are
	// space-padded by the companion's tabwriter, the name is the final
	// column and may contain (multiple) spaces.
	raw := []byte("" +
		"-  17    alpha beta.txt\n" +
		"d    4096  reports q3.md\n" +
		"-  17    double  space.txt\n" +
		"-  9     plain.txt\n")
	entries := parseDrive9LS(raw)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4: %#v", len(entries), entries)
	}
	want := []FileEntry{
		{Name: "alpha beta.txt", SizeBytes: 17, IsDir: false},
		{Name: "reports q3.md", SizeBytes: 4096, IsDir: true},
		{Name: "double  space.txt", SizeBytes: 17, IsDir: false},
		{Name: "plain.txt", SizeBytes: 9, IsDir: false},
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry[%d] = %#v, want %#v", i, entries[i], want[i])
		}
	}
}

func TestParseDrive9LSSkipsMalformedLines(t *testing.T) {
	entries := parseDrive9LS([]byte("total 0\nnot a listing line\n-  x  bad size.txt\n"))
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want none", entries)
	}
	if got := parseDrive9LS(nil); len(got) != 0 {
		t.Fatalf("entries = %#v, want empty", got)
	}
}

func TestParseDrive9PathsPreservesSpaces(t *testing.T) {
	raw := []byte("" +
		"/docs/alpha beta.txt\n" +
		":/docs/nested dir/two words.md\n" +
		"/search hit\t0.85\n")
	results := parseDrive9Paths(raw, 0)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3: %#v", len(results), results)
	}
	want := []SearchResult{
		{Path: "/docs/alpha beta.txt", Name: "alpha beta.txt"},
		{Path: "/docs/nested dir/two words.md", Name: "two words.md"},
		{Path: "/search hit", Name: "search hit"},
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("results[%d] = %#v, want %#v", i, results[i], want[i])
		}
	}
	if got := parseDrive9Paths(raw, 2); len(got) != 2 {
		t.Fatalf("limit results = %d, want 2", len(got))
	}
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

// TestDrive9RunWithVaultSecretServiceFlow covers the orchestration added by the
// run-with-secret rewrite through the script companion harness: the companion is
// asked for the field map, the child runs with the scrubbed environment and the
// injected fields, and the decode/validation/exit-code paths surface the expected
// errors.
func TestDrive9RunWithVaultSecretServiceFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	companion, recordPath := buildFakeDrive9(t)
	service := testCompanionService(t.TempDir(), companion)
	profile := dataProfile()
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FS_TOKEN", "fs-bearer")

	run := func(payload string, command ...string) (string, string, error) {
		t.Helper()
		t.Setenv("TI_FAKE_VAULT_GET", payload)
		var out, errOut bytes.Buffer
		err := service.RunWithVaultSecret(context.Background(), VaultRunWithSecretOptions{
			Profile:    profile,
			SecretPath: "/n/vault/demo",
			Command:    command,
			Stdout:     &out,
			Stderr:     &errOut,
		})
		return out.String(), errOut.String(), err
	}

	out, _, err := run(`{"DB_URL":"mysql://secret","PASSWORD":"s3cr3t","café":"accented"}`, "/bin/sh", "-c", "env")
	if err != nil {
		t.Fatalf("run-with-secret failed: %v", err)
	}
	// café exercises the companion boundary: the companion exports it byte-wise,
	// so the accented character contributes two underscores.
	for _, want := range []string{"DB_URL=mysql://secret", "PASSWORD=s3cr3t", "CAF__=accented"} {
		if !strings.Contains(out, want) {
			t.Fatalf("child environment missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "fs-bearer") {
		t.Fatalf("child environment leaked the filesystem token:\n%s", out)
	}
	call := requireFakeDrive9Call(t, recordPath, "vault", "get")
	if got, want := strings.Join(call.Args, " "), "vault get demo --json"; got != want {
		t.Fatalf("companion args = %q, want %q", got, want)
	}

	// "é" (two bytes) and "_" (one byte) stay distinct, matching the companion.
	if _, _, err := run(`{"é":"a","_":"b"}`, "/bin/sh", "-c", "env"); err != nil {
		t.Fatalf("distinct non-ASCII field names rejected: %v", err)
	}

	if _, _, err := run(`{}`, "/bin/true"); apperr.CodeFor(err) != "vault.empty_secret" {
		t.Fatalf("empty secret error = %v", err)
	}
	if _, _, err := run(`{"db_url":"a","DB_URL":"b"}`, "/bin/true"); apperr.CodeFor(err) != "vault.env_collision" {
		t.Fatalf("colliding field names error = %v", err)
	}
	if _, _, err := run(`{"DB_URL":"x"}`, "/bin/sh", "-c", "exit 7"); apperr.CodeFor(err) != "vault.command_failed" {
		t.Fatalf("child exit error = %v, want vault.command_failed", err)
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

func TestCredentialEnvScrubRemovesCanonicalLegacyAndCompanionSecrets(t *testing.T) {
	base := []string{
		"SAFE=value",
		"TIDB_CLOUD_PUBLIC_KEY=canonical-public",
		"TIDB_CLOUD_PRIVATE_KEY=canonical-private",
		"TI_FS_TOKEN=canonical-fs",
		"TI_VAULT_TOKEN=canonical-vault",
		"TI_FS_API_KEY=internal-fs",
		"TI_FS_AI_PROVIDER_API_KEY=provider-key",
		"TDC_PUBLIC_KEY=legacy-public",
		"TDC_PRIVATE_KEY=legacy-private",
		"TDC_FS_TOKEN=legacy-fs",
		"TDC_VAULT_TOKEN=legacy-vault",
		"DRIVE9_PUBLIC_KEY=companion-public",
		"DRIVE9_PRIVATE_KEY=companion-private",
		"DRIVE9_API_KEY=companion-fs",
		"DRIVE9_VAULT_TOKEN=companion-vault",
	}
	got := credentialenv.Scrub(base)
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
