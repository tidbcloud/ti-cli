package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tidbcloud/tdc/internal/config/store"
	"github.com/tidbcloud/tdc/internal/settings"
	"github.com/tidbcloud/tdc/internal/version"
)

const (
	EnvironmentVariable = "TDC_TELEMETRY"
	installationIDFile  = ".telemetry-installation-id"
	eventName           = "tdc.command.finished"
	schemaVersion       = 1
	deliveryTimeout     = 3 * time.Second
)

var installationIDPattern = regexp.MustCompile(`^tdc_[A-Za-z0-9_-]{22}$`)

type Config struct {
	Eligible    bool
	HomeDir     string
	Endpoint    string
	Info        version.Info
	Environment map[string]string
	Client      *http.Client
	Debug       bool
	DebugWriter io.Writer
	Now         func() time.Time
}

type EventInput struct {
	CommandPath   string
	FlagNames     []string
	ExitCode      int
	ErrorCode     string
	Duration      time.Duration
	CloudProvider string
	RegionCode    string
	ProfileSource string
}

type Session struct {
	installationID string
	endpoint       string
	info           version.Info
	client         *http.Client
	debug          bool
	debugWriter    io.Writer
	now            func() time.Time
}

type batchRequest struct {
	SchemaVersion int         `json:"schema_version"`
	SentAt        string      `json:"sent_at"`
	Events        []wireEvent `json:"events"`
}

type wireEvent struct {
	EventID                 string   `json:"event_id"`
	EventName               string   `json:"event_name"`
	OccurredAt              string   `json:"occurred_at"`
	AnonymousInstallationID string   `json:"anonymous_installation_id"`
	CommandPath             string   `json:"command_path"`
	FlagNames               []string `json:"flag_names"`
	ExitCode                int      `json:"exit_code"`
	ErrorCode               string   `json:"error_code"`
	DurationMS              int64    `json:"duration_ms"`
	CloudProvider           string   `json:"cloud_provider"`
	RegionCode              string   `json:"region_code"`
	CLIVersion              string   `json:"cli_version"`
	OS                      string   `json:"os"`
	Arch                    string   `json:"arch"`
	InstallSource           string   `json:"install_source"`
	ProfileSource           string   `json:"profile_source"`
}

func InstallationIDPath(homeDir string) string {
	return filepath.Join(homeDir, store.TDCDirName, installationIDFile)
}

func Start(cfg Config) *Session {
	if !cfg.Eligible || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	enabled, err := resolveEnabled(cfg)
	if err != nil {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry disabled because its preference could not be resolved")
		return nil
	}
	if !enabled {
		return nil
	}
	homeDir := strings.TrimSpace(cfg.HomeDir)
	if homeDir == "" {
		homeDir, err = os.UserHomeDir()
		if err != nil {
			debug(cfg.Debug, cfg.DebugWriter, "telemetry disabled because its local identity is unavailable")
			return nil
		}
	}
	id, err := loadOrCreateInstallationID(homeDir)
	if err != nil {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry disabled because its local identity is unavailable")
		return nil
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{
			Timeout: deliveryTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Session{
		installationID: id,
		endpoint:       cfg.Endpoint,
		info:           cfg.Info,
		client:         client,
		debug:          cfg.Debug,
		debugWriter:    cfg.DebugWriter,
		now:            now,
	}
}

func (s *Session) Finish(input EventInput) {
	if s == nil {
		return
	}
	now := s.now().UTC()
	eventID, err := randomIdentifier("")
	if err != nil {
		debug(s.debug, s.debugWriter, "telemetry event was dropped before delivery")
		return
	}
	flagNames := append([]string(nil), input.FlagNames...)
	sort.Strings(flagNames)
	durationMS := input.Duration.Milliseconds()
	if durationMS < 0 {
		durationMS = 0
	}
	if durationMS > 86_400_000 {
		durationMS = 86_400_000
	}
	event := wireEvent{
		EventID:                 eventID,
		EventName:               eventName,
		OccurredAt:              now.Format(time.RFC3339Nano),
		AnonymousInstallationID: s.installationID,
		CommandPath:             input.CommandPath,
		FlagNames:               flagNames,
		ExitCode:                input.ExitCode,
		ErrorCode:               input.ErrorCode,
		DurationMS:              durationMS,
		CloudProvider:           normalizedProvider(input.CloudProvider),
		RegionCode:              normalizedRegion(input.RegionCode),
		CLIVersion:              normalizedVersion(s.info.Version),
		OS:                      runtimeValue(s.info.OS, runtime.GOOS),
		Arch:                    runtimeValue(s.info.Arch, runtime.GOARCH),
		InstallSource:           normalizedInstallSource(s.info.InstallSource),
		ProfileSource:           normalizedProfileSource(input.ProfileSource),
	}
	payload, err := json.Marshal(batchRequest{
		SchemaVersion: schemaVersion,
		SentAt:        now.Format(time.RFC3339Nano),
		Events:        []wireEvent{event},
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), deliveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		debug(s.debug, s.debugWriter, "telemetry event was dropped before delivery")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "tdc/"+normalizedVersion(s.info.Version))
	response, err := s.client.Do(request)
	if err != nil {
		debug(s.debug, s.debugWriter, "telemetry delivery failed; the command result was not affected")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		debug(s.debug, s.debugWriter, "telemetry delivery was rejected; the command result was not affected")
	}
}

func resolveEnabled(cfg Config) (bool, error) {
	if raw, exists := envValue(cfg.Environment, EnvironmentVariable); exists {
		enabled, valid := parseOverride(raw)
		if !valid {
			return false, fmt.Errorf("invalid telemetry override")
		}
		return enabled, nil
	}
	doc, exists, err := settings.Load(cfg.HomeDir)
	if err != nil {
		return false, err
	}
	if exists && doc.Telemetry.Enabled != nil {
		return *doc.Telemetry.Enabled, nil
	}
	return isReleaseBuild(cfg.Info) && !isCI(cfg.Environment), nil
}

func isReleaseBuild(info version.Info) bool {
	switch strings.ToLower(strings.TrimSpace(info.InstallSource)) {
	case "archive", "github-release", "homebrew", "scoop":
		return info.Version != "" && info.Version != "dev"
	default:
		return false
	}
}

func loadOrCreateInstallationID(homeDir string) (string, error) {
	path := InstallationIDPath(homeDir)
	id, exists, err := readInstallationID(path)
	if err != nil || exists {
		return id, err
	}
	id, err = randomIdentifier("tdc_")
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, ".telemetry-installation-id.tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", err
	}
	if _, err := temp.WriteString(id + "\n"); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tempPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		persisted, exists, readErr := readInstallationID(path)
		if readErr != nil || !exists {
			return "", readErr
		}
		return persisted, nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func readInstallationID(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	id := strings.TrimSuffix(string(data), "\n")
	if strings.Contains(id, "\n") || !installationIDPattern.MatchString(id) {
		return "", true, fmt.Errorf("invalid telemetry installation ID")
	}
	return id, true, nil
}

func randomIdentifier(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func envValue(env map[string]string, key string) (string, bool) {
	if env != nil {
		value, ok := env[key]
		return value, ok
	}
	return os.LookupEnv(key)
}

func parseOverride(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1":
		return true, true
	case "off", "false", "0":
		return false, true
	default:
		return false, false
	}
}

func isCI(env map[string]string) bool {
	if value, ok := envValue(env, "CI"); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TF_BUILD", "JENKINS_URL"} {
		if value, ok := envValue(env, key); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func normalizedInstallSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "archive", "github-release":
		return "github-release"
	case "homebrew", "scoop", "source", "dev", "unknown":
		return strings.ToLower(strings.TrimSpace(value))
	case "local":
		return "dev"
	default:
		return "unknown"
	}
}

func normalizedProvider(value string) string {
	switch value {
	case "aws", "alibaba_cloud":
		return value
	case "":
		return ""
	default:
		return "unknown"
	}
}

func normalizedRegion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return value
}

func normalizedVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" || len(value) > 64 {
		return "unknown"
	}
	return value
}

func normalizedProfileSource(value string) string {
	switch value {
	case "default", "explicit", "env":
		return value
	default:
		return "unknown"
	}
}

func runtimeValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func debug(enabled bool, writer io.Writer, message string) {
	if enabled && writer != nil {
		_, _ = fmt.Fprintln(writer, "tdc [DEBUG]: "+message)
	}
}
