package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tidbcloud/tdc/internal/telemetrybackend/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var targetVersion int64
	flag.Int64Var(&targetVersion, "to-version", 0, "apply migrations through this version")
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("TIDB_DSN"))
	if dsn == "" {
		logger.Error("TIDB_DSN is required")
		os.Exit(1)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		logger.Error("open TiDB connection failed")
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if targetVersion == 0 {
		err = migrations.Up(ctx, db)
	} else {
		err = migrations.UpTo(ctx, db, targetVersion)
	}
	if err != nil {
		logger.Error("apply telemetry schema migrations failed")
		os.Exit(1)
	}
	logger.Info("telemetry schema migrations applied")
}
