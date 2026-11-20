package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tidbcloud/ti-cli/internal/telemetrybackend"
	"github.com/tidbcloud/ti-cli/internal/telemetrybackend/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	os.Exit(run(os.Args[1:], os.Getenv, logger))
}

func run(args []string, getenv func(string) string, logger *slog.Logger) int {
	flags := flag.NewFlagSet("ti-telemetry-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var targetVersion int64
	flags.Int64Var(&targetVersion, "to-version", 0, "apply migrations through this version")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(os.Stderr)
			flags.PrintDefaults()
			return 0
		}
		logger.Error("invalid arguments", "error", err.Error())
		return 2
	}

	dsn := strings.TrimSpace(getenv("TIDB_DSN"))
	if dsn == "" {
		logger.Error("TIDB_DSN is required")
		return 1
	}
	// Same rule as the API server: in production (also when
	// TELEMETRY_ENVIRONMENT is unset or not a known local/test name) the DSN
	// must use verified TLS.
	if err := telemetrybackend.ValidateTiDBDSN(getenv("TELEMETRY_ENVIRONMENT"), dsn); err != nil {
		logger.Error("refusing TIDB_DSN", "error", err.Error())
		return 1
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		logger.Error("open TiDB connection failed")
		return 1
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
		return 1
	}
	logger.Info("telemetry schema migrations applied")
	return 0
}
