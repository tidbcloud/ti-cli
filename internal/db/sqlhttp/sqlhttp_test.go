package sqlhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
