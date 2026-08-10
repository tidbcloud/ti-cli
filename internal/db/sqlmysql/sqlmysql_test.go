package sqlmysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

func TestExecuteOpensAndClosesOneConnection(t *testing.T) {
	driverName := "ti_sqlmysql_fake"
	fake := &fakeDriver{}
	sql.Register(driverName, fake)

	result, err := Execute(context.Background(), Options{
		ClusterID:  "cluster-1",
		AccessMode: sqlcred.ReadWrite,
		SQL:        "select 1",
		DriverName: driverName,
		DSN:        "fake",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if atomic.LoadInt32(&fake.opened) != 1 || atomic.LoadInt32(&fake.closed) != 1 {
		t.Fatalf("expected one open and close, opened=%d closed=%d", fake.opened, fake.closed)
	}
	if result.Transport != "mysql" || result.RowCount != 1 || result.Rows[0]["n"] != "1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

type fakeDriver struct {
	opened int32
	closed int32
}

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	atomic.AddInt32(&d.opened, 1)
	return &fakeConn{driver: d}, nil
}

type fakeConn struct {
	driver *fakeDriver
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, nil
}

func (c *fakeConn) Close() error {
	atomic.AddInt32(&c.driver.closed, 1)
	return nil
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, nil
}

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{done: false}, nil
}

type fakeRows struct {
	done bool
}

func (r *fakeRows) Columns() []string {
	return []string{"n"}
}

func (r *fakeRows) Close() error {
	return nil
}

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "1"
	return nil
}
