package telemetrybackend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type sqlDatabase interface {
	PingContext(context.Context) error
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type TiDBSink struct {
	db sqlDatabase
}

func NewTiDBSink(db sqlDatabase) *TiDBSink {
	return &TiDBSink{db: db}
}

func (s *TiDBSink) Name() string {
	return "tidb"
}

func (s *TiDBSink) Ready(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *TiDBSink) Write(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	const columns = `(event_id, received_at, occurred_at, anonymous_installation_id,
event_name, command_path, flag_names_json, exit_code, error_code, duration_ms,
cloud_provider, region_code, cli_version, os, arch, install_source,
profile_source, tag, extra_json, schema_version)`
	const placeholders = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

	values := make([]string, 0, len(events))
	args := make([]any, 0, len(events)*20)
	for _, event := range events {
		flagNames, err := json.Marshal(event.FlagNames)
		if err != nil {
			return fmt.Errorf("encode flag names: %w", err)
		}
		var extra any
		if event.Extra != nil {
			extra = string(event.Extra)
		}
		values = append(values, placeholders)
		args = append(args,
			event.EventID,
			event.ReceivedAt,
			event.OccurredAt,
			event.AnonymousInstallationID,
			event.EventName,
			event.CommandPath,
			string(flagNames),
			event.ExitCode,
			event.ErrorCode,
			event.DurationMS,
			event.CloudProvider,
			event.RegionCode,
			event.CLIVersion,
			event.OS,
			event.Arch,
			event.InstallSource,
			event.ProfileSource,
			event.Tag,
			extra,
			event.SchemaVersion,
		)
	}
	query := "INSERT IGNORE INTO telemetry_events " + columns + " VALUES " + strings.Join(values, ",")
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("insert telemetry batch: %w", err)
	}
	return nil
}
