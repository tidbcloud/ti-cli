package sqlmysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"

	"github.com/go-sql-driver/mysql"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
	"github.com/tidbcloud/ti-cli/internal/db/sqlresult"
)

type Options struct {
	ClusterID  string
	AccessMode sqlcred.AccessMode
	Username   string
	Password   string
	Host       string
	Port       int32
	Database   string
	SQL        string
	DriverName string
	DSN        string
}

func DSN(opts Options) string {
	config := mysql.NewConfig()
	config.User = opts.Username
	config.Passwd = opts.Password
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(opts.Host, strconv.FormatInt(int64(opts.Port), 10))
	config.DBName = opts.Database
	config.ParseTime = true
	config.TLSConfig = "true"
	return config.FormatDSN()
}

func Execute(ctx context.Context, opts Options) (sqlresult.Result, error) {
	driverName := opts.DriverName
	if driverName == "" {
		driverName = "mysql"
	}
	dsn := opts.DSN
	if dsn == "" {
		dsn = DSN(opts)
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return sqlresult.Result{}, apperr.Wrap("db.sql_mysql_open", "database", 1, "open MySQL connection", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, opts.SQL)
	if err != nil {
		return sqlresult.Result{}, apperr.Wrap("db.sql_execution_failed", "database", 1, "execute SQL over MySQL transport", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return sqlresult.Result{}, apperr.Wrap("db.sql_mysql_columns", "database", 1, "read MySQL result columns", err)
	}
	fields := make([]sqlresult.Field, 0, len(columns))
	for _, column := range columns {
		fields = append(fields, sqlresult.Field{Name: column, Type: "STRING", Nullable: true})
	}

	resultRows := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return sqlresult.Result{}, apperr.Wrap("db.sql_mysql_scan", "database", 1, "scan MySQL result row", err)
		}
		row := map[string]any{}
		for i, value := range values {
			switch typed := value.(type) {
			case []byte:
				row[columns[i]] = string(typed)
			default:
				row[columns[i]] = typed
			}
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return sqlresult.Result{}, apperr.Wrap("db.sql_mysql_rows", "database", 1, "read MySQL result rows", err)
	}

	return sqlresult.Result{
		Fields:     fields,
		Rows:       resultRows,
		RowCount:   len(resultRows),
		Transport:  "mysql",
		AccessMode: opts.AccessMode,
		ClusterID:  opts.ClusterID,
	}, nil
}

func ValidateEndpoint(host string, port int32) error {
	if host == "" {
		return apperr.New("db.sql_mysql_missing_host", "api", 1, "cluster public endpoint host is missing; describe the cluster and try again after it becomes ACTIVE")
	}
	if port <= 0 {
		return apperr.New("db.sql_mysql_missing_port", "api", 1, "cluster public endpoint port is missing; describe the cluster and try again after it becomes ACTIVE")
	}
	if _, err := strconv.ParseInt(fmt.Sprintf("%d", port), 10, 32); err != nil {
		return apperr.Wrap("db.sql_mysql_invalid_port", "api", 1, "cluster public endpoint port is invalid", err)
	}
	return nil
}
