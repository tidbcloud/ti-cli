package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteProfileCreatesFilesAndRestrictsCredentials(t *testing.T) {
	home := t.TempDir()

	err := WriteProfile(home, "default", ConfigProfile{
		RegionCode:      "aws-us-east-1",
		ProjectID:       "virtual-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, CredentialsProfile{
		TDCPublicKey:  "public",
		TDCPrivateKey: "private",
		FSAPIKey:      "fs-secret",
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
	if cfg["default"].ProjectID != "virtual-1" {
		t.Fatalf("project id was not persisted: %#v", cfg["default"])
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
	tdcDir := filepath.Join(home, TDCDirName)
	if err := os.MkdirAll(tdcDir, 0o700); err != nil {
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

func TestWriteProfilePreservesLoggingConfig(t *testing.T) {
	home := t.TempDir()
	tdcDir := filepath.Join(home, TDCDirName)
	if err := os.MkdirAll(tdcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(home), []byte(`
[logging]
enabled = false
max_file_mb = 3
max_files = 2
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProfile(home, "default", ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, CredentialsProfile{
		TDCPublicKey:  "public",
		TDCPrivateKey: "private",
	}); err != nil {
		t.Fatalf("WriteProfile failed: %v", err)
	}

	logging, ok, err := ReadLoggingConfig(home)
	if err != nil {
		t.Fatalf("ReadLoggingConfig failed: %v", err)
	}
	if !ok || logging.Enabled == nil || *logging.Enabled || logging.MaxFileMB != 3 || logging.MaxFiles != 2 {
		t.Fatalf("logging config was not preserved: ok=%v %#v", ok, logging)
	}
	configDoc, err := ReadConfig(home)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if _, ok := configDoc["logging"]; ok {
		t.Fatalf("logging section must not be returned as a profile: %#v", configDoc)
	}
}

func TestRemoveLegacyFSDefaultFileSystemPreservesConfig(t *testing.T) {
	home := t.TempDir()
	tdcDir := filepath.Join(home, TDCDirName)
	if err := os.MkdirAll(tdcDir, 0o700); err != nil {
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

[logging]
enabled = false
max_file_mb = 3
max_files = 2
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
	if got := configDoc["default"]; got.RegionCode != "aws-us-east-1" || got.ProjectID != "project-1" {
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
	logging, ok, err := ReadLoggingConfig(home)
	if err != nil || !ok || logging.Enabled == nil || *logging.Enabled || logging.MaxFileMB != 3 || logging.MaxFiles != 2 {
		t.Fatalf("logging config was not preserved: ok=%v logging=%#v err=%v", ok, logging, err)
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
		TDCPublicKey:  "public",
		TDCPrivateKey: "private",
		FSAPIKey:      "fs-secret",
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
	if got := credentialsDoc["stage"]; got.FSAPIKey != "" || got.TDCPublicKey != "public" || got.TDCPrivateKey != "private" {
		t.Fatalf("unexpected credentials after clear: %#v", got)
	}
}

func TestReadCredentialsRepairsPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are not meaningful on Windows")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, TDCDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CredentialsPath(home), []byte(`
[default]
tdc_public_key = "public"
tdc_private_key = "private"
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
	if err := os.MkdirAll(filepath.Join(home, TDCDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CredentialsPath(home), []byte(`
[default]
tdc_public_key = "public"
tdc_private_key = "private"

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
	if !strings.Contains(err.Error(), "~/.tdc/db_users/<cluster-id>/credentials") {
		t.Fatalf("expected error to mention db user credential path, got %v", err)
	}
}
