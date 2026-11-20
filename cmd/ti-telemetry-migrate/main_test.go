package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRunRefusesProductionDSNWithoutVerifiedTLS(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
	}{
		{"unset environment defaults to production", ""},
		{"explicit production", "production"},
		{"misspelled production", "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"TIDB_DSN":              "user:secret-password@tcp(203.0.113.10:4000)/telemetry?tls=skip-verify",
				"TELEMETRY_ENVIRONMENT": tc.environment,
			}
			var logs bytes.Buffer
			code := run(nil, func(key string) string { return env[key] }, slog.New(slog.NewJSONHandler(&logs, nil)))
			if code != 1 {
				t.Fatalf("run exit code = %d, want 1", code)
			}
			if !strings.Contains(logs.String(), "verified TLS") {
				t.Fatalf("logs = %s, want a verified TLS refusal", logs.String())
			}
			if strings.Contains(logs.String(), "secret-password") {
				t.Fatalf("logs leak the DSN password: %s", logs.String())
			}
			if strings.Contains(logs.String(), "migrations failed") {
				t.Fatalf("run tried to migrate before refusing the DSN: %s", logs.String())
			}
		})
	}
}

func TestRunRequiresDSN(t *testing.T) {
	var logs bytes.Buffer
	code := run(nil, func(string) string { return "" }, slog.New(slog.NewJSONHandler(&logs, nil)))
	if code != 1 || !strings.Contains(logs.String(), "TIDB_DSN is required") {
		t.Fatalf("run exit code = %d, logs = %s", code, logs.String())
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	var logs bytes.Buffer
	code := run([]string{"--unknown"}, func(string) string { return "" }, slog.New(slog.NewJSONHandler(&logs, nil)))
	if code != 2 {
		t.Fatalf("run exit code = %d, want 2", code)
	}
}
