package telemetrybackend

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	values := map[string]string{
		"TELEMETRY_PUBLIC_HOST":            "telemetry.example.com",
		"TELEMETRY_ENVIRONMENT":            "test",
		"TELEMETRY_FLUSH_INTERVAL":         "250ms",
		"TELEMETRY_MAX_EVENTS_PER_REQUEST": "12",
		"TELEMETRY_TRUSTED_PROXY_CIDRS":    "10.0.0.0/8, 127.0.0.1/32",
		"TIDB_DSN":                         "user:password@tcp(localhost:4000)/telemetry",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.BindAddr != defaultBindAddr {
		t.Fatalf("BindAddr = %q, want %q", cfg.BindAddr, defaultBindAddr)
	}
	if cfg.FlushInterval != 250*time.Millisecond {
		t.Fatalf("FlushInterval = %v", cfg.FlushInterval)
	}
	if cfg.MaxEventsPerRequest != 12 {
		t.Fatalf("MaxEventsPerRequest = %d", cfg.MaxEventsPerRequest)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("TrustedProxyCIDRs length = %d", len(cfg.TrustedProxyCIDRs))
	}
}

func TestLoadConfigRejectsProductionWithoutVerifiedTLS(t *testing.T) {
	values := map[string]string{
		"TELEMETRY_PUBLIC_HOST": "telemetry.example.com",
		"TIDB_DSN":              "user:password@tcp(localhost:4000)/telemetry?tls=skip-verify",
	}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "verified TLS") {
		t.Fatalf("LoadConfig error = %v, want verified TLS error", err)
	}
}

func TestLoadConfigRejectsInvalidAndInconsistentLimits(t *testing.T) {
	values := map[string]string{
		"TELEMETRY_PUBLIC_HOST":       "telemetry.example.com",
		"TELEMETRY_ENVIRONMENT":       "test",
		"TELEMETRY_BUFFER_MAX_EVENTS": "10",
		"TELEMETRY_FLUSH_MAX_EVENTS":  "11",
		"TIDB_DSN":                    "user:password@tcp(localhost:4000)/telemetry",
	}
	_, err := LoadConfig(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "cannot exceed") {
		t.Fatalf("LoadConfig error = %v, want inconsistent limit error", err)
	}
}

func TestValidateTiDBDSN(t *testing.T) {
	const (
		verified   = "user:password@tcp(localhost:4000)/telemetry?tls=true"
		unverified = "user:password@tcp(localhost:4000)/telemetry?tls=skip-verify"
		plain      = "user:password@tcp(localhost:4000)/telemetry"
	)
	for _, tc := range []struct {
		name        string
		environment string
		dsn         string
		wantErr     string
	}{
		{"production with verified TLS", "production", verified, ""},
		{"empty environment means production", "", unverified, "verified TLS"},
		{"blank environment means production", "  ", plain, "verified TLS"},
		{"production without TLS", "Production", plain, "verified TLS"},
		{"non-production without TLS", "telemetry-e2e", plain, ""},
		{"missing DSN", "telemetry-e2e", "", "required"},
		{"invalid DSN", "telemetry-e2e", "not a dsn", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTiDBDSN(tc.environment, tc.dsn)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTiDBDSN = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateTiDBDSN = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
