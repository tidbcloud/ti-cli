package telemetrybackend

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
)

type fakeSQLDatabase struct {
	mu      sync.Mutex
	queries []string
	args    [][]any
	pingErr error
	execErr error
}

func (f *fakeSQLDatabase) PingContext(context.Context) error {
	return f.pingErr
}

func (f *fakeSQLDatabase) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	f.args = append(f.args, append([]any(nil), args...))
	return fakeSQLResult(1), f.execErr
}

type fakeSQLResult int64

func (r fakeSQLResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeSQLResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestTiDBSinkCreatesSchemaAndBatchInsertsSanitizedEvents(t *testing.T) {
	db := &fakeSQLDatabase{}
	sink := NewTiDBSink(db)
	events := []Event{testEvent(), testEvent()}
	events[1].EventID = "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb45"
	if err := sink.Write(context.Background(), events); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if len(db.queries) != 1 {
		t.Fatalf("query count = %d, want 1", len(db.queries))
	}
	if !strings.HasPrefix(db.queries[0], "INSERT IGNORE INTO telemetry_events") {
		t.Fatalf("insert query = %s", db.queries[0])
	}
	if len(db.args[0]) != 40 {
		t.Fatalf("insert args = %d, want 40", len(db.args[0]))
	}
	if flags, ok := db.args[0][6].(string); !ok || flags != `["file-system-name","output"]` {
		t.Fatalf("flag_names_json = %#v", db.args[0][6])
	}
	for _, query := range db.queries {
		if strings.Contains(query, "SELECT") || strings.Contains(query, "password") {
			t.Fatalf("unexpected sensitive query text: %s", query)
		}
	}
}
