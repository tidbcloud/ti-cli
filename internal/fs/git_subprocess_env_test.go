package fs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGitSubprocessesDoNotInheritCredentials covers the reported leak: git runs
// hooks, clean/smudge filters, and configured helpers in the child environment,
// so a ti credential left in os.Environ() would be readable by repository- or
// user-controlled code. The shim records the environment git actually receives.
func TestGitSubprocessesDoNotInheritCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	shimDir := t.TempDir()
	shim := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nenv > \"$GIT_ENV_DUMP\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TIDB_CLOUD_PUBLIC_KEY", "org-public")
	t.Setenv("TIDB_CLOUD_PRIVATE_KEY", "org-private")
	t.Setenv("TI_FS_TOKEN", "fs-bearer")
	t.Setenv("TI_FS_AI_PROVIDER_API_KEY", "provider-secret")
	t.Setenv("TI_VAULT_TOKEN", "vault-token")
	t.Setenv("DRIVE9_API_KEY", "drive9-bearer")
	t.Setenv("GIT_SUBPROCESS_SAFE_VAR", "kept")

	for _, tc := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"runGit", func(ctx context.Context) error { return runGit(ctx, "", "status") }},
		{"gitOutput", func(ctx context.Context) error {
			if _, err := gitOutput(ctx, ".", "status"); err != nil {
				return err
			}
			return nil
		}},
		{"gitCommandOutput", func(ctx context.Context) error {
			if _, err := gitCommandOutput(ctx, "status"); err != nil {
				return err
			}
			return nil
		}},
		{"unpackGitObjectPack", func(ctx context.Context) error {
			return unpackGitObjectPack(ctx, t.TempDir(), []byte("pack"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dump := filepath.Join(t.TempDir(), "env.txt")
			t.Setenv("GIT_ENV_DUMP", dump)
			if err := tc.run(context.Background()); err != nil {
				t.Fatalf("%s failed: %v", tc.name, err)
			}
			values := gitEnvDump(t, dump)
			for _, key := range []string{
				"TIDB_CLOUD_PUBLIC_KEY", "TIDB_CLOUD_PRIVATE_KEY", "TI_FS_TOKEN",
				"TI_FS_AI_PROVIDER_API_KEY", "TI_VAULT_TOKEN", "DRIVE9_API_KEY",
			} {
				if _, ok := values[key]; ok {
					t.Fatalf("%s handed %s to git", tc.name, key)
				}
			}
			if values["GIT_SUBPROCESS_SAFE_VAR"] != "kept" {
				t.Fatalf("%s did not pass the ordinary environment through: %#v", tc.name, values)
			}
			if values["PATH"] == "" {
				t.Fatalf("%s dropped PATH: %#v", tc.name, values)
			}
		})
	}
}

// gitEnvDump reads the environment a shim git recorded.
func gitEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shim environment: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	if len(values) == 0 {
		t.Fatal("shim recorded no environment")
	}
	return values
}
