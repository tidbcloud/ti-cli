package tokenmgmt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

func TestTokenTTLValidation(t *testing.T) {
	t.Parallel()
	valid := []time.Duration{time.Second, 24 * time.Hour, MaxTTL}
	for _, ttl := range valid {
		if _, err := optionalTTLSeconds(&ttl); err != nil {
			t.Fatalf("optionalTTLSeconds(%s): %v", ttl, err)
		}
	}
	invalid := []time.Duration{0, -time.Second, time.Millisecond, MaxTTL + time.Second}
	for _, ttl := range invalid {
		if _, err := optionalTTLSeconds(&ttl); apperr.CodeFor(err) != "fs.token_ttl_invalid" {
			t.Fatalf("optionalTTLSeconds(%s) = %v", ttl, err)
		}
	}
	ttl := time.Hour
	base := GenerateOptions{FileSystemID: "fs-1", TokenName: "name"}
	if _, _, _, err := validateGenerate(base); apperr.CodeFor(err) != "fs.token_lifetime_required" {
		t.Fatalf("missing lifetime = %v", err)
	}
	base.TTL, base.NoExpiration = &ttl, true
	if _, _, _, err := validateGenerate(base); apperr.CodeFor(err) != "fs.token_lifetime_required" {
		t.Fatalf("both lifetimes = %v", err)
	}
	longScopedTTL := MaxTTL + time.Second
	if seconds, err := scopedTTLSeconds(&longScopedTTL); err != nil || seconds == nil || *seconds != int64(longScopedTTL/time.Second) {
		t.Fatalf("scopedTTLSeconds(%s) = %v, %v", longScopedTTL, seconds, err)
	}
}

func TestGenerateAndListMapBackendFieldsAndStoreMetadata(t *testing.T) {
	home := t.TempDir()
	newToken := wrappedToken(t, "fs-1", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TiDBCloud-Public-Key") != "public" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authentication headers: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-1","tenant_id":"fs-1","key_name":"local-owner","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-13T00:00:00Z"}`))
		case "/v1/tokens":
			_, _ = w.Write([]byte(`{"tokens":[{"token_id":"token-1","tenant_id":"fs-1","key_name":"local-owner","scope_kind":"owner","status":"active","expired":false,"issued_at":"2026-08-12T00:00:00Z","expires_at":"2026-08-13T00:00:00Z","created_at":"2026-08-12T00:00:00Z","updated_at":"2026-08-12T00:00:00Z","token":"drive9_malicious"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{FileSystemID: "fs-1", RegionCode: "aws-us-east-1", APIKey: wrappedToken(t, "fs-1", 1), TokenID: "token-old", ScopeKind: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	ttl := time.Hour
	generated, err := service.Generate(context.Background(), GenerateOptions{Profile: profile, FileSystemID: "fs-1", TokenName: "local-owner", TTL: &ttl, StoreLocally: true, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if generated.TokenName != "local-owner" || generated.FSToken != newToken || !generated.CredentialsStored || !strings.Contains(generated.PreviousTokenNote, "remains active") {
		t.Fatalf("generated = %#v", generated)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil {
		t.Fatal(err)
	}
	if credential.TokenID != "token-1" || credential.TokenName != "local-owner" || credential.ScopeKind != "owner" || credential.ExpiresAt == nil {
		t.Fatalf("credential = %#v", credential)
	}
	listed, err := service.List(context.Background(), ListOptions{Profile: profile, FileSystemID: "fs-1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tokens) != 1 || listed.Tokens[0].TokenName != "local-owner" || strings.Contains(listed.Human(), newToken) {
		t.Fatalf("listed = %#v human=%q", listed, listed.Human())
	}
	encoded, err := json.Marshal(listed)
	if err != nil || strings.Contains(string(encoded), "drive9_malicious") {
		t.Fatalf("list output retained unexpected plaintext: %s, %v", encoded, err)
	}
}

func TestOwnerTokenManagesTokenInventoryWithoutExplicitFileSystemID(t *testing.T) {
	home := t.TempDir()
	ownerToken := wrappedToken(t, "fs-1", 1)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+ownerToken {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-TiDBCloud-Public-Key") != "" || r.Header.Get("X-TiDBCloud-Private-Key") != "" {
			t.Errorf("bearer request included TiDB Cloud credentials: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tokens":
			if r.URL.Query().Get("tenant_id") != "fs-1" {
				t.Errorf("list query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"tokens":[{"token_id":"scoped-1","tenant_id":"fs-1","scope_kind":"fs_scoped","status":"active","expired":false,"issued_at":"2026-08-12T00:00:00Z","created_at":"2026-08-12T00:00:00Z","updated_at":"2026-08-12T00:00:00Z"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/scoped-1/deactivate":
			_, _ = w.Write([]byte(`{"token_id":"scoped-1","tenant_id":"fs-1","status":"disabled"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/scoped-1/activate":
			_, _ = w.Write([]byte(`{"token_id":"scoped-1","tenant_id":"fs-1","status":"active"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tokens/scoped-1":
			_, _ = w.Write([]byte(`{"token_id":"scoped-1","tenant_id":"fs-1","status":"revoked"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)

	listed, err := service.List(context.Background(), ListOptions{Profile: profile, Token: ownerToken, TokenExplicit: true, Limit: DefaultListLimit})
	if err != nil || listed.FileSystemID != "fs-1" || len(listed.Tokens) != 1 {
		t.Fatalf("List() result=%#v err=%v", listed, err)
	}
	disabled, err := service.Disable(context.Background(), MutationOptions{Profile: profile, Token: ownerToken, TokenExplicit: true, TokenID: "scoped-1"})
	if err != nil || disabled.FileSystemID != "fs-1" || disabled.Status != "disabled" {
		t.Fatalf("Disable() result=%#v err=%v", disabled, err)
	}
	enabled, err := service.Enable(context.Background(), MutationOptions{Profile: profile, Token: ownerToken, TokenExplicit: true, TokenID: "scoped-1"})
	if err != nil || enabled.FileSystemID != "fs-1" || enabled.Status != "active" {
		t.Fatalf("Enable() result=%#v err=%v", enabled, err)
	}
	deleted, err := service.Delete(context.Background(), MutationOptions{Profile: profile, Token: ownerToken, TokenExplicit: true, TokenID: "scoped-1"})
	if err != nil || deleted.FileSystemID != "fs-1" || deleted.Status != "revoked" {
		t.Fatalf("Delete() result=%#v err=%v", deleted, err)
	}
	if requests != 4 {
		t.Fatalf("requests = %d, want 4", requests)
	}
}

func TestTiDBCloudTokenManagementRequiresExplicitFileSystemID(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	service, profile := tokenTestService(t.TempDir(), server.URL)
	t.Setenv("TI_FS_TOKEN", "")

	_, err := service.List(context.Background(), ListOptions{Profile: profile, Limit: DefaultListLimit})
	if apperr.CodeFor(err) != "fs.missing_file_system_id" || !strings.Contains(err.Error(), "when token management uses TiDB Cloud API credentials") {
		t.Fatalf("List() error = %v", err)
	}
	_, err = service.Disable(context.Background(), MutationOptions{Profile: profile, TokenID: "scoped-1"})
	if apperr.CodeFor(err) != "fs.missing_file_system_id" {
		t.Fatalf("Disable() error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("missing IDs sent %d remote requests", requests)
	}
}

func TestGenerateScopedUsesOwnerBearerAndStoresScopes(t *testing.T) {
	home := t.TempDir()
	ownerToken := wrappedToken(t, "fs-1", 1)
	scopedToken := wrappedToken(t, "fs-1", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tokens" || r.Header.Get("Authorization") != "Bearer "+ownerToken {
			t.Errorf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-TiDBCloud-Public-Key") != "" {
			t.Error("scoped issue included TiDB Cloud credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["subject"] != "sandbox" || body["ttl_seconds"] != float64(3600) {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":` + jsonString(scopedToken) + `,"token_id":"scoped-1","subject":"sandbox","scope_kind":"fs_scoped","expires_at":"2026-08-13T00:00:00Z","scopes":[{"prefix":"/workspace","ops":["read","list"]}]}`))
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{FileSystemID: "fs-1", RegionCode: "aws-us-east-1", APIKey: ownerToken, TokenID: "owner-1", ScopeKind: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	ttl := time.Hour
	result, err := service.GenerateScoped(context.Background(), GenerateScopedOptions{Profile: profile, FileSystemID: "fs-1", Subject: "sandbox", TTL: &ttl, Allows: []string{"/workspace:read,list"}, StoreLocally: true, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScopeKind != "fs_scoped" || !result.CredentialsStored || len(result.Scopes) != 1 || result.Scopes[0].Prefix != "/workspace" {
		t.Fatalf("result = %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil || credential.APIKey != scopedToken || credential.ScopeKind != "fs_scoped" || len(credential.Scopes) != 1 || strings.Join(credential.Scopes[0].Ops, ",") != "read,list" {
		t.Fatalf("credential = %#v, %v", credential, err)
	}
}

func TestGenerateScopedValidationAndKnownScopedOwnerRejection(t *testing.T) {
	ttl := time.Hour
	base := GenerateScopedOptions{TTL: &ttl}
	for _, tc := range []struct {
		allow string
		code  string
	}{
		{"", "fs.token_scope_required"},
		{"/workspace:search", "fs.invalid_token_scope"},
		{"/workspace:unknown", "fs.invalid_token_scope"},
		{"/workspace/../secret:read", "fs.invalid_token_scope"},
	} {
		opts := base
		if tc.allow != "" {
			opts.Allows = []string{tc.allow}
		}
		if _, _, _, err := validateGenerateScoped(opts); apperr.CodeFor(err) != tc.code {
			t.Fatalf("allow %q error = %v", tc.allow, err)
		}
	}
	home := t.TempDir()
	service, profile := tokenTestService(home, "http://127.0.0.1:1")
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{FileSystemID: "fs-1", RegionCode: "aws-us-east-1", APIKey: wrappedToken(t, "fs-1", 1), ScopeKind: "fs_scoped"}, false); err != nil {
		t.Fatal(err)
	}
	_, err := service.GenerateScoped(context.Background(), GenerateScopedOptions{Profile: profile, FileSystemID: "fs-1", TTL: &ttl, Allows: []string{"/workspace:read"}})
	if apperr.CodeFor(err) != "fs.owner_token_required" {
		t.Fatalf("known scoped token error = %v", err)
	}
}

func TestGenerateStoreConflictPreventsRemoteMutation(t *testing.T) {
	home := t.TempDir()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredential(home, profile, "fs-1", "aws-us-east-1", wrappedToken(t, "fs-1", 1), false); err != nil {
		t.Fatal(err)
	}
	ttl := time.Hour
	_, err := service.Generate(context.Background(), GenerateOptions{Profile: profile, FileSystemID: "fs-1", TokenName: "next", TTL: &ttl, StoreLocally: true})
	if apperr.CodeFor(err) != "fs.token_local_conflict" || requests != 0 {
		t.Fatalf("Generate() err=%v requests=%d", err, requests)
	}
}

func TestGenerateDoesNotStoreByDefault(t *testing.T) {
	home := t.TempDir()
	token := wrappedToken(t, "fs-1", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":` + jsonString(token) + `,"token_id":"token-1","tenant_id":"fs-1","key_name":"ci","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z"}`))
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	ttl := time.Hour
	result, err := service.Generate(context.Background(), GenerateOptions{Profile: profile, FileSystemID: "fs-1", TokenName: "ci", TTL: &ttl})
	if err != nil || result.CredentialsStored || result.FSToken != token {
		t.Fatalf("Generate() result=%#v err=%v", result, err)
	}
	if _, err := fscred.GetCredential(home, profile.Name, "fs-1"); apperr.CodeFor(err) != "fs.credential_not_found" {
		t.Fatalf("default generation wrote local credentials: %v", err)
	}
}

func TestGenerateStoreFailureRollsBackRemoteToken(t *testing.T) {
	home := t.TempDir()
	newToken := wrappedToken(t, "fs-1", 2)
	deleteRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-1","tenant_id":"fs-1","key_name":"owner","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tokens/token-1":
			deleteRequests++
			_, _ = w.Write([]byte(`{"token_id":"token-1","tenant_id":"fs-1","status":"revoked"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	service.storeCredential = func(string, *config.Profile, fscred.Credential, bool) (fscred.Credential, error) {
		return fscred.Credential{}, errors.New("injected write failure")
	}
	ttl := time.Hour
	result, err := service.Generate(context.Background(), GenerateOptions{Profile: profile, FileSystemID: "fs-1", TokenName: "owner", TTL: &ttl, StoreLocally: true})
	if apperr.CodeFor(err) != "fs.token_store_failed" || deleteRequests != 1 {
		t.Fatalf("Generate() result=%#v err=%v deleteRequests=%d", result, err, deleteRequests)
	}
	if result.FSToken != "" {
		t.Fatalf("rolled-back token leaked in result: %#v", result)
	}
}

func TestGenerateStoreAndRollbackFailureReturnsOneTimeSecret(t *testing.T) {
	home := t.TempDir()
	newToken := wrappedToken(t, "fs-1", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-1","tenant_id":"fs-1","key_name":"owner","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tokens/token-1":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"rollback failed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	service.storeCredential = func(string, *config.Profile, fscred.Credential, bool) (fscred.Credential, error) {
		return fscred.Credential{}, errors.New("injected write failure")
	}
	ttl := time.Hour
	result, err := service.Generate(context.Background(), GenerateOptions{Profile: profile, FileSystemID: "fs-1", TokenName: "owner", TTL: &ttl, StoreLocally: true})
	if apperr.CodeFor(err) != "fs.token_partial_success" || result.FSToken != newToken || result.TokenID != "token-1" {
		t.Fatalf("Generate() result=%#v err=%v", result, err)
	}
	partial, ok := err.(*PartialResultError)
	if !ok || partial.StructuredResult().(GenerateResult).FSToken != newToken {
		t.Fatalf("partial result = %#v", err)
	}
}

func TestLocalRefreshAtomicallyReplacesCredentialAndPreservesScopes(t *testing.T) {
	home := t.TempDir()
	oldToken := wrappedToken(t, "fs-1", 1)
	newToken := wrappedToken(t, "fs-1", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+oldToken || r.Header.Get("X-TiDBCloud-Public-Key") != "" {
			t.Errorf("refresh authentication = %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-1","tenant_id":"fs-1","scope_kind":"fs_scoped","expires_at":"2026-08-14T00:00:00Z"}`))
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{FileSystemID: "fs-1", RegionCode: "aws-us-east-1", APIKey: oldToken, TokenID: "token-1", TokenName: "preserved", ScopeKind: "fs_scoped", Scopes: []fscred.TokenScope{{Prefix: "/workspace", Ops: []string{"read", "list"}}}}, false); err != nil {
		t.Fatal(err)
	}
	result, err := service.Refresh(context.Background(), RefreshOptions{Profile: profile, FileSystemID: "fs-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.CredentialsStored || result.FSToken != newToken {
		t.Fatalf("result = %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil {
		t.Fatal(err)
	}
	if credential.APIKey != newToken || credential.TokenName != "preserved" || credential.TokenID != "token-1" || credential.ScopeKind != "fs_scoped" || len(credential.Scopes) != 1 || credential.Scopes[0].Prefix != "/workspace" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestLocalRefreshRecoveryWriteFailureReturnsNewSecret(t *testing.T) {
	home := t.TempDir()
	oldToken := wrappedToken(t, "fs-1", 1)
	newToken := wrappedToken(t, "fs-1", 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-1","tenant_id":"fs-1","scope_kind":"owner","expires_at":null}`))
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{FileSystemID: "fs-1", RegionCode: "aws-us-east-1", APIKey: oldToken, TokenID: "token-1", ScopeKind: "owner"}, false); err != nil {
		t.Fatal(err)
	}
	service.writeRecovery = func(string, string, fscred.Credential) (string, error) {
		return "", errors.New("injected recovery write failure")
	}
	result, err := service.Refresh(context.Background(), RefreshOptions{Profile: profile, FileSystemID: "fs-1"})
	if apperr.CodeFor(err) != "fs.token_partial_success" || result.FSToken != newToken || result.CredentialsStored {
		t.Fatalf("Refresh() result=%#v err=%v", result, err)
	}
	credential, loadErr := fscred.GetCredential(home, profile.Name, "fs-1")
	if loadErr != nil || credential.APIKey != oldToken {
		t.Fatalf("old credential was not preserved: %#v %v", credential, loadErr)
	}
}

func TestRefreshNetworkFailureIsAmbiguousAndNotRetried(t *testing.T) {
	home := t.TempDir()
	token := wrappedToken(t, "fs-1", 1)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support hijacking")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Hijack(): %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	result, err := service.Refresh(context.Background(), RefreshOptions{Profile: profile, Token: token, TokenExplicit: true})
	if apperr.CodeFor(err) != "fs.token_refresh_ambiguous" || requests != 1 || result.FSToken != "" {
		t.Fatalf("Refresh() result=%#v err=%v requests=%d", result, err, requests)
	}
}

func TestRefreshAndDisableRejectMatchingMounts(t *testing.T) {
	home := t.TempDir()
	token := wrappedToken(t, "fs-1", 1)
	service, profile := tokenTestService(home, "http://127.0.0.1:1")
	locator, err := mountlocator.New(profile.Name, "fs-1", "aws-us-east-1", t.TempDir(), t.TempDir(), "fs")
	if err != nil {
		t.Fatal(err)
	}
	locator = locator.WithTokenCorrelation("fs-1", "token-1", tokenFingerprint(token))
	if _, err := mountlocator.Write(home, locator); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), RefreshOptions{Profile: profile, FileSystemID: "fs-1", Token: token, TokenExplicit: true}); apperr.CodeFor(err) != "fs.token_mount_active" || !strings.Contains(err.Error(), "drain-file-system") {
		t.Fatalf("refresh mount guard = %v", err)
	}
	if _, err := service.Disable(context.Background(), MutationOptions{Profile: profile, FileSystemID: "fs-1", TokenID: "token-1"}); apperr.CodeFor(err) != "fs.token_mount_active" {
		t.Fatalf("disable mount guard = %v", err)
	}
}

func TestEnvironmentRefreshDoesNotRewriteLocalCredential(t *testing.T) {
	home := t.TempDir()
	localToken := wrappedToken(t, "fs-1", 1)
	envToken := wrappedToken(t, "fs-1", 8)
	newToken := wrappedToken(t, "fs-1", 9)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+envToken {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":` + jsonString(newToken) + `,"token_id":"token-env","tenant_id":"fs-1","scope_kind":"owner","expires_at":null}`))
	}))
	defer server.Close()
	service, profile := tokenTestService(home, server.URL)
	if _, err := fscred.StoreCredential(home, profile, "fs-1", "aws-us-east-1", localToken, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FS_TOKEN", envToken)
	result, err := service.Refresh(context.Background(), RefreshOptions{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if result.CredentialsStored {
		t.Fatalf("environment refresh stored credentials: %#v", result)
	}
	credential, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil || credential.APIKey != localToken {
		t.Fatalf("local credential changed: %#v %v", credential, err)
	}
}

func tokenTestService(home, baseURL string) (Service, *config.Profile) {
	profile := &config.Profile{
		Name: "test", HomeDir: home, PlacementRegionCode: "aws-us-east-1", CloudProvider: "aws", RegionCode: "us-east-1",
		TiDBCloudPublicKey: "public", TiDBCloudPrivateKey: "private",
	}
	resolver := endpoints.Resolver{FSBaseURLs: map[endpoints.ProviderRegion]string{{Provider: "aws", Region: "us-east-1"}: baseURL}}
	return Service{Resolver: resolver, HomeDir: home, Timeout: 2 * time.Second}, profile
}

func wrappedToken(t *testing.T, fileSystemID string, version int) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(map[string]any{"tenant_id": fileSystemID, "token_version": version})
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return "drive9_" + base64.RawURLEncoding.EncodeToString([]byte(header+"."+payload+".signature"))
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestMain(m *testing.M) {
	os.Unsetenv("TI_FS_TOKEN")
	os.Exit(m.Run())
}
