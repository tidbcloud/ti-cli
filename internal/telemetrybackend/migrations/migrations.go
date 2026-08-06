package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

const VersionTable = "tdc_telemetry_schema_migrations"

//go:embed sql/*.sql
var files embed.FS

func Up(ctx context.Context, db *sql.DB) error {
	return up(ctx, db, 0)
}

func UpTo(ctx context.Context, db *sql.DB, version int64) error {
	if version <= 0 {
		return fmt.Errorf("migration target version must be positive")
	}
	return up(ctx, db, version)
}

func up(ctx context.Context, db *sql.DB, version int64) error {
	goose.SetBaseFS(files)
	if err := goose.SetDialect("mysql"); err != nil {
		return fmt.Errorf("configure goose MySQL dialect: %w", err)
	}
	goose.SetTableName(VersionTable)
	if version == 0 {
		if err := goose.UpContext(ctx, db, "sql"); err != nil {
			return fmt.Errorf("apply telemetry schema migrations: %w", err)
		}
		return nil
	}
	if err := goose.UpToContext(ctx, db, "sql", version); err != nil {
		return fmt.Errorf("apply telemetry schema migrations through version %d: %w", version, err)
	}
	return nil
}
