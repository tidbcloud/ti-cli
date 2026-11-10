package fs

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// TestVaultSecretChildEnvScrubsCredentials covers the reported leak: the child
// spawned by run-with-secret must not inherit the filesystem bearer token, the
// vault token, or the organization API keys.
func TestVaultSecretChildEnvScrubsCredentials(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin",
		"SAFE_VAR=kept",
		"TI_FS_TOKEN=fs-bearer",
		"TI_FS_API_KEY=fs-api-key",
		"TI_FS_AI_PROVIDER_API_KEY=provider-secret",
		"TI_VAULT_TOKEN=vault-token",
		"TIDB_CLOUD_PUBLIC_KEY=org-public",
		"TIDB_CLOUD_PRIVATE_KEY=org-private",
		"TDC_PRIVATE_KEY=legacy-private",
		"DRIVE9_API_KEY=drive9-bearer",
		"DRIVE9_VAULT_TOKEN=drive9-vault",
		// Windows-compatible spellings must be scrubbed too.
		"ti_fs_token=fs-bearer-lower",
		"Tidb_Cloud_Private_Key=org-private-mixed",
	}
	env, err := vaultSecretChildEnv(base, map[string]string{
		"DB_URL":   "mysql://user:pass@host/db",
		"PASSWORD": "s3cr3t",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMapFS(env)
	for _, key := range []string{
		"TI_FS_TOKEN", "TI_FS_API_KEY", "TI_FS_AI_PROVIDER_API_KEY", "TI_VAULT_TOKEN",
		"TIDB_CLOUD_PUBLIC_KEY", "TIDB_CLOUD_PRIVATE_KEY", "TDC_PRIVATE_KEY",
		"DRIVE9_API_KEY", "DRIVE9_VAULT_TOKEN",
		"ti_fs_token", "Tidb_Cloud_Private_Key",
	} {
		if _, ok := values[key]; ok {
			t.Fatalf("child environment kept credential %s", key)
		}
	}
	if values["SAFE_VAR"] != "kept" {
		t.Fatalf("safe variable lost: %#v", values)
	}
	if values["DB_URL"] != "mysql://user:pass@host/db" || values["PASSWORD"] != "s3cr3t" {
		t.Fatalf("secret fields not injected: %#v", values)
	}
	for _, key := range []string{"TI_FS_TOKEN=", "TI_VAULT_TOKEN=", "TIDB_CLOUD_PRIVATE_KEY=", "TI_FS_AI_PROVIDER_API_KEY="} {
		if strings.Contains(strings.Join(env, "\n"), key) {
			t.Fatalf("child environment still contains %s", key)
		}
	}
}

// TestVaultSecretChildEnvNormalizesFieldKeys pins the rule shared with
// `read-secret --format env`: stored field names are normalized into the env
// charset instead of being rejected, and colliding normalizations fail closed.
func TestVaultSecretChildEnvNormalizesFieldKeys(t *testing.T) {
	env, err := vaultSecretChildEnv(nil, map[string]string{"db_url": "mysql://x", "9lives": "y"})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMapFS(env)
	if values["DB_URL"] != "mysql://x" || values["_9LIVES"] != "y" {
		t.Fatalf("field names were not normalized: %#v", values)
	}

	if _, err := vaultSecretChildEnv(nil, map[string]string{"db_url": "a", "DB_URL": "b"}); apperr.CodeFor(err) != "vault.env_collision" {
		t.Fatalf("collision error = %v", err)
	}
	if _, err := vaultSecretChildEnv(nil, map[string]string{"DB_URL": "a\x00b"}); apperr.CodeFor(err) != "vault.invalid_env_value" {
		t.Fatalf("control byte error = %v", err)
	}
}

// TestVaultSecretChildEnvDropsCaseVariantInheritedKey covers the Windows
// shadowing case: env names fold case-insensitively, so an ambient db_url must
// not survive next to the injected DB_URL.
func TestVaultSecretChildEnvDropsCaseVariantInheritedKey(t *testing.T) {
	env, err := vaultSecretChildEnv([]string{"PATH=/bin", "db_url=ambient", "DB_URL=ambient-upper"}, map[string]string{"DB_URL": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMapFS(env)
	if values["DB_URL"] != "secret" {
		t.Fatalf("injected value lost: %#v", values)
	}
	for key := range values {
		if strings.EqualFold(key, "DB_URL") && key != "DB_URL" {
			t.Fatalf("case variant %q survived the merge: %#v", key, values)
		}
	}
	if len(values) != 2 {
		t.Fatalf("unexpected child env: %#v", values)
	}
}

// TestRunVaultSecretCommandDoesNotLeakTokens runs a real child and inspects its
// environment.
func TestRunVaultSecretCommandDoesNotLeakTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	t.Setenv("TI_FS_TOKEN", "fs-bearer")
	t.Setenv("TI_VAULT_TOKEN", "vault-token")
	t.Setenv("TIDB_CLOUD_PRIVATE_KEY", "org-private")
	t.Setenv("DRIVE9_API_KEY", "drive9-bearer")
	t.Setenv("TI_FS_AI_PROVIDER_API_KEY", "provider-secret")
	t.Setenv("db_url", "ambient")
	t.Setenv("RUN_WITH_SECRET_SAFE_VAR", "kept")
	var out bytes.Buffer
	err := runVaultSecretCommand(context.Background(), VaultRunWithSecretOptions{
		Command: []string{"/bin/sh", "-c", "env"},
		Stdout:  &out,
	}, map[string]string{"SECRET_TOKEN": "s3cr3t", "DB_URL": "mysql://secret"})
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.Contains(output, "SECRET_TOKEN=s3cr3t") || !strings.Contains(output, "DB_URL=mysql://secret") {
		t.Fatalf("child did not receive the secret field:\n%s", output)
	}
	if !strings.Contains(output, "RUN_WITH_SECRET_SAFE_VAR=kept") {
		t.Fatalf("child lost the safe environment:\n%s", output)
	}
	if strings.Contains(output, "db_url=ambient") {
		t.Fatalf("case-variant ambient key shadowed the injected value:\n%s", output)
	}
	for _, leak := range []string{"fs-bearer", "vault-token", "org-private", "drive9-bearer", "provider-secret"} {
		if strings.Contains(output, leak) {
			t.Fatalf("child environment leaked %q:\n%s", leak, output)
		}
	}
}

func TestRunVaultSecretCommandPropagatesExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	err := runVaultSecretCommand(context.Background(), VaultRunWithSecretOptions{
		Command: []string{"/bin/sh", "-c", "exit 7"},
	}, map[string]string{"SECRET_TOKEN": "x"})
	if err == nil {
		t.Fatal("nonzero child exit accepted")
	}
	if code := apperr.CodeFor(err); code != "vault.command_failed" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if exit := apperr.ExitCodeFor(err); exit != 7 {
		t.Fatalf("exit code = %d: %v", exit, err)
	}
}

func environmentMapFS(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
