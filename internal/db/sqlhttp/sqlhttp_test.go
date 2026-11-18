package sqlhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
	"github.com/tidbcloud/ti-cli/internal/oplog"
)

func TestExecuteHTTP(t *testing.T) {
	var gotAuthUser string
	var gotAuthPassword string
	var gotDatabase string
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/sql" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		gotAuthUser, gotAuthPassword, _ = r.BasicAuth()
		gotDatabase = r.Header.Get("TiDB-Database")
		if r.Header.Get("TiDB-Session") != "" {
			t.Fatalf("expected empty TiDB-Session")
		}
		if r.Header.Get("X-Debug-Trace-Id") == "" {
			t.Fatalf("missing trace id")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("TiDB-Session", "session-1")
		_, _ = w.Write([]byte(`{
			"types":[{"name":"n","type":"INT","nullable":false},{"name":"j","type":"JSON","nullable":true}],
			"rows":[["1","{\"ok\":true}"]],
			"rowsAffected":1,
			"sLastInsertID":"42"
		}`))
	}))
	defer server.Close()

	result, err := Execute(context.Background(), Options{
		ClusterID:  "cluster-1",
		AccessMode: sqlcred.ReadWrite,
		Username:   "user",
		Password:   "pass",
		Database:   "test",
		SQL:        "select 1",
		BaseURL:    server.URL,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if gotAuthUser != "user" || gotAuthPassword != "pass" {
		t.Fatalf("unexpected auth %q %q", gotAuthUser, gotAuthPassword)
	}
	if gotDatabase != "test" || gotBody["query"] != "select 1" {
		t.Fatalf("unexpected request database=%q body=%#v", gotDatabase, gotBody)
	}
	if result.Transport != "https" || result.AccessMode != sqlcred.ReadWrite || result.Session != "session-1" {
		t.Fatalf("unexpected result metadata: %#v", result)
	}
	if result.RowCount != 1 || result.Rows[0]["n"] != int64(1) {
		t.Fatalf("unexpected rows: %#v", result.Rows)
	}
	if nested, ok := result.Rows[0]["j"].(map[string]any); !ok || nested["ok"] != true {
		t.Fatalf("unexpected json row value: %#v", result.Rows[0]["j"])
	}
}

func TestExecuteHTTPErrorDoesNotEchoSQL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"permission denied"}`))
	}))
	defer server.Close()

	_, err := Execute(context.Background(), Options{
		ClusterID: "cluster-1",
		Username:  "user",
		Password:  "pass",
		SQL:       "insert into secret_table values ('secret')",
		BaseURL:   server.URL,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := apperr.MessageFor(err); got != "permission denied" {
		t.Fatalf("unexpected message %q", got)
	}
	if strings.Contains(apperr.MessageFor(err), "secret_table") {
		t.Fatalf("error leaked SQL text: %q", apperr.MessageFor(err))
	}
}

func TestExecuteHTTPRecordsSafeAPIEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"types":[],"rows":[]}`))
	}))
	defer server.Close()
	recorder := &memoryRecorder{}

	_, err := Execute(oplog.WithRecorder(context.Background(), recorder), Options{
		ClusterID: "cluster-1",
		Username:  "user",
		Password:  "pass",
		SQL:       "select secret from private_table",
		BaseURL:   server.URL,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("expected one event, got %#v", recorder.events)
	}
	event := recorder.events[0]
	if event.Service != "tidb_cloud_sql" || event.Operation != "execute SQL statement" || event.Method != http.MethodPost || event.StatusCode != http.StatusOK {
		t.Fatalf("unexpected event: %#v", event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private_table") || strings.Contains(string(data), "select secret") || strings.Contains(string(data), "pass") {
		t.Fatalf("SQL event leaked sensitive data: %s", string(data))
	}
}

type memoryRecorder struct {
	events []oplog.Event
}

func (r *memoryRecorder) Record(_ context.Context, event oplog.Event) {
	r.events = append(r.events, event)
}

func TestEndpointURLFromClusterHost(t *testing.T) {
	got, err := endpointURL(Options{Host: "gateway01.us-east-1.prod.aws.tidbcloud.com"})
	if err != nil {
		t.Fatalf("endpointURL failed: %v", err)
	}
	if got != "https://http-gateway01.us-east-1.prod.aws.tidbcloud.com/v1beta/sql" {
		t.Fatalf("endpointURL = %q", got)
	}
}

const sqlHTTPJSONResponse = `{"types":[{"name":"n","type":"INT","nullable":false}],"rows":[["1"]],"rowsAffected":0}`

func executeAgainst(t *testing.T, baseURL string, client *http.Client) error {
	t.Helper()
	_, err := Execute(context.Background(), Options{
		ClusterID:  "cluster-1",
		AccessMode: sqlcred.ReadWrite,
		Username:   "user",
		Password:   "pass",
		Database:   "test",
		SQL:        "select 1",
		BaseURL:    baseURL,
		HTTPClient: client,
	})
	return err
}

// TestExecuteHTTPRefusesCrossOriginRedirect covers the credential-redirect rule
// for the Basic-authenticated SQL transport: the second origin must never receive
// the request, and the failure names the origin change.
func TestExecuteHTTPRefusesCrossOriginRedirect(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		_, _ = w.Write([]byte(sqlHTTPJSONResponse))
	}))
	t.Cleanup(attacker.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/v1beta/sql", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)

	err := executeAgainst(t, redirecting.URL, nil)
	if apperr.CodeFor(err) != "db.sql_http_redirect_refused" {
		t.Fatalf("error code = %q, want db.sql_http_redirect_refused: %v", apperr.CodeFor(err), err)
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "refused a SQL HTTPS API redirect") || !strings.Contains(message, "to "+attacker.URL) {
		t.Fatalf("refusal message is not actionable: %q", message)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("the redirect target received %d request(s); SQL credentials could have leaked", got)
	}
}

// TestExecuteHTTPRefusesCrossOriginRedirectUnderCallerPolicy pins that a caller
// policy can only narrow the rule, never widen it.
func TestExecuteHTTPRefusesCrossOriginRedirectUnderCallerPolicy(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
	}))
	t.Cleanup(attacker.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/v1beta/sql", http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)

	permissive := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	if err := executeAgainst(t, redirecting.URL, permissive); apperr.CodeFor(err) != "db.sql_http_redirect_refused" {
		t.Fatalf("permissive caller policy error = %v, want db.sql_http_redirect_refused", err)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("the redirect target received %d request(s)", got)
	}
}

// TestExecuteHTTPFollowsSameOriginRedirect keeps ordinary same-origin redirects
// working. 307 preserves the method, so the redirected request must still be the
// authenticated POST with the SQL body.
func TestExecuteHTTPFollowsSameOriginRedirect(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/moved" {
			http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("redirected method = %s, want POST", r.Method)
		}
		if user, password, ok := r.BasicAuth(); !ok || user != "user" || password != "pass" {
			t.Errorf("redirected request lost its Basic auth: %q %q", user, password)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["query"] != "select 1" {
			t.Errorf("redirected request lost the SQL body: %#v (%v)", body, err)
		}
		_, _ = w.Write([]byte(sqlHTTPJSONResponse))
	}))
	t.Cleanup(server.Close)

	if err := executeAgainst(t, server.URL, nil); err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

// TestExecuteHTTPSameOriginRedirectRefusedByCallerFallsThrough pins the
// classification rule: when a stricter caller policy refuses a same-origin
// redirect, Execute must not claim a cross-origin refusal.
func TestExecuteHTTPSameOriginRedirectRefusedByCallerFallsThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/moved", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	noRedirects := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	err := executeAgainst(t, server.URL, noRedirects)
	if code := apperr.CodeFor(err); code != "db.sql_execution_failed" {
		t.Fatalf("error code = %q, want db.sql_execution_failed: %v", code, err)
	}
	if message := apperr.MessageFor(err); strings.Contains(message, "refused a SQL HTTPS API redirect") {
		t.Fatalf("a same-origin refusal was misclassified as a cross-origin redirect: %q", message)
	}
}

// TestExecuteHTTPRecordsRedirectLoopEvent pins that the operation log does not
// contradict the command error for a same-origin redirect loop.
func TestExecuteHTTPRecordsRedirectLoopEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	recorder := &memoryRecorder{}

	_, err := Execute(oplog.WithRecorder(context.Background(), recorder), Options{
		ClusterID: "cluster-1",
		Username:  "user",
		Password:  "pass",
		SQL:       "select 1",
		BaseURL:   server.URL,
	})
	if apperr.CodeFor(err) != "db.sql_http_redirect_loop" {
		t.Fatalf("error code = %q: %v", apperr.CodeFor(err), err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("expected one event, got %#v", recorder.events)
	}
	if code := recorder.events[0].ErrorCode; code != "db.sql_http_redirect_loop" {
		t.Fatalf("operation log error code = %q, want db.sql_http_redirect_loop", code)
	}
}

// TestExecuteHTTPReportsRedirectLoop pins the redirect cap for the SQL client.
func TestExecuteHTTPReportsRedirectLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	if err := executeAgainst(t, server.URL, nil); apperr.CodeFor(err) != "db.sql_http_redirect_loop" {
		t.Fatalf("redirect loop error = %v, want db.sql_http_redirect_loop", err)
	}
}
