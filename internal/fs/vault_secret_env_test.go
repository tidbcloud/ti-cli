package fs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

// TestVaultSecretChildEnvRejectsProcessConfigurationKeys covers the injection
// finding: a vault field may carry a secret value, but a field name that
// configures how the child resolves programs or loads code would let the secret's
// author pick what the child executes, so the name is rejected instead of
// injected. Case variants must be caught too, because normalization upper-cases
// the name before the denylist is consulted.
func TestVaultSecretChildEnvRejectsProcessConfigurationKeys(t *testing.T) {
	for _, field := range []string{
		"PATH",
		"path",
		"Path",
		"LD_PRELOAD",
		"ld_preload",
		"LD_LIBRARY_PATH",
		"DYLD_INSERT_LIBRARIES",
		"BASH_ENV",
		"ENV",
		"IFS",
		"PYTHONPATH",
		"NODE_OPTIONS",
		"JAVA_TOOL_OPTIONS",
		"SSH_AUTH_SOCK",
		"GIT_SSH_COMMAND",
		"GIT_CONFIG_GLOBAL",
		"PAGER",
		"COMSPEC",
		"PSModulePath",
		// Lua reads the versioned spellings before the unsuffixed ones, so the
		// trailing-version rule has to resolve them to the same entry; the Solaris
		// loader has the same shape with LD_PRELOAD_32.
		"LUA_INIT_5_4",
		"lua_init_5_4",
		"LUA_PATH_5_4",
		"LUA_CPATH_5_1",
		"LD_PRELOAD_32",
		"LD_LIBRARY_PATH_64",
		// Prefix families whose suffix is chosen by the field name.
		"DYLD_PRINT_LIBRARIES",
		"dYlD_pRiNt_lIbRaRiEs",
		"LD_32_PRELOAD",
		"LD_64_LIBRARY_PATH",
		"GIT_CONFIG_KEY_0",
		"GIT_CONFIG_VALUE_0",
		// Same class as the entries above, added after the second review round.
		"JDK_JAVA_OPTIONS",
		"IBM_JAVA_OPTIONS",
		"OPENJ9_JAVA_OPTIONS",
		"GIT_CONFIG_PARAMETERS",
		"GIT_EXTERNAL_DIFF",
		"GIT_DIR",
		"GIT_COMMON_DIR",
		"GIT_WORK_TREE",
		"PYTHONUSERBASE",
		"PYTHONBREAKPOINT",
		"OPENSSL_CONF",
		"OPENSSL_ENGINES",
		"OPENSSL_MODULES",
		"DOTNET_STARTUP_HOOKS",
		"CORECLR_PROFILER",
		"COR_PROFILER_PATH",
		"GLIBC_TUNABLES",
		"LD_ORIGIN_PATH",
		"PERL5SHELL",
		"FPATH",
		"SUDO_ASKPASS",
	} {
		_, err := vaultSecretChildEnv(nil, map[string]string{field: "attacker-controlled"})
		if apperr.CodeFor(err) != "vault.env_key_forbidden" {
			t.Fatalf("field %q error = %v, want vault.env_key_forbidden", field, err)
		}
		message := apperr.MessageFor(err)
		if !strings.Contains(message, "rename those fields") {
			t.Fatalf("field %q error is not actionable: %q", field, message)
		}
		if exit := apperr.ExitCodeFor(err); exit != 2 {
			t.Fatalf("field %q exit code = %d, want 2", field, exit)
		}
	}
}

// TestVaultSecretChildEnvAllowsSecretLikeKeys pins the other side of the exact-name
// rule: plausible secret field names, including ones that share a prefix with a
// rejected name or end in a version-looking segment, stay injectable.
func TestVaultSecretChildEnvAllowsSecretLikeKeys(t *testing.T) {
	fields := map[string]string{
		"DB_URL":            "mysql://user:pass@host/db",
		"PGPASSWORD":        "s3cr3t",
		"SSH_PRIVATE_KEY":   "-----BEGIN OPENSSH PRIVATE KEY-----",
		"GIT_TOKEN":         "ghp_example",
		"LD_PRELOAD_BACKUP": "not-a-loader-variable",
		"PATH_EXTRA":        "/opt/bin",
		// The trailing-version rule only applies to a denied base name.
		"LUA_VERSION":  "5.4",
		"DB_URL_2":     "mysql://user:pass@host/db2",
		"PGPASSWORD_5": "s3cr3t-5",
		// Neither the exact-name table nor the prefix families cover these.
		"GIT_DIR_BACKUP": "/tmp/backup",
		"OPENSSL_CERT":   "/tmp/cert.pem",
	}
	env, err := vaultSecretChildEnv(nil, fields)
	if err != nil {
		t.Fatalf("secret-like fields rejected: %v", err)
	}
	values := environmentMapFS(env)
	for key, want := range fields {
		if values[key] != want {
			t.Fatalf("field %q was not injected: %#v", key, values)
		}
	}
}

// TestTrimVaultEnvVersionSuffix pins the version-segment stripping edges: only
// all-digit segments are removed, the name is never reduced to nothing, and a name
// without such a segment reports no change.
func TestTrimVaultEnvVersionSuffix(t *testing.T) {
	for _, tc := range []struct {
		key      string
		wantBase string
		wantTrim bool
	}{
		{"LUA_INIT_5_4", "LUA_INIT", true},
		{"LUA_PATH_5_1", "LUA_PATH", true},
		{"LUA_CPATH_10", "LUA_CPATH", true},
		{"LUA_INIT", "LUA_INIT", false},
		{"LUA_INIT_", "LUA_INIT_", false},
		{"_9LIVES", "_9LIVES", false},
		{"DB_URL_2", "DB_URL", true},
		{"PATH", "PATH", false},
	} {
		base, trimmed := trimVaultEnvVersionSuffix(tc.key)
		if base != tc.wantBase || trimmed != tc.wantTrim {
			t.Fatalf("trimVaultEnvVersionSuffix(%q) = (%q, %v), want (%q, %v)", tc.key, base, trimmed, tc.wantBase, tc.wantTrim)
		}
	}
}

// TestVaultSecretChildEnvForbiddenKeyMessageIsStable pins the deterministic error
// text when a secret carries several rejected names: every rejected name is
// reported once, in sorted order, so the message is stable across map iteration.
func TestVaultSecretChildEnvForbiddenKeyMessageIsStable(t *testing.T) {
	message := ""
	for i := 0; i < 50; i++ {
		_, err := vaultSecretChildEnv(nil, map[string]string{"PATH": "a", "LD_PRELOAD": "b", "IFS": "c"})
		if apperr.CodeFor(err) != "vault.env_key_forbidden" {
			t.Fatalf("forbidden key error = %v", err)
		}
		got := apperr.MessageFor(err)
		if message == "" {
			message = got
			continue
		}
		if got != message {
			t.Fatalf("forbidden key message changed across runs: %q vs %q", message, got)
		}
	}
	if !strings.Contains(message, `"IFS" (shell word splitting), "LD_PRELOAD" (the dynamic loader), "PATH" (the child's command lookup)`) {
		t.Fatalf("forbidden key message does not list every rejected name in sorted order: %q", message)
	}
	if strings.Count(message, `"PATH"`) != 1 {
		t.Fatalf("forbidden key message repeats a rejected name: %q", message)
	}
}

// TestRunVaultSecretCommandRefusesProtectedFieldBeforeSpawn proves the rejection
// happens before the child exists: the command must not run at all.
func TestRunVaultSecretCommandRefusesProtectedFieldBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	marker := filepath.Join(t.TempDir(), "spawned")
	err := runVaultSecretCommand(context.Background(), VaultRunWithSecretOptions{
		Command: []string{"/bin/sh", "-c", "touch " + marker},
	}, map[string]string{"LD_PRELOAD": "/tmp/evil.so"})
	if apperr.CodeFor(err) != "vault.env_key_forbidden" {
		t.Fatalf("error code = %q: %v", apperr.CodeFor(err), err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("child ran with a protected environment key injected: %v", statErr)
	}
}

// TestVaultSecretChildEnvCannotExportShellFunctions pins why the denylist needs no
// BASH_FUNC_*%% entry: the normalizer rewrites '%', so an injected field can never
// reach bash in the exported-function shape.
func TestVaultSecretChildEnvCannotExportShellFunctions(t *testing.T) {
	env, err := vaultSecretChildEnv(nil, map[string]string{"BASH_FUNC_x%%": "() { :; }"})
	if err != nil {
		t.Fatalf("exported-function field rejected: %v", err)
	}
	values := environmentMapFS(env)
	if values["BASH_FUNC_X__"] != "() { :; }" {
		t.Fatalf("BASH_FUNC field was not normalized: %#v", values)
	}
	for key := range values {
		if strings.Contains(key, "%") {
			t.Fatalf("child env key %q can still carry the exported-function marker: %#v", key, values)
		}
	}
}

// TestVaultSecretChildEnvCollisionMessageIsStable pins the deterministic error
// text: the colliding pair is reported in sorted order instead of map order.
func TestVaultSecretChildEnvCollisionMessageIsStable(t *testing.T) {
	message := ""
	for i := 0; i < 50; i++ {
		_, err := vaultSecretChildEnv(nil, map[string]string{"db_url": "a", "DB_URL": "b"})
		if apperr.CodeFor(err) != "vault.env_collision" {
			t.Fatalf("collision error = %v", err)
		}
		got := apperr.MessageFor(err)
		if message == "" {
			message = got
			continue
		}
		if got != message {
			t.Fatalf("collision message changed across runs: %q vs %q", message, got)
		}
	}
	if !strings.Contains(message, `"DB_URL" and "db_url"`) {
		t.Fatalf("collision message is not sorted: %q", message)
	}
}

// TestVaultSecretChildEnvDropsCaseVariantInheritedKey covers the Windows
// shadowing case: env names fold case-insensitively there, so an ambient db_url
// must not survive next to the injected DB_URL.
func TestVaultSecretChildEnvDropsCaseVariantInheritedKey(t *testing.T) {
	previous := envKeysFoldCase
	envKeysFoldCase = true
	t.Cleanup(func() { envKeysFoldCase = previous })

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

// TestVaultSecretChildEnvPreservesCaseDistinctKeysOnUnix pins the other side of
// that rule: Unix environment names are case-sensitive, so an injected HTTP_PROXY
// must not delete an inherited http_proxy (curl reads only the lowercase form).
func TestVaultSecretChildEnvPreservesCaseDistinctKeysOnUnix(t *testing.T) {
	previous := envKeysFoldCase
	envKeysFoldCase = false
	t.Cleanup(func() { envKeysFoldCase = previous })

	env, err := vaultSecretChildEnv(
		[]string{"PATH=/bin", "http_proxy=http://proxy.example:8080"},
		map[string]string{"HTTP_PROXY": "http://secret-proxy:3128"},
	)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMapFS(env)
	if values["http_proxy"] != "http://proxy.example:8080" || values["HTTP_PROXY"] != "http://secret-proxy:3128" {
		t.Fatalf("case-distinct variables were not both preserved: %#v", values)
	}
}

// TestVaultSecretChildEnvNormalizesNonASCIIKeys matches the companion's byte-wise
// normalization: "café" is two bytes after the accented letter and therefore
// exports as CAF__, while a single underscore stays distinct.
func TestVaultSecretChildEnvNormalizesNonASCIIKeys(t *testing.T) {
	env, err := vaultSecretChildEnv(nil, map[string]string{"café": "x"})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMapFS(env)
	if values["CAF__"] != "x" || len(values) != 1 {
		t.Fatalf("non-ASCII field normalized to %#v, want CAF__", values)
	}

	// The companion exports "é" as "__" and "_" as "_", so these do not collide.
	env, err = vaultSecretChildEnv(nil, map[string]string{"é": "a", "_": "b"})
	if err != nil {
		t.Fatalf("distinct non-ASCII names rejected: %v", err)
	}
	values = environmentMapFS(env)
	if values["__"] != "a" || values["_"] != "b" {
		t.Fatalf("non-ASCII collision handling diverged: %#v", values)
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
	// Unix keeps case-distinct names, so the ambient db_url survives while the
	// injected DB_URL is added; only Windows folds them together.
	if !strings.Contains(output, "db_url=ambient") {
		t.Fatalf("case-distinct ambient key was dropped on Unix:\n%s", output)
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
