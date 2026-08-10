package homemigration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestEnsureStateDirectorySelection(t *testing.T) {
	t.Run("neither exists", func(t *testing.T) {
		home := t.TempDir()
		result, err := Ensure(home)
		if err != nil || result.Status != "not_needed" {
			t.Fatalf("Ensure() = %#v, %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(home, store.TIDirName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fresh migration unexpectedly created state: %v", err)
		}
	})

	t.Run("new state exists", func(t *testing.T) {
		home := t.TempDir()
		mustMkdir(t, filepath.Join(home, store.TIDirName), 0o700)
		result, err := Ensure(home)
		if err != nil || result.Status != "not_needed" {
			t.Fatalf("Ensure() = %#v, %v", result, err)
		}
	})

	t.Run("both states conflict", func(t *testing.T) {
		home := t.TempDir()
		mustMkdir(t, filepath.Join(home, LegacyDirName), 0o700)
		mustMkdir(t, filepath.Join(home, store.TIDirName), 0o700)
		_, err := Ensure(home)
		if apperr.CodeFor(err) != "config.home_migration_conflict" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestEnsureMigratesDurableStateAndPreservesLegacy(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	mustMkdir(t, legacy, 0o700)
	mustWrite(t, filepath.Join(legacy, "config"), "[default]\nregion_code = 'aws-us-east-1'\n", 0o644)
	mustWrite(t, filepath.Join(legacy, "credentials"), "[default]\ntdc_public_key = 'public'\ntdc_private_key = 'private'\n", 0o600)
	mustWrite(t, filepath.Join(legacy, ".preferences"), "schema_version = 1\n\n[logging]\nenabled = true\n", 0o600)
	mustWrite(t, filepath.Join(legacy, ".telemetry-installation-id"), "tdc_0123456789abcdefghijkl\n", 0o600)
	mustWrite(t, filepath.Join(legacy, "db_users", "cluster-1", "credentials"), "[read_only]\nusername = 'reader'\npassword = 'secret'\n", 0o600)
	mustWrite(t, filepath.Join(legacy, "fs_resources", "profile", "resource", "config"), "file_system_name = 'workspace'\n", 0o644)
	mustWrite(t, filepath.Join(legacy, "fs_resources", "profile", "resource", "credentials"), "api_key = 'drive9_secret'\n", 0o600)
	mustWrite(t, filepath.Join(legacy, "fs_credentials", "resource", "credentials"), "api_key = 'legacy'\n", 0o600)
	for _, excluded := range []string{"logs/ti.jsonl", "cache/blob", "local/blob", "drive9-home/config"} {
		mustWrite(t, filepath.Join(legacy, filepath.FromSlash(excluded)), "excluded", 0o600)
	}

	result, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "migrated" || result.Source != legacy {
		t.Fatalf("unexpected result: %#v", result)
	}
	newRoot := filepath.Join(home, store.TIDirName)
	credentials, err := os.ReadFile(filepath.Join(newRoot, "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(credentials), "tidb_cloud_public_key") || strings.Contains(string(credentials), "tdc_public_key") {
		t.Fatalf("credentials were not normalized:\n%s", credentials)
	}
	identity, err := os.ReadFile(filepath.Join(newRoot, ".telemetry-installation-id"))
	if err != nil || strings.TrimSpace(string(identity)) != "tdc_0123456789abcdefghijkl" {
		t.Fatalf("legacy identity changed: %q, %v", identity, err)
	}
	for _, copied := range []string{"config", "credentials", ".preferences", ".telemetry-installation-id", "db_users/cluster-1/credentials", "fs_resources/profile/resource/config", "fs_resources/profile/resource/credentials", "fs_credentials/resource/credentials"} {
		if _, err := os.Stat(filepath.Join(newRoot, filepath.FromSlash(copied))); err != nil {
			t.Fatalf("missing migrated %s: %v", copied, err)
		}
	}
	for _, excluded := range []string{"logs", "cache", "local", "mounts", "drive9-home"} {
		if _, err := os.Stat(filepath.Join(newRoot, excluded)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded path %s was migrated: %v", excluded, err)
		}
	}
	if _, err := os.Stat(filepath.Join(legacy, "credentials")); err != nil {
		t.Fatalf("legacy source was modified: %v", err)
	}
	result, err = Ensure(home)
	if err != nil || result.Status != "not_needed" {
		t.Fatalf("published migration should remain usable while legacy state is retained: %#v, %v", result, err)
	}
}

func TestEnsureRejectsUntrustedMigrationMarker(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	destination := filepath.Join(home, store.TIDirName)
	mustMkdir(t, legacy, 0o700)
	mustWrite(t, filepath.Join(destination, migrationMarkerName), `{"schema_version":1,"source":"/different/.tdc"}`, 0o600)
	_, err := Ensure(home)
	if apperr.CodeFor(err) != "config.home_migration_conflict" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureRejectsUnsafeSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode and symlink assertions")
	}
	t.Run("credential permissions", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, LegacyDirName)
		mustWrite(t, filepath.Join(legacy, "credentials"), "[default]\ntdc_public_key='public'\ntdc_private_key='private'\n", 0o644)
		_, err := Ensure(home)
		if apperr.CodeFor(err) != "config.home_migration_unsafe_source" {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, LegacyDirName)
		mustMkdir(t, legacy, 0o700)
		target := filepath.Join(home, "outside")
		mustWrite(t, target, "secret", 0o600)
		if err := os.Symlink(target, filepath.Join(legacy, "credentials")); err != nil {
			t.Fatal(err)
		}
		_, err := Ensure(home)
		if apperr.CodeFor(err) != "config.home_migration_unsafe_source" {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestEnsureRejectsConflictingCredentialFields(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	mustWrite(t, filepath.Join(legacy, "credentials"), "[default]\ntidb_cloud_public_key='new'\ntdc_public_key='old'\ntidb_cloud_private_key='same'\ntdc_private_key='same'\n", 0o600)
	_, err := Ensure(home)
	if apperr.CodeFor(err) != "config.environment_conflict" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, store.TIDirName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed migration published destination: %v", statErr)
	}
}

func TestEnsureRejectsActiveLegacyMount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness check is Unix-only")
	}
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	mountPath := filepath.Join(home, "workspace")
	mustMkdir(t, mountPath, 0o700)
	mustWrite(t, filepath.Join(legacy, "mounts", "active.json"), `{"mount_path":`+strconv.Quote(mountPath)+`,"pid":`+strconv.Itoa(os.Getpid())+`}`, 0o600)
	_, err := Ensure(home)
	if apperr.CodeFor(err) != "config.home_migration_active_mount" {
		t.Fatalf("unexpected error: %v", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "tdc fs drain-file-system") || !strings.Contains(message, "tdc fs unmount-file-system") {
		t.Fatalf("missing old-command instructions: %s", message)
	}
}

func TestEnsureFailureLeavesNoDestination(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, LegacyDirName)
	mustWrite(t, filepath.Join(legacy, "config"), "[default]\nregion_code='aws-us-east-1'\n", 0o644)
	_, err := ensure(home, options{
		copyRegular: func(string, string, os.FileMode) error { return errors.New("injected copy failure") },
	})
	if apperr.CodeFor(err) != "config.home_migration_failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoPublishedOrStagedState(t, home)

	_, err = ensure(home, options{beforePublish: func(string) error { return errors.New("injected publish failure") }})
	if apperr.CodeFor(err) != "config.home_migration_failed" {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoPublishedOrStagedState(t, home)
}

func assertNoPublishedOrStagedState(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, store.TIDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed migration published destination: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".ti.migrate-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("migration staging directories remain: %#v, %v", matches, err)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
}
