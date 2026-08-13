package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
)

func TestControlPlaneTokenRequestsUseHeadersAndExpectedShapes(t *testing.T) {
	t.Parallel()
	requests := make(chan *http.Request, 5)
	bodies := make(chan map[string]any, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		requests <- r.Clone(r.Context())
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/tokens/generate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"drive9_secret","token_id":"token-1","tenant_id":"fs-1","key_name":"ci","scope_kind":"owner","status":"active","issued_at":"2026-08-12T00:00:00Z","expires_at":null}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"tokens":[],"next_offset":50}`))
		default:
			_, _ = w.Write([]byte(`{"token_id":"token-1","tenant_id":"fs-1","status":"active"}`))
		}
	}))
	defer server.Close()

	client := newTokenTestClient(t, server.URL, "")
	creds := TiDBCloudCredentials{PublicKey: "public", PrivateKey: "private"}
	ttl := int64(3600)
	generated, err := client.GenerateToken(context.Background(), creds, GenerateTokenRequest{FileSystemID: "fs-1", TokenName: "ci", TTLSeconds: &ttl})
	if err != nil || generated.Token != "drive9_secret" {
		t.Fatalf("GenerateToken() = %#v, %v", generated, err)
	}
	req, body := <-requests, <-bodies
	assertControlHeaders(t, req)
	if body["tenant_id"] != "fs-1" || body["key_name"] != "ci" || body["ttl_seconds"] != float64(3600) {
		t.Fatalf("generate body = %#v", body)
	}

	listed, err := client.ListTokens(context.Background(), creds, ListTokensOptions{FileSystemID: "fs-1", IncludeExpired: true, Offset: 2, Limit: 50})
	if err != nil || listed.NextOffset == nil || *listed.NextOffset != 50 {
		t.Fatalf("ListTokens() = %#v, %v", listed, err)
	}
	req, body = <-requests, <-bodies
	assertControlHeaders(t, req)
	if len(body) != 0 || req.URL.Query().Get("tenant_id") != "fs-1" || req.URL.Query().Get("include_expired") != "1" || req.URL.Query().Get("offset") != "2" || req.URL.Query().Get("limit") != "50" {
		t.Fatalf("list request = %s %#v", req.URL.String(), body)
	}

	if _, err := client.SetTokenEnabled(context.Background(), creds, "fs-1", "token-1", true); err != nil {
		t.Fatal(err)
	}
	req, body = <-requests, <-bodies
	assertControlHeaders(t, req)
	if req.URL.Path != "/v1/tokens/token-1/activate" || req.URL.Query().Get("tenant_id") != "fs-1" || len(body) != 0 {
		t.Fatalf("activate request = %s %#v", req.URL.Path, body)
	}
	if _, err := client.SetTokenEnabled(context.Background(), creds, "fs-1", "token-1", false); err != nil {
		t.Fatal(err)
	}
	req, body = <-requests, <-bodies
	assertControlHeaders(t, req)
	if req.URL.Path != "/v1/tokens/token-1/deactivate" || req.URL.Query().Get("tenant_id") != "fs-1" || len(body) != 0 {
		t.Fatalf("deactivate request = %s %#v", req.URL.Path, body)
	}

	if _, err := client.DeleteToken(context.Background(), creds, "fs-1", "token-1"); err != nil {
		t.Fatal(err)
	}
	req, body = <-requests, <-bodies
	assertControlHeaders(t, req)
	if req.Method != http.MethodDelete || req.URL.Path != "/v1/tokens/token-1" || req.URL.Query().Get("tenant_id") != "fs-1" || len(body) != 0 {
		t.Fatalf("delete request = %s %s %#v", req.Method, req.URL.Path, body)
	}
}

func TestRefreshTokenUsesBearerOnly(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer drive9_old" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get(tidbCloudPublicKeyHeader) != "" || r.Header.Get(tidbCloudPrivateKeyHeader) != "" {
			t.Error("refresh included control-plane credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Errorf("refresh body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"drive9_new","token_id":"token-1","tenant_id":"fs-1","scope_kind":"owner","expires_at":null}`))
	}))
	defer server.Close()

	client := newTokenTestClient(t, server.URL, "drive9_old")
	response, err := client.RefreshToken(context.Background(), RefreshTokenRequest{})
	if err != nil || response.Token != "drive9_new" {
		t.Fatalf("RefreshToken() = %#v, %v", response, err)
	}
}

func TestRefreshTokenSendsExplicitTTL(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["ttl_seconds"] != float64(3600) {
			t.Errorf("refresh body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"drive9_new","token_id":"token-1","tenant_id":"fs-1","scope_kind":"owner","expires_at":null}`))
	}))
	defer server.Close()
	client := newTokenTestClient(t, server.URL, "drive9_old")
	ttl := int64(3600)
	if _, err := client.RefreshToken(context.Background(), RefreshTokenRequest{TTLSeconds: &ttl}); err != nil {
		t.Fatal(err)
	}
}

func TestTokenErrorsRedactCredentialMaterial(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"rejected drive9_old"}`))
	}))
	defer server.Close()
	client := newTokenTestClient(t, server.URL, "drive9_old")
	_, err := client.RefreshToken(context.Background(), RefreshTokenRequest{})
	if err == nil || strings.Contains(err.Error(), "drive9_old") {
		t.Fatalf("RefreshToken error leaked token: %v", err)
	}
}

func newTokenTestClient(t *testing.T, baseURL, bearer string) *Client {
	t.Helper()
	endpoint := endpoints.Endpoint{Service: endpoints.ServiceFS, BaseURL: baseURL, Provider: "aws", RegionCode: "us-east-1"}
	var raw *api.Client
	var err error
	if bearer == "" {
		raw, err = api.New(api.Options{Endpoint: endpoint, ProfileName: "test", MaxRetries: -1})
	} else {
		raw, err = api.NewBearerClient("test", bearer, endpoint, "fs.token.refresh", api.Options{MaxRetries: -1})
	}
	if err != nil {
		t.Fatal(err)
	}
	return New(raw)
}

func assertControlHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if req.Header.Get(tidbCloudPublicKeyHeader) != "public" || req.Header.Get(tidbCloudPrivateKeyHeader) != "private" {
		t.Fatalf("control headers = %q/%q", req.Header.Get(tidbCloudPublicKeyHeader), req.Header.Get(tidbCloudPrivateKeyHeader))
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected Authorization = %q", req.Header.Get("Authorization"))
	}
}
