package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestLoadExplicitProfile(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "stage", "aws-us-west-2", "stage-public", "stage-private")

	profile, err := Load(context.Background(), LoadOptions{
		Profile:         "stage",
		ProfileExplicit: true,
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Name != "stage" || profile.Source != "profile" {
		t.Fatalf("unexpected profile identity: %#v", profile)
	}
	if profile.CloudProvider != "aws" || profile.RegionCode != "us-west-2" {
		t.Fatalf("explicit profile did not win over env: %#v", profile)
	}
}

func TestLoadReadsProjectID(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{RegionCode: "aws-us-east-1", ProjectID: "virtual-1"}, store.CredentialsProfile{TiDBCloudPublicKey: "public", TiDBCloudPrivateKey: "private"}); err != nil {
		t.Fatal(err)
	}
	profile, err := Load(context.Background(), LoadOptions{Profile: "stage", ProfileExplicit: true, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProjectID != "virtual-1" {
		t.Fatalf("project id = %q, want virtual-1", profile.ProjectID)
	}
}

func TestLoadRegionOverrideWinsOverExplicitProfile(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, "stage", "aws-us-west-2", "stage-public", "stage-private")

	profile, err := Load(context.Background(), LoadOptions{
		Profile:         "stage",
		ProfileExplicit: true,
		RegionOverride:  "aws-ap-southeast-1",
		HomeDir:         home,
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Name != "stage" || profile.CloudProvider != "aws" || profile.RegionCode != "ap-southeast-1" || profile.PlacementRegionCode != "aws-ap-southeast-1" {
		t.Fatalf("region override did not win over profile: %#v", profile)
	}
	if profile.TiDBCloudPublicKey != "stage-public" || profile.TiDBCloudPrivateKey != "stage-private" {
		t.Fatalf("region override changed credentials: %#v", profile)
	}
}

func TestLoadEnvironmentFallback(t *testing.T) {
	profile, err := Load(context.Background(), LoadOptions{
		HomeDir: t.TempDir(),
		Env: map[string]string{
			"TI_REGION_CODE":         "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Source != "env" {
		t.Fatalf("expected env source, got %#v", profile)
	}
	if profile.TiDBCloudPrivateKey != "env-private" {
		t.Fatalf("env private key not loaded")
	}
}

func TestLoadEnvironmentFallbackUsesRegionOverride(t *testing.T) {
	profile, err := Load(context.Background(), LoadOptions{
		HomeDir:        t.TempDir(),
		RegionOverride: "ali-ap-southeast-1",
		Env: map[string]string{
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Source != "env" || profile.CloudProvider != "alibaba_cloud" || profile.RegionCode != "ap-southeast-1" {
		t.Fatalf("expected env credentials with region override, got %#v", profile)
	}
}

func TestLoadEnvironmentCredentialsUseDefaultProfileNamespace(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, DefaultProfile, store.ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "dropme-fs",
		FSTenantID:      "tenant-1",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  "local-public",
		TiDBCloudPrivateKey: "local-private",
		FSAPIKey:            "fs-secret",
	}); err != nil {
		t.Fatal(err)
	}

	profile, err := Load(context.Background(), LoadOptions{
		HomeDir: home,
		Env: map[string]string{
			"TI_REGION_CODE":         "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Source != "env" || profile.Name != DefaultProfile {
		t.Fatalf("expected env credential source with default profile namespace, got %#v", profile)
	}
	if profile.TiDBCloudPublicKey != "env-public" || profile.TiDBCloudPrivateKey != "env-private" {
		t.Fatalf("expected credentials from environment, got %#v", profile)
	}
	if profile.FSResourceName != "dropme-fs" || profile.FSTenantID != "tenant-1" || profile.FSAPIKey != "fs-secret" {
		t.Fatalf("expected persisted fs resource to be merged, got %#v", profile)
	}
	if profile.FSCloudProvider != "aws" || profile.FSRegionCode != "us-east-1" || profile.FSPlacementRegionCode != "aws-us-east-1" {
		t.Fatalf("expected persisted fs placement to be merged, got %#v", profile)
	}
}

func TestLoadEnvironmentCredentialsUseExplicitProfileNamespace(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{
		RegionCode:      "aws-us-west-2",
		FSResourceName:  "stage-fs",
		FSTenantID:      "tenant-stage",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-west-2",
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  "stage-local-public",
		TiDBCloudPrivateKey: "stage-local-private",
		FSAPIKey:            "stage-fs-secret",
	}); err != nil {
		t.Fatal(err)
	}

	profile, err := Load(context.Background(), LoadOptions{
		Profile:         "stage",
		ProfileExplicit: true,
		HomeDir:         home,
		Env: map[string]string{
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
	})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Source != "env" || profile.Name != "stage" {
		t.Fatalf("expected env credential source with explicit profile namespace, got %#v", profile)
	}
	if profile.TiDBCloudPublicKey != "env-public" || profile.TiDBCloudPrivateKey != "env-private" {
		t.Fatalf("expected credentials from environment, got %#v", profile)
	}
	if profile.FSResourceName != "stage-fs" || profile.FSTenantID != "tenant-stage" || profile.FSAPIKey != "stage-fs-secret" {
		t.Fatalf("expected persisted stage fs resource, got %#v", profile)
	}
	if profile.PlacementRegionCode != "aws-us-west-2" || profile.RegionCode != "us-west-2" {
		t.Fatalf("expected profile region when env region is absent, got %#v", profile)
	}
}

func TestLoadDefaultProfileFallback(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, DefaultProfile, "ali-ap-southeast-1", "public", "private")

	profile, err := Load(context.Background(), LoadOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.Name != DefaultProfile || profile.CloudProvider != "alibaba_cloud" {
		t.Fatalf("default profile not loaded: %#v", profile)
	}
}

func TestLoadRejectsInvalidRegionCombination(t *testing.T) {
	home := t.TempDir()
	writeProfile(t, home, DefaultProfile, "ali-us-east-1", "public", "private")

	_, err := Load(context.Background(), LoadOptions{HomeDir: home})
	if err == nil {
		t.Fatal("expected invalid region to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "unsupported region") {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestLoadMissingCredentialsSuggestsConfigure(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, DefaultProfile, store.ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, store.CredentialsProfile{}); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), LoadOptions{HomeDir: home})
	if err == nil {
		t.Fatal("expected missing credentials to fail")
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "ti configure") || !strings.Contains(message, "~/.ti/credentials") {
		t.Fatalf("missing credentials error did not include guidance: %q", message)
	}
}

func TestLoadRejectsServerURLKeys(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, store.TIDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath(home), []byte(`
[default]
region_code = "aws-us-east-1"
api_endpoint = "https://example.invalid"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.CredentialsPath(home), []byte(`
[default]
tidb_cloud_public_key = "public"
tidb_cloud_private_key = "private"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), LoadOptions{HomeDir: home})
	if err == nil {
		t.Fatal("expected server URL key to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "api_endpoint") {
		t.Fatalf("expected api_endpoint in error, got %q", got)
	}
}

func TestLoadReadsFSCredentials(t *testing.T) {
	home := t.TempDir()
	err := store.WriteProfile(home, DefaultProfile, store.ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
		FSAPIKey:            "fs-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	profile, err := Load(context.Background(), LoadOptions{HomeDir: home})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.FSAPIKey != "fs-secret" || profile.FSResourceName != "workspace" {
		t.Fatalf("fs credentials not loaded: %#v", profile)
	}
}

func TestLoadLocalDoesNotRequireProfileOrTiDBCloudCredentials(t *testing.T) {
	home := t.TempDir()
	profile, err := LoadLocal(context.Background(), LoadOptions{
		HomeDir: home,
		Env: map[string]string{
			"TI_REGION_CODE":        "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY": "ignored-without-private-key",
		},
	})
	if err != nil {
		t.Fatalf("LoadLocal failed: %v", err)
	}
	if profile.Name != DefaultProfile || profile.HomeDir != home || profile.PlacementRegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected local profile: %#v", profile)
	}
	if profile.TiDBCloudPublicKey != "" || profile.TiDBCloudPrivateKey != "" {
		t.Fatalf("local loader must not load TiDB Cloud credentials: %#v", profile)
	}
	if _, err := os.Stat(filepath.Join(home, store.TIDirName)); !os.IsNotExist(err) {
		t.Fatalf("LoadLocal wrote local state: %v", err)
	}
}

func TestLoadLocalAllowsMissingPlacement(t *testing.T) {
	profile, err := LoadLocal(context.Background(), LoadOptions{HomeDir: t.TempDir(), Profile: "ephemeral"})
	if err != nil {
		t.Fatalf("LoadLocal failed: %v", err)
	}
	if profile.Name != "ephemeral" || profile.PlacementRegionCode != "" {
		t.Fatalf("unexpected local profile: %#v", profile)
	}
}

func TestLoadLocalRejectsMalformedEnvironmentPlacement(t *testing.T) {
	_, err := LoadLocal(context.Background(), LoadOptions{
		HomeDir: t.TempDir(),
		Env:     map[string]string{"TI_REGION_CODE": "aws-not-a-region"},
	})
	if apperr.CodeFor(err) != "config.invalid_region" {
		t.Fatalf("error = %v, want config.invalid_region", err)
	}
}

func writeProfile(t *testing.T, home, name, regionCode, publicKey, privateKey string) {
	t.Helper()
	err := store.WriteProfile(home, name, store.ConfigProfile{
		RegionCode: regionCode,
	}, store.CredentialsProfile{
		TiDBCloudPublicKey:  publicKey,
		TiDBCloudPrivateKey: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
}
