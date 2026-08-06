package migrations

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsAreOrderedAndForwardOnlyNonDestructive(t *testing.T) {
	entries, err := files.ReadDir("sql")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != "00001_create_telemetry_events.sql" || entries[1].Name() != "00002_add_environment_metadata.sql" {
		t.Fatalf("unexpected migration files: %#v", entries)
	}
	for _, entry := range entries {
		contents, err := files.ReadFile("sql/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		parts := strings.SplitN(string(contents), "-- +goose Down", 2)
		if len(parts) != 2 {
			t.Fatalf("%s is missing a Goose Down section", entry.Name())
		}
		upper := strings.ToUpper(parts[0])
		for _, prohibited := range []string{"DROP TABLE", "TRUNCATE TABLE", "DELETE FROM"} {
			if strings.Contains(upper, prohibited) {
				t.Fatalf("%s contains destructive statement %q", entry.Name(), prohibited)
			}
		}
	}
}

func TestEnvironmentMetadataMigrationHasAnAccurateRollback(t *testing.T) {
	contents, err := files.ReadFile("sql/00002_add_environment_metadata.sql")
	if err != nil {
		t.Fatalf("read environment metadata migration: %v", err)
	}
	parts := strings.SplitN(strings.ToUpper(string(contents)), "-- +GOOSE DOWN", 2)
	if len(parts) != 2 {
		t.Fatal("environment metadata migration is missing a Goose Down section")
	}

	down := parts[1]
	for _, statement := range []string{
		"DROP INDEX IDX_TAG_RECEIVED",
		"DROP COLUMN EXTRA_JSON",
		"DROP COLUMN TAG",
	} {
		if !strings.Contains(down, statement) {
			t.Fatalf("environment metadata rollback is missing %q", statement)
		}
	}
}
