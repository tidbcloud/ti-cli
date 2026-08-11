package fscred

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

func TestCredentialStoreAndResolveByID(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	stored, err := StoreCredential(home, profile, "tenant-1", "aws-us-east-1", wrappedToken(t, "tenant-1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FileSystemID != "tenant-1" || !stored.HasLocalToken {
		t.Fatalf("stored credential = %#v", stored)
	}
	paths, err := CredentialPath(home, profile.Name, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %#o", info.Mode().Perm())
	}
	for _, dir := range []string{filepath.Dir(paths.Credentials), filepath.Dir(filepath.Dir(paths.Credentials))} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("credential directory %s mode = %#o", dir, info.Mode().Perm())
		}
	}
	selected, credential, err := ResolveCredential(home, profile, ResolveCredentialOptions{FileSystemID: "tenant-1", FileSystemIDExplicit: true, TokenRequired: true})
	if err != nil {
		t.Fatal(err)
	}
	if selected.FSTenantID != "tenant-1" || selected.FSAPIKey == "" || credential.RegionCode != "aws-us-east-1" {
		t.Fatalf("selected=%#v credential=%#v", selected, credential)
	}
}

func TestResolveCredentialDerivesIDFromExplicitToken(t *testing.T) {
	profile := credentialTestProfile()
	token := wrappedToken(t, "tenant-token")
	selected, credential, err := ResolveCredential(t.TempDir(), profile, ResolveCredentialOptions{
		Token: token, TokenExplicit: true, RegionOverride: "aws-us-east-1", TokenRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.FSTenantID != "tenant-token" || credential.FileSystemID != "tenant-token" || credential.APIKey != token {
		t.Fatalf("selected=%#v credential=%#v", selected, credential)
	}
	_, _, err = ResolveCredential(t.TempDir(), profile, ResolveCredentialOptions{
		FileSystemID: "tenant-other", FileSystemIDExplicit: true, Token: token, TokenExplicit: true, RegionOverride: "aws-us-east-1", TokenRequired: true,
	})
	if apperr.CodeFor(err) != "fs.token_file_system_mismatch" {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestResolveCredentialReportsMissingLocalTokenForKnownID(t *testing.T) {
	_, _, err := ResolveCredential(t.TempDir(), credentialTestProfile(), ResolveCredentialOptions{
		FileSystemID: "tenant-without-token", FileSystemIDExplicit: true, TokenRequired: true,
	})
	if apperr.CodeFor(err) != "auth.missing_fs_api_key" {
		t.Fatalf("missing token error = %v", err)
	}
	if !strings.Contains(err.Error(), "import-file-system-token") || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("missing token error is not actionable: %v", err)
	}
}

func TestFileSystemIDFromTokenRejectsMalformedInputs(t *testing.T) {
	wrapJWT := func(jwt string) string {
		return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
	}
	encoded := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "unknown wrapper", token: "opaque"},
		{name: "invalid wrapper encoding", token: "drive9_%%%"},
		{name: "invalid JWT structure", token: wrapJWT("only.two")},
		{name: "invalid claims JSON", token: wrapJWT("header." + encoded("{") + ".signature")},
		{name: "missing tenant ID", token: wrapJWT("header." + encoded(`{"token_version":1}`) + ".signature")},
		{name: "invalid tenant ID", token: wrapJWT("header." + encoded(`{"tenant_id":"bad id"}`) + ".signature")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FileSystemIDFromToken(tc.token); apperr.CodeFor(err) != "fs.invalid_token" {
				t.Fatalf("error = %v, want fs.invalid_token", err)
			}
		})
	}
}

func TestCredentialConflictAndExplicitReplace(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	first := wrappedToken(t, "tenant-1")
	if _, err := StoreCredential(home, profile, "tenant-1", "aws-us-east-1", first, false); err != nil {
		t.Fatal(err)
	}
	second := first + "changed"
	if _, err := StoreCredential(home, profile, "tenant-1", "aws-us-east-1", second, false); apperr.CodeFor(err) != "fs.credential_import_conflict" {
		t.Fatalf("conflict error = %v", err)
	}
	if _, err := StoreCredential(home, profile, "tenant-1", "aws-us-east-1", second, true); err != nil {
		t.Fatal(err)
	}
	got, err := GetCredential(home, profile.Name, "tenant-1")
	if err != nil || got.APIKey != second {
		t.Fatalf("credential=%#v err=%v", got, err)
	}
}

func TestMigrateNameRegistryPreservesLegacySource(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	token := wrappedToken(t, "tenant-legacy")
	if err := Store(home, profile, "workspace", "tenant-legacy", "aws", "aws-us-east-1", token); err != nil {
		t.Fatal(err)
	}
	if err := MigrateNameRegistry(home, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(home, profile.Name, "workspace"); err != nil {
		t.Fatalf("legacy source removed: %v", err)
	}
	credential, err := GetCredential(home, profile.Name, "tenant-legacy")
	if err != nil || credential.APIKey != token {
		t.Fatalf("credential=%#v err=%v", credential, err)
	}
}

func TestMigrateNameRegistryPreflightsDuplicateIDConflicts(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	if err := Store(home, profile, "first", "tenant-shared", "aws", "aws-us-east-1", wrappedToken(t, "tenant-shared")); err != nil {
		t.Fatal(err)
	}
	if err := Store(home, profile, "second", "tenant-shared", "aws", "aws-us-east-1", wrappedToken(t, "tenant-shared")+"-different"); err != nil {
		t.Fatal(err)
	}
	err := MigrateNameRegistry(home, profile)
	if apperr.CodeFor(err) != "fs.credential_migration_conflict" {
		t.Fatalf("migration error = %v", err)
	}
	if _, err := GetCredential(home, profile.Name, "tenant-shared"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("conflicting migration wrote a destination credential: %v", err)
	}
	if _, err := Get(home, profile.Name, "first"); err != nil {
		t.Fatalf("first legacy source changed: %v", err)
	}
	if _, err := Get(home, profile.Name, "second"); err != nil {
		t.Fatalf("second legacy source changed: %v", err)
	}
}

func TestMigrateNameRegistryMultipleResourcesAliasesAndIdempotency(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	sharedToken := wrappedToken(t, "tenant-shared")
	for _, resource := range []struct {
		name, id, token string
	}{
		{name: "workspace", id: "tenant-shared", token: sharedToken},
		{name: "workspace-alias", id: "tenant-shared", token: sharedToken},
		{name: "scratch", id: "tenant-scratch", token: wrappedToken(t, "tenant-scratch")},
	} {
		if err := Store(home, profile, resource.name, resource.id, "aws", "aws-us-east-1", resource.token); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := MigrateNameRegistry(home, profile); err != nil {
			t.Fatalf("migration pass %d: %v", i+1, err)
		}
	}
	credentials, err := ListCredentials(home, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 2 {
		t.Fatalf("migrated credentials = %#v, want two unique IDs", credentials)
	}
	for _, name := range []string{"workspace", "workspace-alias", "scratch"} {
		if _, err := Get(home, profile.Name, name); err != nil {
			t.Fatalf("legacy source %q was changed: %v", name, err)
		}
	}
}

func TestMigrateNameRegistryDoesNotRestoreDeletedCredential(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	if err := Store(home, profile, "workspace", "tenant-deleted", "aws", "aws-us-east-1", wrappedToken(t, "tenant-deleted")); err != nil {
		t.Fatal(err)
	}
	if err := MigrateNameRegistry(home, profile); err != nil {
		t.Fatal(err)
	}
	if removed, err := DeleteCredential(home, profile.Name, "tenant-deleted"); err != nil || !removed {
		t.Fatalf("delete migrated credential: removed=%t err=%v", removed, err)
	}
	if err := MigrateNameRegistry(home, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := GetCredential(home, profile.Name, "tenant-deleted"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("completed legacy migration restored a deleted credential: %v", err)
	}
	if _, err := Get(home, profile.Name, "workspace"); err != nil {
		t.Fatalf("legacy rollback source was removed: %v", err)
	}
}

func TestMigrateNameRegistryPreflightsDestinationConflictsBeforeAnyWrite(t *testing.T) {
	home := t.TempDir()
	profile := credentialTestProfile()
	if err := Store(home, profile, "first", "tenant-first", "aws", "aws-us-east-1", wrappedToken(t, "tenant-first")); err != nil {
		t.Fatal(err)
	}
	legacyConflictToken := wrappedToken(t, "tenant-conflict")
	if err := Store(home, profile, "conflict", "tenant-conflict", "aws", "aws-us-east-1", legacyConflictToken); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreCredential(home, profile, "tenant-conflict", "aws-us-east-1", legacyConflictToken+"-different", false); err != nil {
		t.Fatal(err)
	}
	if err := MigrateNameRegistry(home, profile); apperr.CodeFor(err) != "fs.credential_migration_conflict" {
		t.Fatalf("migration error = %v", err)
	}
	if _, err := GetCredential(home, profile.Name, "tenant-first"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("preflight conflict allowed a partial migration: %v", err)
	}
}

func wrappedToken(t *testing.T, tenantID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(map[string]any{"tenant_id": tenantID, "token_version": 1, "iat": 1})
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	jwt := header + "." + payload + ".signature"
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(jwt))
}

func credentialTestProfile() *config.Profile {
	return &config.Profile{
		Name: "stage", PlacementRegionCode: "aws-us-east-1", CloudProvider: "aws", RegionCode: "us-east-1",
	}
}
