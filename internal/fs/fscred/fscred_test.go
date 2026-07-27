package fscred

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/config/store"
)

func TestRegistryStoreListAndFileModes(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	dir, err := resourceDir(home, profile.Name, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, store.TDCDirName, resourcesDirName),
		profileDir(home, profile.Name),
		dir,
	} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "scratch", "tenant-2", "aws", "aws-us-west-2", "key-2"); err != nil {
		t.Fatal(err)
	}
	resources, err := List(home, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].Name != "scratch" || resources[1].Name != "workspace" {
		t.Fatalf("unexpected resources: %#v", resources)
	}
	if _, err := os.Stat(store.ConfigPath(home)); !os.IsNotExist(err) {
		t.Fatalf("registry store wrote main config: %v", err)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, filepath.Join(home, store.TDCDirName, resourcesDirName), 0o700)
		assertMode(t, profileDir(home, profile.Name), 0o700)
		assertMode(t, dir, 0o700)
		assertMode(t, filepath.Join(dir, configFileName), 0o644)
		assertMode(t, filepath.Join(dir, credsFileName), 0o600)
	}
	configData, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "key-1") || strings.Contains(string(configData), "api_key") {
		t.Fatalf("resource config leaked API key: %s", configData)
	}
	if filepath.Base(dir) == "workspace" || filepath.Base(filepath.Dir(dir)) == profile.Name {
		t.Fatalf("profile and resource path segments must be encoded: %s", dir)
	}
}

func TestResolveSelectionPrecedence(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if err := Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "scratch", "tenant-2", "aws", "aws-us-west-2", "key-2"); err != nil {
		t.Fatal(err)
	}
	selected, _, err := Resolve(home, profile, "scratch", true, map[string]string{"TDC_FS_FILE_SYSTEM_NAME": "workspace"})
	if err != nil || selected.FSResourceName != "scratch" || selected.FSAPIKey != "key-2" || selected.FSRegionCode != "us-west-2" {
		t.Fatalf("flag selection failed: selected=%#v err=%v", selected, err)
	}
	selected, _, err = Resolve(home, profile, "", false, map[string]string{"TDC_FS_FILE_SYSTEM_NAME": "scratch"})
	if err != nil || selected.FSResourceName != "scratch" {
		t.Fatalf("environment selection failed: selected=%#v err=%v", selected, err)
	}
	if _, _, err := Resolve(home, profile, "", false, map[string]string{}); apperr.CodeFor(err) != "fs.missing_file_system_name" {
		t.Fatalf("expected missing selector error, got %v", err)
	}
	if _, _, err := Resolve(home, profile, "missing", true, map[string]string{}); apperr.CodeFor(err) != "fs.resource_not_found" {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestResolveNeverInfersResourceFromRegistryCardinality(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if _, _, err := Resolve(home, profile, "", false, map[string]string{}); apperr.CodeFor(err) != "fs.missing_file_system_name" {
		t.Fatalf("zero-resource error = %v", err)
	}
	if err := Store(home, profile, "only", "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve(home, profile, "", false, map[string]string{}); apperr.CodeFor(err) != "fs.missing_file_system_name" {
		t.Fatalf("one-resource error = %v", err)
	}
	if err := Store(home, profile, "second", "tenant-2", "aws", "aws-us-east-1", "key-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve(home, profile, "", false, map[string]string{}); apperr.CodeFor(err) != "fs.missing_file_system_name" {
		t.Fatalf("multiple-resource error = %v", err)
	}
}

func TestResolveAuthenticatedConfigurationFree(t *testing.T) {
	home := t.TempDir()
	profile := &config.Profile{Name: "ephemeral", HomeDir: home}
	selected, resource, err := ResolveAuthenticated(home, profile, ResolveAuthOptions{
		TokenRequired: true,
		Env: map[string]string{
			"TDC_FS_FILE_SYSTEM_NAME": "workspace",
			"TDC_FS_TOKEN":            "drive9-secret",
			"TDC_REGION_CODE":         "aws-us-east-1",
		},
		RegionOverride: "aws-us-east-1",
	})
	if err != nil {
		t.Fatalf("ResolveAuthenticated failed: %v", err)
	}
	if selected.FSResourceName != "workspace" || selected.FSAPIKey != "drive9-secret" || selected.FSPlacementRegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected selected profile: %#v", selected)
	}
	if resource.TenantID != "" || !resource.HasAPIKey {
		t.Fatalf("unexpected ephemeral resource: %#v", resource)
	}
	if _, err := os.Stat(filepath.Join(home, store.TDCDirName)); !os.IsNotExist(err) {
		t.Fatalf("configuration-free resolution wrote local state: %v", err)
	}
}

func TestResolveAuthenticatedPrecedenceAndMixedSources(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if err := Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-west-2", "stored-token"); err != nil {
		t.Fatal(err)
	}
	profile.PlacementRegionCode = "aws-eu-central-1"

	selected, _, err := ResolveAuthenticated(home, profile, ResolveAuthOptions{
		Selector:         "workspace",
		SelectorExplicit: true,
		Token:            "flag-token",
		TokenExplicit:    true,
		RegionOverride:   "aws-ap-southeast-1",
		TokenRequired:    true,
		Env: map[string]string{
			"TDC_FS_TOKEN":    "env-token",
			"TDC_REGION_CODE": "aws-us-east-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.FSAPIKey != "flag-token" || selected.FSPlacementRegionCode != "aws-ap-southeast-1" {
		t.Fatalf("flag precedence failed: %#v", selected)
	}

	selected, _, err = ResolveAuthenticated(home, profile, ResolveAuthOptions{
		TokenRequired: true,
		Env: map[string]string{
			"TDC_FS_FILE_SYSTEM_NAME": "workspace",
			"TDC_FS_TOKEN":            "env-token",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.FSAPIKey != "env-token" || selected.FSPlacementRegionCode != "aws-us-west-2" {
		t.Fatalf("mixed env/registry resolution failed: %#v", selected)
	}
}

func TestResolveAuthenticatedErrorsAndDelegatedTokenMode(t *testing.T) {
	profile := &config.Profile{Name: "ephemeral", HomeDir: t.TempDir()}
	tests := []struct {
		name string
		opts ResolveAuthOptions
		code string
	}{
		{
			name: "missing name",
			opts: ResolveAuthOptions{TokenRequired: true, RegionOverride: "aws-us-east-1", Env: map[string]string{"TDC_FS_TOKEN": "token"}},
			code: "fs.missing_file_system_name",
		},
		{
			name: "unregistered resource without token",
			opts: ResolveAuthOptions{Selector: "workspace", SelectorExplicit: true, TokenRequired: true, RegionOverride: "aws-us-east-1"},
			code: "fs.resource_not_found",
		},
		{
			name: "missing region",
			opts: ResolveAuthOptions{Selector: "workspace", SelectorExplicit: true, Token: "token", TokenExplicit: true, TokenRequired: true},
			code: "fs.missing_region",
		},
		{
			name: "empty selector flag",
			opts: ResolveAuthOptions{SelectorExplicit: true, Token: "token", TokenExplicit: true, TokenRequired: true, RegionOverride: "aws-us-east-1", Env: map[string]string{"TDC_FS_FILE_SYSTEM_NAME": "workspace"}},
			code: "fs.empty_file_system_name",
		},
		{
			name: "empty token flag",
			opts: ResolveAuthOptions{Selector: "workspace", SelectorExplicit: true, TokenExplicit: true, TokenRequired: true, RegionOverride: "aws-us-east-1"},
			code: "fs.empty_token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ResolveAuthenticated(profile.HomeDir, profile, tt.opts)
			if apperr.CodeFor(err) != tt.code {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
		})
	}

	selected, _, err := ResolveAuthenticated(profile.HomeDir, profile, ResolveAuthOptions{
		Selector:         "workspace",
		SelectorExplicit: true,
		RegionOverride:   "aws-us-east-1",
		TokenRequired:    false,
	})
	if err != nil {
		t.Fatalf("delegated token mode failed: %v", err)
	}
	if selected.FSAPIKey != "" {
		t.Fatalf("delegated token mode unexpectedly selected owner token")
	}
}

func TestLegacyFlatResourceMigration(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant-1",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, store.CredentialsProfile{TDCPublicKey: "public", TDCPrivateKey: "private", FSAPIKey: "key-1"}); err != nil {
		t.Fatal(err)
	}
	profile := registryProfile(home)
	profile.FSResourceName = "workspace"
	profile.FSTenantID = "tenant-1"
	profile.FSCloudProvider = "aws"
	profile.FSPlacementRegionCode = "aws-us-east-1"
	profile.FSRegionCode = "us-east-1"
	profile.FSAPIKey = "key-1"
	if err := MigrateLegacy(home, profile); err != nil {
		t.Fatal(err)
	}
	resource, err := Get(home, "stage", "workspace")
	if err != nil || resource.APIKey != "key-1" {
		t.Fatalf("migration resource failed: resource=%#v err=%v", resource, err)
	}
	configDoc, _ := store.ReadConfig(home)
	credentialsDoc, _ := store.ReadCredentials(home)
	if got := configDoc["stage"]; got.FSResourceName != "" || got.FSTenantID != "" {
		t.Fatalf("legacy config not cleared: %#v", got)
	}
	if got := credentialsDoc["stage"]; got.FSAPIKey != "" || got.TDCPublicKey != "public" {
		t.Fatalf("legacy credentials not cleared safely: %#v", got)
	}
}

func TestIncompleteLegacyResourceFails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Profile)
	}{
		{name: "name", mutate: func(profile *config.Profile) { profile.FSResourceName = "" }},
		{name: "tenant", mutate: func(profile *config.Profile) { profile.FSTenantID = "" }},
		{name: "placement", mutate: func(profile *config.Profile) { profile.FSPlacementRegionCode = "" }},
		{name: "api key", mutate: func(profile *config.Profile) { profile.FSAPIKey = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := registryProfile(t.TempDir())
			profile.FSResourceName = "workspace"
			profile.FSTenantID = "tenant-1"
			profile.FSCloudProvider = "aws"
			profile.FSPlacementRegionCode = "aws-us-east-1"
			profile.FSRegionCode = "us-east-1"
			profile.FSAPIKey = "key-1"
			tt.mutate(profile)
			if err := MigrateLegacy(profile.HomeDir, profile); apperr.CodeFor(err) != "fs.resource_credentials_incomplete" {
				t.Fatalf("expected incomplete credentials error, got %v", err)
			}
		})
	}
}

func TestRegistryRequiresConfigAndCredentialsPair(t *testing.T) {
	for _, missing := range []string{"config", "credentials"} {
		t.Run(missing, func(t *testing.T) {
			home := t.TempDir()
			dir, err := resourceDir(home, "stage", "workspace")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if missing != "config" {
				if err := writeTOML(filepath.Join(dir, configFileName), Resource{Name: "workspace", TenantID: "tenant-1", CloudProvider: "aws", RegionCode: "aws-us-east-1"}, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if missing != "credentials" {
				if err := writeTOML(filepath.Join(dir, credsFileName), credentials{APIKey: "key-1"}, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Get(home, "stage", "workspace"); apperr.CodeFor(err) != "fs.resource_credentials_incomplete" {
				t.Fatalf("expected incomplete registry error, got %v", err)
			}
		})
	}
}

func TestRegistryRejectsInvalidPlacementMetadata(t *testing.T) {
	for _, tt := range []struct {
		name          string
		cloudProvider string
		placementCode string
	}{
		{name: "missing provider", placementCode: "aws-us-east-1"},
		{name: "invalid region", cloudProvider: "aws", placementCode: "aws-missing-1"},
		{name: "provider mismatch", cloudProvider: "alibaba_cloud", placementCode: "aws-us-east-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			dir, err := resourceDir(home, "stage", "workspace")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := writeTOML(filepath.Join(dir, configFileName), Resource{
				Name:          "workspace",
				TenantID:      "tenant-1",
				CloudProvider: tt.cloudProvider,
				RegionCode:    tt.placementCode,
			}, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeTOML(filepath.Join(dir, credsFileName), credentials{APIKey: "key-1"}, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Get(home, "stage", "workspace"); apperr.CodeFor(err) != "fs.resource_credentials_incomplete" {
				t.Fatalf("expected invalid placement error, got %v", err)
			}
		})
	}
}

func TestResolveDryRunUsesLegacyResourceWithoutMigrating(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	profile.FSResourceName = "workspace"
	profile.FSTenantID = "tenant-1"
	profile.FSCloudProvider = "aws"
	profile.FSPlacementRegionCode = "aws-us-east-1"
	profile.FSRegionCode = "us-east-1"
	profile.FSAPIKey = "key-1"
	selected, _, err := ResolveDryRun(home, profile, "workspace", true, nil)
	if err != nil || selected.FSAPIKey != "key-1" {
		t.Fatalf("dry-run legacy selection failed: selected=%#v err=%v", selected, err)
	}
	if _, err := os.Stat(filepath.Join(home, store.TDCDirName, resourcesDirName)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created registry state: %v", err)
	}
}

func TestLegacyMigrationRejectsRegistryMismatch(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if err := Store(home, profile, "workspace", "tenant-new", "aws", "aws-us-east-1", "key-new"); err != nil {
		t.Fatal(err)
	}
	profile.FSResourceName = "workspace"
	profile.FSTenantID = "tenant-old"
	profile.FSCloudProvider = "aws"
	profile.FSPlacementRegionCode = "aws-us-east-1"
	profile.FSRegionCode = "us-east-1"
	profile.FSAPIKey = "key-old"
	if err := MigrateLegacy(home, profile); apperr.CodeFor(err) != "fs.resource_migration_failed" {
		t.Fatalf("expected migration mismatch error, got %v", err)
	}
	resource, err := Get(home, profile.Name, "workspace")
	if err != nil || resource.APIKey != "key-new" {
		t.Fatalf("registry resource changed: resource=%#v err=%v", resource, err)
	}
}

func TestDeletePreservesOtherResourceWithoutChangingMainConfig(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if err := store.WriteProfile(home, profile.Name, store.ConfigProfile{RegionCode: "aws-us-east-1", ProjectID: "project-1"}, store.CredentialsProfile{}); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "scratch", "tenant-2", "aws", "aws-us-west-2", "key-2"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(home, profile, "workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(home, profile.Name, "workspace"); apperr.CodeFor(err) != "fs.resource_not_found" {
		t.Fatalf("workspace still exists: %v", err)
	}
	if resource, err := Get(home, profile.Name, "scratch"); err != nil || resource.APIKey != "key-2" {
		t.Fatalf("scratch changed: resource=%#v err=%v", resource, err)
	}
	configDoc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := configDoc[profile.Name]; got.RegionCode != "aws-us-east-1" || got.ProjectID != "project-1" {
		t.Fatalf("main config changed: %#v", got)
	}
}

func TestStoreRejectsConflictingResourceName(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	if err := Store(home, profile, "workspace", "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "workspace", "tenant-2", "aws", "aws-us-east-1", "key-2"); apperr.CodeFor(err) != "fs.resource_name_conflict" {
		t.Fatalf("expected resource name conflict, got %v", err)
	}
	resource, err := Get(home, profile.Name, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if resource.TenantID != "tenant-1" || resource.APIKey != "key-1" {
		t.Fatalf("conflicting store changed resource: %#v", resource)
	}
}

func TestResourceNameCannotEscapeRegistry(t *testing.T) {
	home := t.TempDir()
	profile := registryProfile(home)
	name := "../../outside"
	if err := Store(home, profile, name, "tenant-1", "aws", "aws-us-east-1", "key-1"); err != nil {
		t.Fatal(err)
	}
	dir, err := resourceDir(home, profile.Name, name)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, store.TDCDirName, resourcesDirName)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(os.PathSeparator) {
		t.Fatalf("resource escaped registry: dir=%s rel=%s err=%v", dir, rel, err)
	}
}

func registryProfile(home string) *config.Profile {
	return &config.Profile{
		Name:                "stage",
		HomeDir:             home,
		PlacementRegionCode: "aws-us-east-1",
		CloudProvider:       "aws",
		RegionCode:          "us-east-1",
		TDCPublicKey:        "public",
		TDCPrivateKey:       "private",
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
