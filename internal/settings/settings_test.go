package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestLoadMissingSettingsDoesNotCreateFile(t *testing.T) {
	home := t.TempDir()
	doc, exists, err := Load(home)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if exists {
		t.Fatalf("missing settings reported as existing: %#v", doc)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Fatalf("missing settings was created: %v", err)
	}

	logging, err := ResolveLogging(home, map[string]string{})
	if err != nil {
		t.Fatalf("ResolveLogging failed: %v", err)
	}
	if !logging.Enabled || logging.MaxFileBytes != 10*1024*1024 || logging.MaxFiles != 5 {
		t.Fatalf("unexpected logging defaults: %#v", logging)
	}
}

func TestPathUsesHiddenPreferencesAndIgnoresUnshippedSettingsPath(t *testing.T) {
	home := t.TempDir()
	wantPath := filepath.Join(home, ".ti", ".preferences")
	if got := Path(home); got != wantPath {
		t.Fatalf("Path(%q) = %q, want %q", home, got, wantPath)
	}
	writeFile(t, filepath.Join(home, ".ti", "settings"), "[logging]\nenabled = false\n", 0o600)

	cfg, err := ResolveLogging(home, map[string]string{})
	if err != nil {
		t.Fatalf("ResolveLogging failed: %v", err)
	}
	if !cfg.Enabled {
		t.Fatalf("unshipped settings path affected logging: %#v", cfg)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("unshipped settings path was migrated: %v", err)
	}
}

func TestLoadStrictSettingsAndExposeTelemetryPreference(t *testing.T) {
	home := t.TempDir()
	writeFile(t, Path(home), `schema_version = 1

[logging]
enabled = false
max_file_mb = 3
max_files = 2

[telemetry]
enabled = false
`, 0o644)

	doc, exists, err := Load(home)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !exists || doc.Logging.Enabled == nil || *doc.Logging.Enabled || doc.Telemetry.Enabled == nil || *doc.Telemetry.Enabled {
		t.Fatalf("unexpected settings document: %#v", doc)
	}
	logging, err := ResolveLogging(home, map[string]string{})
	if err != nil {
		t.Fatalf("ResolveLogging failed: %v", err)
	}
	if logging.Enabled || logging.MaxFileBytes != 3*1024*1024 || logging.MaxFiles != 2 {
		t.Fatalf("unexpected resolved logging: %#v", logging)
	}
	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("reading settings changed user permissions: got %o", info.Mode().Perm())
	}
}

func TestResolveLoggingEnvironmentPrecedence(t *testing.T) {
	home := t.TempDir()
	writeFile(t, Path(home), `[logging]
enabled = false
max_file_mb = 4
max_files = 3
`, 0o600)

	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "on", want: true},
		{value: "TRUE", want: true},
		{value: "1", want: true},
		{value: "YES", want: true},
		{value: "off", want: false},
		{value: "FALSE", want: false},
		{value: "0", want: false},
		{value: "no", want: false},
	} {
		t.Run(test.value, func(t *testing.T) {
			cfg, err := ResolveLogging(home, map[string]string{"TI_LOGGING": test.value})
			if err != nil {
				t.Fatalf("override failed: %v", err)
			}
			if cfg.Enabled != test.want {
				t.Fatalf("enabled = %t, want %t", cfg.Enabled, test.want)
			}
			if test.want && (cfg.MaxFileBytes != 4*1024*1024 || cfg.MaxFiles != 3) {
				t.Fatalf("enabled override did not preserve settings limits: %#v", cfg)
			}
		})
	}
}

func TestResolveLoggingDisabledEnvironmentDoesNotReadMalformedSettings(t *testing.T) {
	home := t.TempDir()
	writeFile(t, Path(home), "not valid toml = [", 0o600)
	writeFile(t, store.ConfigPath(home), "not valid toml = [", 0o644)
	cfg, err := ResolveLogging(home, map[string]string{"TI_LOGGING": "off"})
	if err != nil {
		t.Fatalf("disabled environment should short-circuit settings: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("expected disabled logging: %#v", cfg)
	}
}

func TestResolveLoggingInvalidEnvironmentFailsClosed(t *testing.T) {
	home := t.TempDir()
	writeFile(t, Path(home), "not valid toml = [", 0o600)
	writeFile(t, store.ConfigPath(home), "not valid toml = [", 0o644)
	cfg, err := ResolveLogging(home, map[string]string{"TI_LOGGING": "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "invalid TI_LOGGING") {
		t.Fatalf("expected invalid environment error, got %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("invalid environment did not fail closed: %#v", cfg)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown field", content: "unknown = true\n"},
		{name: "unknown section", content: "[other]\nenabled = true\n"},
		{name: "unsupported schema", content: "schema_version = 2\n"},
		{name: "zero max file", content: "[logging]\nmax_file_mb = 0\n"},
		{name: "negative max files", content: "[logging]\nmax_files = -1\n"},
		{name: "wrong telemetry type", content: "[telemetry]\nenabled = \"no\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, Path(home), test.content, 0o600)
			if _, _, err := Load(home); err == nil {
				t.Fatal("expected invalid settings error")
			}
		})
	}
}

func TestLoadMigratesLegacyLoggingAndPreservesProfilesAndCredentials(t *testing.T) {
	home := t.TempDir()
	writeFile(t, store.ConfigPath(home), `[default]
region_code = "aws-us-east-1"
project_id = "project-default"

[stage]
region_code = "aws-us-west-2"
project_id = "project-stage"

[logging]
enabled = false
max_file_mb = 3
max_files = 2
`, 0o644)
	credentials := []byte("[default]\ntidb_cloud_public_key = 'public'\ntidb_cloud_private_key = 'private'\n")
	writeFile(t, store.CredentialsPath(home), string(credentials), 0o600)

	doc, exists, err := Load(home)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !exists || doc.Logging.Enabled == nil || *doc.Logging.Enabled || doc.Logging.MaxFileMB == nil || *doc.Logging.MaxFileMB != 3 || doc.Logging.MaxFiles == nil || *doc.Logging.MaxFiles != 2 {
		t.Fatalf("legacy logging was not migrated: %#v", doc)
	}
	settingsData, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settingsData), "[telemetry]") {
		t.Fatalf("migration wrote empty telemetry settings: %s", settingsData)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path(home))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("migrated settings mode: want 0600, got %o", info.Mode().Perm())
		}
	}
	configData, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "[logging]") {
		t.Fatalf("legacy logging remains in config: %s", configData)
	}
	profiles, err := store.ReadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if profiles["default"].LegacyProjectID != "project-default" || profiles["stage"].LegacyProjectID != "project-stage" {
		t.Fatalf("profiles changed during migration: %#v", profiles)
	}
	afterCredentials, err := os.ReadFile(store.CredentialsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCredentials) != string(credentials) {
		t.Fatalf("credentials changed during migration:\n%s", afterCredentials)
	}

	beforeSettings := string(settingsData)
	if _, _, err := Load(home); err != nil {
		t.Fatalf("repeated Load failed: %v", err)
	}
	afterSettings, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSettings) != beforeSettings {
		t.Fatalf("repeated migration rewrote settings:\n%s", afterSettings)
	}
}

func TestMigrationKeepsExistingSettingsAuthoritativeAndUnchanged(t *testing.T) {
	home := t.TempDir()
	writeFile(t, store.ConfigPath(home), `[default]
region_code = "aws-us-east-1"

[logging]
enabled = false
max_file_mb = 2
`, 0o644)
	settingsData := "# user formatting must remain\nschema_version = 1\n\n[telemetry]\nenabled = false\n"
	writeFile(t, Path(home), settingsData, 0o644)

	doc, exists, err := Load(home)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !exists || doc.Logging.Enabled != nil || doc.Telemetry.Enabled == nil || *doc.Telemetry.Enabled {
		t.Fatalf("existing settings were not authoritative: %#v", doc)
	}
	after, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != settingsData {
		t.Fatalf("migration rewrote existing settings:\n%s", after)
	}
	configData, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "[logging]") {
		t.Fatalf("legacy logging was not removed: %s", configData)
	}
	logging, err := ResolveLogging(home, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !logging.Enabled || logging.MaxFileBytes != 10*1024*1024 || logging.MaxFiles != 5 {
		t.Fatalf("missing authoritative logging did not use defaults: %#v", logging)
	}
}

func TestMigrationFailurePreservesLegacyConfig(t *testing.T) {
	home := t.TempDir()
	legacy := "[logging]\nenabled = false\n"
	writeFile(t, store.ConfigPath(home), legacy, 0o644)
	if err := os.MkdirAll(Path(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(home); err == nil {
		t.Fatal("expected migration failure")
	}
	after, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != legacy {
		t.Fatalf("failed migration changed legacy config: %q", after)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
