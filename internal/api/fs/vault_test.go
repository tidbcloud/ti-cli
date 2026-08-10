package fs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
)

func TestReadVaultSecretFieldUsesDelegatedBearerToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/vault/read/db-prod/password" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte("hunter2"))
	}))
	defer server.Close()

	value, err := testBearerClient(t, server.URL).ReadVaultSecretField(context.Background(), "db-prod", "password")
	if err != nil {
		t.Fatalf("ReadVaultSecretField failed: %v", err)
	}
	if value != "hunter2" {
		t.Fatalf("value = %q", value)
	}
}

func TestCreateVaultSecretReturnsStatusError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"secret already exists"}`))
	}))
	defer server.Close()

	_, err := testBearerClient(t, server.URL).CreateVaultSecret(context.Background(), "db-prod", map[string]string{"password": "hunter2"})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected conflict API error, got %#v", err)
	}
}

func TestIssueVaultGrantSendsPayloadAndDecodesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vault/grants" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := req["principal_type"]; ok {
			t.Fatalf("principal_type must not be sent: %#v", req)
		}
		if req["agent"] != "agent-1" || req["perm"] != "read" || req["label_hint"] != "prod-readonly" || req["ttl_seconds"] != float64(3600) {
			t.Fatalf("unexpected request: %#v", req)
		}
		scope, ok := req["scope"].([]any)
		if !ok || len(scope) != 1 || scope[0] != "db-prod/password" {
			t.Fatalf("scope = %#v", req["scope"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "delegated-token",
			"grant_id":   "grant-123",
			"expires_at": "2026-04-19T13:00:00Z",
			"scope":      []string{"db-prod/password"},
			"perm":       "read",
		})
	}))
	defer server.Close()

	response, err := testBearerClient(t, server.URL).IssueVaultGrant(context.Background(), VaultGrantIssueRequest{
		Agent:      "agent-1",
		Scope:      []string{"db-prod/password"},
		Perm:       "read",
		TTLSeconds: 3600,
		LabelHint:  "prod-readonly",
	})
	if err != nil {
		t.Fatalf("IssueVaultGrant failed: %v", err)
	}
	if response.Token != "delegated-token" || response.GrantID != "grant-123" || response.Perm != "read" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if len(response.Scope) != 1 || response.Scope[0] != "db-prod/password" {
		t.Fatalf("scope = %#v", response.Scope)
	}
}

func TestRevokeVaultGrantSendsDeleteWithPayload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/vault/grants/grant-123" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var req struct {
			RevokedBy string `json:"revoked_by"`
			Reason    string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.RevokedBy != "ti" || req.Reason != "rotated" {
			t.Fatalf("unexpected request: %#v", req)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := testBearerClient(t, server.URL).RevokeVaultGrant(context.Background(), "grant-123", "ti", "rotated"); err != nil {
		t.Fatalf("RevokeVaultGrant failed: %v", err)
	}
}
