package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/oplog"
)

func TestClientDoJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("unexpected Accept header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cluster-1"}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL, authz.StarterClusterRead)
	req, err := client.NewRequest(context.Background(), http.MethodGet, "/v1beta1/clusters/cluster-1", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := client.DoJSON(req, &out); err != nil {
		t.Fatalf("DoJSON failed: %v", err)
	}
	if out.ID != "cluster-1" {
		t.Fatalf("unexpected response %#v", out)
	}
}

func TestClientMapsAuthenticationError(t *testing.T) {
	err := statusError(t, http.StatusUnauthorized, `{"message":"bad key"}`, authz.StarterClusterRead)
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected exit 3, got %d", got)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected api.Error, got %T", err)
	}
	if apiErr.Code != "auth.invalid_credentials" {
		t.Fatalf("unexpected code %q", apiErr.Code)
	}
	if !strings.Contains(apperr.MessageFor(err), "authentication failed") {
		t.Fatalf("unexpected message %q", apperr.MessageFor(err))
	}
}

func TestClientMapsPermissionDenied(t *testing.T) {
	err := statusError(t, http.StatusForbidden, `{"message":"denied"}`, authz.StarterClusterCreate)
	if got := apperr.ExitCodeFor(err); got != 4 {
		t.Fatalf("expected exit 4, got %d", got)
	}
	if !strings.Contains(apperr.MessageFor(err), string(authz.StarterClusterCreate)) {
		t.Fatalf("unexpected message %q", apperr.MessageFor(err))
	}
}

func TestClientMapsNotFound(t *testing.T) {
	err := statusError(t, http.StatusNotFound, `{"message":"missing"}`, authz.StarterClusterRead)
	if got := apperr.ExitCodeFor(err); got != 5 {
		t.Fatalf("expected exit 5, got %d", got)
	}
}

func TestClientMapsAPIGap(t *testing.T) {
	err := statusError(t, http.StatusMethodNotAllowed, `{"message":"method not allowed"}`, authz.StarterSQLUserRead)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected api.Error, got %T", err)
	}
	if apiErr.Code != "api.contract_gap" {
		t.Fatalf("unexpected code %q", apiErr.Code)
	}
}

func TestClientMapsPaymentRequired(t *testing.T) {
	err := statusError(t, http.StatusPaymentRequired, `{"message":"payment cannot be processed"}`, authz.StarterClusterCreate)
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected api.Error, got %T", err)
	}
	if apiErr.Code != "api.payment_required" {
		t.Fatalf("unexpected code %q", apiErr.Code)
	}
	if !strings.Contains(apperr.MessageFor(err), "payment required") {
		t.Fatalf("unexpected message %q", apperr.MessageFor(err))
	}
}

func TestClientRecordsSafeAPIEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "request-1")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := New(Options{
		Endpoint: endpoints.Endpoint{
			Service:    endpoints.ServiceFS,
			BaseURL:    server.URL,
			Provider:   "aws",
			RegionCode: "us-east-1",
		},
		ProfileName: "stage",
		Permission:  authz.FSFileRead,
		Action:      "read ti fs file",
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	recorder := &memoryRecorder{}
	ctx := oplog.WithRecorder(context.Background(), recorder)
	req, err := client.NewRequest(ctx, http.MethodGet, "/v1/fs/secret/path?read=1", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	var out map[string]bool
	if err := client.DoJSON(req, &out); err != nil {
		t.Fatalf("DoJSON failed: %v", err)
	}

	if len(recorder.events) != 1 {
		t.Fatalf("expected one event, got %#v", recorder.events)
	}
	event := recorder.events[0]
	if event.Service != "ti_fs" || event.Operation != "read ti fs file" || event.StatusCode != http.StatusOK || event.RequestID != "request-1" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if strings.Contains(event.Operation, "secret/path") || strings.Contains(event.Operation, "read=1") {
		t.Fatalf("API event leaked raw path/query: %#v", event)
	}
}

func TestClientRetriesSafeGETByDefault(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := testClient(t, server.URL, authz.FSFileRead)
	req, err := client.NewRequest(context.Background(), http.MethodGet, "/v1/fs/file.txt", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	var out map[string]bool
	if err := client.DoJSON(req, &out); err != nil {
		t.Fatalf("DoJSON failed after safe retry: %v", err)
	}
	if !out["ok"] {
		t.Fatalf("unexpected output: %#v", out)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestClientDoesNotRetryUnsafePOSTByDefault(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := testClient(t, server.URL, authz.FSFileWrite)
	req, err := client.NewRequest(context.Background(), http.MethodPost, "/v1/fs/file.txt", map[string]string{"value": "x"})
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	err = client.DoJSON(req, nil)
	if err == nil {
		t.Fatal("expected POST 500 to fail")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want no unsafe retry", got)
	}
}

func statusError(t *testing.T, status int, body string, permission authz.Permission) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := testClient(t, server.URL, permission)
	client.Action = "create Starter clusters"
	req, err := client.NewRequest(context.Background(), http.MethodGet, "/v1beta1/test", nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	return client.DoJSON(req, nil)
}

type memoryRecorder struct {
	events []oplog.Event
}

func (r *memoryRecorder) Record(_ context.Context, event oplog.Event) {
	r.events = append(r.events, event)
}

func testClient(t *testing.T, baseURL string, permission authz.Permission) *Client {
	t.Helper()
	client, err := New(Options{
		Endpoint: endpoints.Endpoint{
			Service:    endpoints.ServiceStarter,
			BaseURL:    baseURL,
			Provider:   "aws",
			RegionCode: "us-east-1",
		},
		ProfileName: "stage",
		Permission:  permission,
		HTTPClient:  http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	return client
}
