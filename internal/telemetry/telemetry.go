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
	"unicode/utf8"

	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/store"
	"github.com/tidbcloud/ti-cli/internal/settings"
	"github.com/tidbcloud/ti-cli/internal/version"
)

const (
	EnvironmentVariable      = "TI_TELEMETRY"
	TagEnvironmentVariable   = "TI_TELEMETRY_TAG"
	ExtraEnvironmentVariable = "TI_TELEMETRY_EXTRA"
	installationIDFile       = ".telemetry-installation-id"
	eventName                = "ti.command.finished"
	schemaVersion            = 2
	deliveryTimeout          = 3 * time.Second
	maxTagBytes              = 128
	maxExtraBytes            = 2048
	maxExtraDepth            = 8
)

var installationIDPattern = regexp.MustCompile(`^(?:ti|tdc)_[A-Za-z0-9_-]{22}$`)

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
	tag            string
	extra          json.RawMessage
}

type batchRequest struct {
	SchemaVersion int         `json:"schema_version"`
	SentAt        string      `json:"sent_at"`
	Events        []wireEvent `json:"events"`
}

type wireEvent struct {
	EventID                 string          `json:"event_id"`
	EventName               string          `json:"event_name"`
	OccurredAt              string          `json:"occurred_at"`
	AnonymousInstallationID string          `json:"anonymous_installation_id"`
	CommandPath             string          `json:"command_path"`
	FlagNames               []string        `json:"flag_names"`
	ExitCode                int             `json:"exit_code"`
	ErrorCode               string          `json:"error_code"`
	DurationMS              int64           `json:"duration_ms"`
	CloudProvider           string          `json:"cloud_provider"`
	RegionCode              string          `json:"region_code"`
	CLIVersion              string          `json:"cli_version"`
	OS                      string          `json:"os"`
	Arch                    string          `json:"arch"`
	InstallSource           string          `json:"install_source"`
	ProfileSource           string          `json:"profile_source"`
	Tag                     string          `json:"tag,omitempty"`
	Extra                   json.RawMessage `json:"extra,omitempty"`
}

func InstallationIDPath(homeDir string) string {
	return filepath.Join(homeDir, store.TIDirName, installationIDFile)
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
	tagValue, tagExists, _, err := envcompat.ResolveNames(cfg.Environment, TagEnvironmentVariable, "TDC_TELEMETRY_TAG")
	if err != nil {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry disabled because environment metadata conflicts")
		return nil
	}
	tag, tagOK := normalizeTag(tagValue, tagExists)
	if !tagOK {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry tag was omitted because it is invalid")
	}
	extraValue, extraExists, _, err := envcompat.ResolveNames(cfg.Environment, ExtraEnvironmentVariable, "TDC_TELEMETRY_EXTRA")
	if err != nil {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry disabled because environment metadata conflicts")
		return nil
	}
	extra, extraOK := normalizeExtra(extraValue, extraExists)
	if !extraOK {
		debug(cfg.Debug, cfg.DebugWriter, "telemetry extra metadata was omitted because it is invalid")
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
		tag:            tag,
		extra:          extra,
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
		Tag:                     s.tag,
		Extra:                   s.extra,
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
	request.Header.Set("User-Agent", "ti/"+normalizedVersion(s.info.Version))
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

func normalizeTag(value string, exists bool) (string, bool) {
	if !exists || value == "" {
		return "", true
	}
	if !utf8.ValidString(value) {
		return "", false
	}
	if len(value) <= maxTagBytes {
		return value, true
	}
	for len(value) > maxTagBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value, true
}

func normalizeExtra(value string, exists bool) (json.RawMessage, bool) {
	if !exists || value == "" || !utf8.ValidString(value) {
		return nil, !exists || value == ""
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if extraMetadataProhibited(decoded, 0) {
		return nil, false
	}
	encoded, err := json.Marshal(decoded)
	if err != nil || len(encoded) > maxExtraBytes {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

func extraMetadataProhibited(value any, depth int) bool {
	if depth > maxExtraDepth {
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, prohibited := prohibitedMetadataKeys[strings.ToLower(key)]; prohibited || extraMetadataProhibited(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if extraMetadataProhibited(child, depth+1) {
				return true
			}
		}
	}
	return false
}

var prohibitedMetadataKeys = map[string]struct{}{
	"api_payload": {}, "branch_id": {}, "cluster_id": {}, "command_output": {},
	"credential": {}, "credentials": {}, "file_content": {}, "file_path": {},
	"flag_value": {}, "flag_values": {}, "hostname": {}, "ip_address": {},
	"journal_id": {}, "layer_id": {}, "machine_id": {}, "mac_address": {},
	"password": {}, "path": {}, "profile_name": {}, "project_id": {},
	"raw_error": {}, "request_body": {}, "resource_id": {}, "response_body": {},
	"secret": {}, "sql": {}, "sql_text": {}, "tenant_id": {}, "token": {}, "username": {},
}

func resolveEnabled(cfg Config) (bool, error) {
	raw, exists, _, err := envcompat.ResolveNames(cfg.Environment, EnvironmentVariable, "TDC_TELEMETRY")
	if err != nil {
		return false, err
	}
	if exists {
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
	id, err = randomIdentifier("ti_")
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
		_, _ = fmt.Fprintln(writer, "ti [DEBUG]: "+message)
	}
}
