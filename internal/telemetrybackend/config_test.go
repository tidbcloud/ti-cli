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
