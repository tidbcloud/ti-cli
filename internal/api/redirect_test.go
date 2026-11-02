package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

func TestClientRefusesCrossOriginRedirects(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("other host received Authorization %q", got)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer other.Close()

	for _, tc := range []struct {
		name   string
		method string
		status int
		client func(t *testing.T, baseURL string) *Client
	}{
		{"bearer GET 302", http.MethodGet, http.StatusFound, testBearerClient},
		{"bearer POST 307", http.MethodPost, http.StatusTemporaryRedirect, testBearerClient},
		{"digest GET 302", http.MethodGet, http.StatusFound, testDigestClient},
		{"digest POST 308", http.MethodPost, http.StatusPermanentRedirect, testDigestClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			otherHits.Store(0)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/stolen", tc.status)
			}))
			defer origin.Close()

			client := tc.client(t, origin.URL)
			var body any
			if tc.method == http.MethodPost {
				body = map[string]string{"name": "x"}
			}
			req, err := client.NewRequest(context.Background(), tc.method, "/v1beta1/clusters", body)
			if err != nil {
				t.Fatalf("NewRequest failed: %v", err)
			}
			err = client.DoJSON(req, nil)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "api.redirect_refused" || apiErr.StatusCode != tc.status {
				t.Fatalf("expected api.redirect_refused with HTTP %d, got %v", tc.status, err)
			}
			otherHost := strings.TrimPrefix(other.URL, "http://")
			if !strings.Contains(apiErr.Message, otherHost) {
				t.Fatalf("message should name the redirect target, got %q", apiErr.Message)
			}
			if got := otherHits.Load(); got != 0 {
				t.Fatalf("other host was contacted %d times", got)
			}
		})
	}
}

func TestClientFollowsSameOriginRedirectWithCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
			t.Errorf("%s: Authorization = %q", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/v1/old":
			http.Redirect(w, r, "/v1/new", http.StatusFound)
		case "/v1/new":
			_, _ = w.Write([]byte(`{"id":"moved"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	req, err := client.NewRequest(context.Background(), http.MethodGet, "/v1/old", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := client.DoJSON(req, &out); err != nil {
		t.Fatalf("DoJSON failed: %v", err)
	}
	if out.ID != "moved" {
		t.Fatalf("unexpected response %#v", out)
	}
}

func TestClientStopsSameOriginRedirectLoop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/v1/loop", http.StatusFound)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	client.MaxRetries = 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := client.NewRequest(ctx, http.MethodGet, "/v1/loop", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	err = client.DoJSON(req, nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "api.redirect_loop" {
		t.Fatalf("expected api.redirect_loop, got %v", err)
	}
	if !strings.Contains(apiErr.Message, "redirected more than") {
		t.Fatalf("unexpected message %q", apiErr.Message)
	}
	if got := hits.Load(); got != maxRedirects {
		t.Fatalf("expected %d requests before stopping, got %d", maxRedirects, got)
	}
}

func TestRefusedDowngradeRedirectNamesBothSchemes(t *testing.T) {
	client := testBearerClient(t, "https://api.example.com")
	req, err := client.NewRequest(context.Background(), http.MethodGet, "/v1/a", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	res := &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{"http://api.example.com/v1/a"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
	err = client.statusError(req, res)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "api.redirect_refused" {
		t.Fatalf("expected api.redirect_refused, got %v", err)
	}
	if !strings.Contains(apiErr.Message, "https://api.example.com to http://api.example.com") {
		t.Fatalf("message should show both schemes, got %q", apiErr.Message)
	}
}

func TestSameOriginRedirectRules(t *testing.T) {
	via := []*http.Request{{URL: mustParseURL(t, "https://api.example.com/v1/a")}}
	for _, tc := range []struct {
		target string
		allow  bool
	}{
		{"https://api.example.com/v1/b", true},
		{"https://API.example.com/v1/b", true},
		{"http://api.example.com/v1/b", false},
		{"https://api.example.com:8443/v1/b", false},
		{"https://evil.example/v1/b", false},
	} {
		err := sameOriginRedirect(&http.Request{URL: mustParseURL(t, tc.target)}, via)
		if tc.allow && err != nil {
			t.Fatalf("%s: expected redirect to be followed, got %v", tc.target, err)
		}
		if !tc.allow && !errors.Is(err, http.ErrUseLastResponse) {
			t.Fatalf("%s: expected redirect to be refused, got %v", tc.target, err)
		}
	}
}

func TestNewKeepsCallerRedirectPolicyAndDoesNotMutateClient(t *testing.T) {
	shared := &http.Client{}
	client, err := New(Options{Endpoint: endpoints.Endpoint{BaseURL: "https://api.example.com"}, HTTPClient: shared})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if client.HTTPClient.CheckRedirect == nil {
		t.Fatal("expected the same-origin redirect policy on a caller-supplied client")
	}
	if shared.CheckRedirect != nil {
		t.Fatal("New must not modify the caller's http.Client")
	}

	custom := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client, err = New(Options{Endpoint: endpoints.Endpoint{BaseURL: "https://api.example.com"}, HTTPClient: &http.Client{CheckRedirect: custom}})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := client.HTTPClient.CheckRedirect(&http.Request{URL: mustParseURL(t, "https://api.example.com/x")}, []*http.Request{{URL: mustParseURL(t, "https://api.example.com/")}}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("expected the caller's CheckRedirect to be kept, got %v", err)
	}
}

func testBearerClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewBearerClient("stage", "fs-secret", endpoints.Endpoint{
		Service:    endpoints.ServiceFS,
		BaseURL:    baseURL,
		Provider:   "aws",
		RegionCode: "us-east-1",
	}, authz.FSFileRead, Options{})
	if err != nil {
		t.Fatalf("NewBearerClient failed: %v", err)
	}
	return client
}

func testDigestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewDigestClient(&config.Profile{
		Name:                "stage",
		TiDBCloudPublicKey:  "PUBKEY",
		TiDBCloudPrivateKey: "PRIVKEY",
	}, endpoints.Endpoint{
		Service:    endpoints.ServiceStarter,
		BaseURL:    baseURL,
		Provider:   "aws",
		RegionCode: "us-east-1",
	}, authz.StarterClusterRead, Options{})
	if err != nil {
		t.Fatalf("NewDigestClient failed: %v", err)
	}
	return client
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}
