package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestWriteProfileCreatesFilesAndRestrictsCredentials(t *testing.T) {
	home := t.TempDir()

	err := WriteProfile(home, "default", ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, CredentialsProfile{
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
		FSAPIKey:            "fs-secret",
	})
	if err != nil {
		t.Fatalf("WriteProfile failed: %v", err)
	}

	if _, err := os.Stat(ConfigPath(home)); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	credentialsInfo, err := os.Stat(CredentialsPath(home))
	if err != nil {
		t.Fatalf("credentials file missing: %v", err)
	}
	if runtime.GOOS != "windows" && credentialsInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode: want 0600, got %o", credentialsInfo.Mode().Perm())
	}

	cfg, err := ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if cfg["default"].FSResourceName != "workspace" {
		t.Fatalf("fs resource name was not persisted: %#v", cfg["default"])
	}

	creds, err := ReadCredentials(home)
	if err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	if creds["default"].FSAPIKey != "fs-secret" {
		t.Fatalf("fs api key was not persisted: %#v", creds["default"])
	}
}

func TestReadConfigRejectsURLLikeKeys(t *testing.T) {
	home := t.TempDir()
	tiDir := filepath.Join(home, TIDirName)
	if err := os.MkdirAll(tiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(home), []byte(`
[default]
region_code = "aws-us-east-1"
server_url = "https://example.invalid"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadConfig(home)
	if err == nil {
		t.Fatal("expected URL-like key to be rejected")
	}
	if !strings.Contains(err.Error(), "server_url") {
		t.Fatalf("expected error to name server_url, got %v", err)
	}
}

func TestWriteProfileRejectsReservedLoggingName(t *testing.T) {
	home := t.TempDir()
	err := WriteProfile(home, "LoGgInG", ConfigProfile{RegionCode: "aws-us-east-1"}, CredentialsProfile{TiDBCloudPublicKey: "public", TiDBCloudPrivateKey: "private"})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved profile error, got %v", err)
	}
	if _, err := os.Stat(ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatalf("reserved profile wrote config: %v", err)
	}
	if _, err := os.Stat(CredentialsPath(home)); !os.IsNotExist(err) {
		t.Fatalf("reserved profile wrote credentials: %v", err)
	}
}

func TestRemoveLegacyFSDefaultFileSystemPreservesConfig(t *testing.T) {
	home := t.TempDir()
	tiDir := filepath.Join(home, TIDirName)
	if err := os.MkdirAll(tiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(home), []byte(`
[default]
region_code = "aws-us-east-1"
project_id = "project-1"
fs_default_file_system_name = "workspace"

[stage]
region_code = "aws-us-west-2"
fs_default_file_system_name = "scratch"

`), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := RemoveLegacyFSDefaultFileSystem(home, "default")
	if err != nil {
		t.Fatalf("remove legacy default: %v", err)
	}
	if !changed {
		t.Fatal("expected legacy default removal")
	}
	configDoc, err := ReadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := configDoc["default"]; got.RegionCode != "aws-us-east-1" || got.LegacyProjectID != "project-1" {
		t.Fatalf("default profile changed: %#v", got)
	}
	data, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[default]\nfs_default_file_system_name") || strings.Contains(string(data), "project_id = 'project-1'\nfs_default_file_system_name") {
		t.Fatalf("default profile legacy key remains: %s", data)
	}
	if !strings.Contains(string(data), "fs_default_file_system_name = 'scratch'") {
		t.Fatalf("unselected profile legacy key changed: %s", data)
	}
	changed, err = RemoveLegacyFSDefaultFileSystem(home, "default")
	if err != nil {
		t.Fatalf("repeat removal: %v", err)
	}
	if changed {
		t.Fatal("repeated legacy removal should be a no-op")
	}
}

func TestClearFSResourcePreservesTiDBCloudCredentials(t *testing.T) {
	home := t.TempDir()
	if err := WriteProfile(home, "stage", ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant-1",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, CredentialsProfile{
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
		FSAPIKey:            "fs-secret",
	}); err != nil {
		t.Fatalf("WriteProfile failed: %v", err)
	}

	if err := ClearFSResource(home, "stage"); err != nil {
		t.Fatalf("ClearFSResource failed: %v", err)
	}

	configDoc, err := ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if got := configDoc["stage"]; got.FSResourceName != "" || got.FSTenantID != "" || got.CloudProvider != "" || got.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected config after clear: %#v", got)
	}

	credentialsDoc, err := ReadCredentials(home)
	if err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	if got := credentialsDoc["stage"]; got.FSAPIKey != "" || got.TiDBCloudPublicKey != "public" || got.TiDBCloudPrivateKey != "private" {
		t.Fatalf("unexpected credentials after clear: %#v", got)
	}
}

func TestReadCredentialsRepairsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on Windows")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, TIDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CredentialsPath(home), []byte(`
[default]
tidb_cloud_public_key = "public"
tidb_cloud_private_key = "private"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCredentials(home); err != nil {
		t.Fatalf("ReadCredentials failed: %v", err)
	}
	info, err := os.Stat(CredentialsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode was not repaired: got %o", info.Mode().Perm())
	}
}

func TestReadCredentialsRejectsDBUsersInMainCredentials(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, TIDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CredentialsPath(home), []byte(`
[default]
tidb_cloud_public_key = "public"
tidb_cloud_private_key = "private"

[default.db_users."cluster-id".read_write]
username = "user"
password = "pass"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ReadCredentials(home)
	if err == nil {
		t.Fatal("expected db_users in main credentials to be rejected")
	}
	if !strings.Contains(err.Error(), "~/.ti/db_users/<cluster-id>/credentials") {
		t.Fatalf("expected error to mention db user credential path, got %v", err)
	}
}

func TestReadCredentialsSupportsLegacyFieldsAndWritesCanonicalFields(t *testing.T) {
	home := t.TempDir()
	path := CredentialsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[default]\ntdc_public_key='public'\ntdc_private_key='private'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadCredentials(home)
	if err != nil {
		t.Fatal(err)
	}
	if doc["default"].TiDBCloudPublicKey != "public" || doc["default"].TiDBCloudPrivateKey != "private" {
		t.Fatalf("legacy credentials were not decoded: %#v", doc)
	}
	if err := WriteProfile(home, "default", ConfigProfile{RegionCode: "aws-us-east-1"}, CredentialsProfile{}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "tidb_cloud_public_key") || strings.Contains(string(written), "tdc_public_key") {
		t.Fatalf("write did not canonicalize credentials:\n%s", written)
	}
}

func TestReadCredentialsRejectsConflictingLegacyFields(t *testing.T) {
	home := t.TempDir()
	path := CredentialsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "[default]\ntidb_cloud_public_key='new'\ntdc_public_key='old'\ntidb_cloud_private_key='same'\ntdc_private_key='same'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadCredentials(home)
	if apperr.CodeFor(err) != "config.environment_conflict" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeCredentialsPreservesUnrelatedFields(t *testing.T) {
	contents := []byte("[default]\ntdc_public_key='public'\ntdc_private_key='private'\nfs_api_key='drive9-token'\nfuture_secret='preserve-me'\n")
	normalized, err := NormalizeCredentials(contents, "credentials")
	if err != nil {
		t.Fatal(err)
	}
	text := string(normalized)
	for _, want := range []string{"tidb_cloud_public_key", "tidb_cloud_private_key", "fs_api_key", "future_secret", "preserve-me"} {
		if !strings.Contains(text, want) {
			t.Fatalf("normalized credentials lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tdc_public_key") || strings.Contains(text, "tdc_private_key") {
		t.Fatalf("normalized credentials retained legacy names:\n%s", text)
	}
}
