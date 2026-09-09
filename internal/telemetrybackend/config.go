package telemetrybackend

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	defaultBindAddr             = ":8080"
	defaultMaxBodyBytes         = 64 * 1024
	defaultMaxEventsPerRequest  = 20
	defaultBufferMaxEvents      = 10_000
	defaultFlushMaxEvents       = 100
	defaultFlushMaxBytes        = 256 * 1024
	defaultFlushInterval        = 5 * time.Second
	defaultShutdownDrainTimeout = 5 * time.Second
	defaultSinkTimeout          = 5 * time.Second
	defaultRateLimitPerMinute   = 60
	defaultRateLimitBurst       = 120
)

// Config contains all runtime settings for the ingestion service.
type Config struct {
	BindAddr             string
	PublicHost           string
	Environment          string
	MaxBodyBytes         int64
	MaxEventsPerRequest  int
	BufferMaxEvents      int
	FlushMaxEvents       int
	FlushMaxBytes        int
	FlushInterval        time.Duration
	ShutdownDrainTimeout time.Duration
	SinkTimeout          time.Duration
	RateLimitPerMinute   int
	RateLimitBurst       int
	TrustedProxyCIDRs    []netip.Prefix
	TiDBDSN              string
}

// LoadConfig reads configuration through getenv. Docker Compose supplies these
// values from the server-local .env file.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		BindAddr:             valueOrDefault(getenv("TELEMETRY_BIND_ADDR"), defaultBindAddr),
		PublicHost:           strings.TrimSpace(getenv("TELEMETRY_PUBLIC_HOST")),
		Environment:          valueOrDefault(getenv("TELEMETRY_ENVIRONMENT"), "production"),
		MaxBodyBytes:         defaultMaxBodyBytes,
		MaxEventsPerRequest:  defaultMaxEventsPerRequest,
		BufferMaxEvents:      defaultBufferMaxEvents,
		FlushMaxEvents:       defaultFlushMaxEvents,
		FlushMaxBytes:        defaultFlushMaxBytes,
		FlushInterval:        defaultFlushInterval,
		ShutdownDrainTimeout: defaultShutdownDrainTimeout,
		SinkTimeout:          defaultSinkTimeout,
		RateLimitPerMinute:   defaultRateLimitPerMinute,
		RateLimitBurst:       defaultRateLimitBurst,
		TiDBDSN:              strings.TrimSpace(getenv("TIDB_DSN")),
	}

	var err error
	if cfg.MaxBodyBytes, err = envInt64(getenv, "TELEMETRY_MAX_BODY_BYTES", cfg.MaxBodyBytes); err != nil {
		return Config{}, err
	}
	intSettings := []struct {
		name string
		dst  *int
	}{
		{"TELEMETRY_MAX_EVENTS_PER_REQUEST", &cfg.MaxEventsPerRequest},
		{"TELEMETRY_BUFFER_MAX_EVENTS", &cfg.BufferMaxEvents},
		{"TELEMETRY_FLUSH_MAX_EVENTS", &cfg.FlushMaxEvents},
		{"TELEMETRY_FLUSH_MAX_BYTES", &cfg.FlushMaxBytes},
		{"TELEMETRY_RATE_LIMIT_PER_MINUTE", &cfg.RateLimitPerMinute},
		{"TELEMETRY_RATE_LIMIT_BURST", &cfg.RateLimitBurst},
	}
	for _, setting := range intSettings {
		if *setting.dst, err = envInt(getenv, setting.name, *setting.dst); err != nil {
			return Config{}, err
		}
	}
	durationSettings := []struct {
		name string
		dst  *time.Duration
	}{
		{"TELEMETRY_FLUSH_INTERVAL", &cfg.FlushInterval},
		{"TELEMETRY_SHUTDOWN_DRAIN_TIMEOUT", &cfg.ShutdownDrainTimeout},
		{"TELEMETRY_SINK_TIMEOUT", &cfg.SinkTimeout},
	}
	for _, setting := range durationSettings {
		if *setting.dst, err = envDuration(getenv, setting.name, *setting.dst); err != nil {
			return Config{}, err
		}
	}
	cfg.TrustedProxyCIDRs, err = parseTrustedProxyCIDRs(valueOrDefault(
		getenv("TELEMETRY_TRUSTED_PROXY_CIDRS"),
		"127.0.0.0/8,::1/128",
	))
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects unsafe or internally inconsistent runtime settings.
func (c Config) Validate() error {
	if strings.TrimSpace(c.BindAddr) == "" {
		return fmt.Errorf("TELEMETRY_BIND_ADDR is required")
	}
	if strings.TrimSpace(c.PublicHost) == "" {
		return fmt.Errorf("TELEMETRY_PUBLIC_HOST is required")
	}
	if strings.TrimSpace(c.Environment) == "" {
		return fmt.Errorf("TELEMETRY_ENVIRONMENT is required")
	}
	if c.MaxBodyBytes <= 0 || c.MaxEventsPerRequest <= 0 || c.BufferMaxEvents <= 0 {
		return fmt.Errorf("telemetry request and buffer limits must be positive")
	}
	if c.FlushMaxEvents <= 0 || c.FlushMaxBytes <= 0 {
		return fmt.Errorf("telemetry flush limits must be positive")
	}
	if c.FlushMaxEvents > c.BufferMaxEvents {
		return fmt.Errorf("TELEMETRY_FLUSH_MAX_EVENTS cannot exceed TELEMETRY_BUFFER_MAX_EVENTS")
	}
	if c.FlushInterval <= 0 || c.ShutdownDrainTimeout <= 0 || c.SinkTimeout <= 0 {
		return fmt.Errorf("telemetry durations must be positive")
	}
	if c.RateLimitPerMinute <= 0 || c.RateLimitBurst <= 0 {
		return fmt.Errorf("telemetry rate limits must be positive")
	}
	if c.TiDBDSN == "" {
		return fmt.Errorf("TIDB_DSN is required")
	}
	dsn, err := mysql.ParseDSN(c.TiDBDSN)
	if err != nil {
		return fmt.Errorf("TIDB_DSN is invalid")
	}
	if strings.EqualFold(c.Environment, "production") {
		switch strings.ToLower(dsn.TLSConfig) {
		case "", "false", "skip-verify", "preferred":
			return fmt.Errorf("TIDB_DSN must enable verified TLS in production")
		}
	}
	return nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func envInt(getenv func(string) string, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func envInt64(getenv func(string) string, name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func envDuration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration", name)
	}
	return value, nil
}

func parseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("TELEMETRY_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
