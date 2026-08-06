package telemetrybackend

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	eventSchemaVersionV1 = 1
	eventSchemaVersionV2 = 2
	maxTagBytes          = 128
	maxExtraBytes        = 2048
	maxExtraDepth        = 8
)

var (
	eventIDPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	installationIDPattern = regexp.MustCompile(`^tdc_[A-Za-z0-9_-]{16,96}$`)
	commandPathPattern    = regexp.MustCompile(`^tdc(?: [a-z][a-z0-9-]{0,63}){0,2}$`)
	flagNamePattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	errorCodePattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
	cliVersionPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)
)

var disallowedFieldNames = map[string]struct{}{
	"api_payload":    {},
	"branch_id":      {},
	"cluster_id":     {},
	"command_output": {},
	"credential":     {},
	"credentials":    {},
	"file_content":   {},
	"file_path":      {},
	"flag_value":     {},
	"flag_values":    {},
	"hostname":       {},
	"ip_address":     {},
	"journal_id":     {},
	"layer_id":       {},
	"machine_id":     {},
	"mac_address":    {},
	"password":       {},
	"path":           {},
	"profile_name":   {},
	"project_id":     {},
	"raw_error":      {},
	"request_body":   {},
	"response_body":  {},
	"resource_id":    {},
	"sql":            {},
	"sql_text":       {},
	"secret":         {},
	"tenant_id":      {},
	"token":          {},
	"username":       {},
}

var allowedRegions = map[string]struct{}{
	"":                   {},
	"unknown":            {},
	"aws-us-east-1":      {},
	"aws-us-west-2":      {},
	"aws-eu-central-1":   {},
	"aws-ap-northeast-1": {},
	"aws-ap-southeast-1": {},
	"ali-ap-southeast-1": {},
}

var allowedOperatingSystems = map[string]struct{}{
	"aix": {}, "android": {}, "darwin": {}, "dragonfly": {}, "freebsd": {},
	"illumos": {}, "ios": {}, "js": {}, "linux": {}, "netbsd": {},
	"openbsd": {}, "plan9": {}, "solaris": {}, "wasip1": {}, "windows": {},
}

var allowedArchitectures = map[string]struct{}{
	"386": {}, "amd64": {}, "arm": {}, "arm64": {}, "loong64": {},
	"mips": {}, "mips64": {}, "mips64le": {}, "mipsle": {}, "ppc64": {},
	"ppc64le": {}, "riscv64": {}, "s390x": {}, "wasm": {},
}

// Event is the validated, allowlisted representation used by both sinks.
type Event struct {
	EventID                 string
	EventName               string
	OccurredAt              time.Time
	ReceivedAt              time.Time
	AnonymousInstallationID string
	CommandPath             string
	FlagNames               []string
	ExitCode                int
	ErrorCode               string
	DurationMS              int64
	CloudProvider           string
	RegionCode              string
	CLIVersion              string
	OS                      string
	Arch                    string
	InstallSource           string
	ProfileSource           string
	Tag                     string
	Extra                   json.RawMessage
	SchemaVersion           int
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
	ExitCode                *int            `json:"exit_code"`
	ErrorCode               string          `json:"error_code"`
	DurationMS              *int64          `json:"duration_ms"`
	CloudProvider           string          `json:"cloud_provider"`
	RegionCode              string          `json:"region_code"`
	CLIVersion              string          `json:"cli_version"`
	OS                      string          `json:"os"`
	Arch                    string          `json:"arch"`
	InstallSource           string          `json:"install_source"`
	ProfileSource           string          `json:"profile_source"`
	Tag                     *string         `json:"tag,omitempty"`
	Extra                   json.RawMessage `json:"extra,omitempty"`
}

func decodeAndValidateBatch(body []byte, maxEvents int, receivedAt time.Time) ([]Event, error) {
	if hasDisallowedField(body) {
		return nil, errors.New("request contains a prohibited field")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request batchRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, errors.New("invalid JSON schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values")
	}
	if request.SchemaVersion != eventSchemaVersionV1 && request.SchemaVersion != eventSchemaVersionV2 {
		return nil, errors.New("unsupported schema version")
	}
	if _, err := time.Parse(time.RFC3339Nano, request.SentAt); err != nil {
		return nil, errors.New("invalid sent_at")
	}
	if len(request.Events) == 0 || len(request.Events) > maxEvents {
		return nil, errors.New("invalid event count")
	}

	events := make([]Event, 0, len(request.Events))
	for _, raw := range request.Events {
		event, err := validateEvent(raw, request.SchemaVersion, receivedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func validateEvent(raw wireEvent, schemaVersion int, receivedAt time.Time) (Event, error) {
	if !eventIDPattern.MatchString(raw.EventID) {
		return Event{}, errors.New("invalid event_id")
	}
	if raw.EventName != "tdc.command.finished" {
		return Event{}, errors.New("invalid event_name")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, raw.OccurredAt)
	if err != nil {
		return Event{}, errors.New("invalid occurred_at")
	}
	if !installationIDPattern.MatchString(raw.AnonymousInstallationID) {
		return Event{}, errors.New("invalid anonymous_installation_id")
	}
	if !validString(raw.CommandPath, 128) || !commandPathPattern.MatchString(raw.CommandPath) {
		return Event{}, errors.New("invalid command_path")
	}
	if raw.FlagNames == nil || len(raw.FlagNames) > 64 {
		return Event{}, errors.New("invalid flag_names")
	}
	for _, flag := range raw.FlagNames {
		if !flagNamePattern.MatchString(flag) {
			return Event{}, errors.New("invalid flag_names")
		}
	}
	if raw.ExitCode == nil || *raw.ExitCode < 0 || *raw.ExitCode > 255 {
		return Event{}, errors.New("invalid exit_code")
	}
	if !errorCodePattern.MatchString(raw.ErrorCode) {
		return Event{}, errors.New("invalid error_code")
	}
	if raw.DurationMS == nil || *raw.DurationMS < 0 || *raw.DurationMS > 86_400_000 {
		return Event{}, errors.New("invalid duration_ms")
	}
	if !oneOf(raw.CloudProvider, "", "aws", "alibaba_cloud", "unknown") {
		return Event{}, errors.New("invalid cloud_provider")
	}
	if _, ok := allowedRegions[raw.RegionCode]; !ok {
		return Event{}, errors.New("invalid region_code")
	}
	if !cliVersionPattern.MatchString(raw.CLIVersion) {
		return Event{}, errors.New("invalid cli_version")
	}
	if _, ok := allowedOperatingSystems[raw.OS]; !ok {
		return Event{}, errors.New("invalid os")
	}
	if _, ok := allowedArchitectures[raw.Arch]; !ok {
		return Event{}, errors.New("invalid runtime metadata")
	}
	if !oneOf(raw.InstallSource, "", "github-release", "homebrew", "scoop", "source", "dev", "unknown") {
		return Event{}, errors.New("invalid install_source")
	}
	if !oneOf(raw.ProfileSource, "", "default", "explicit", "env", "unknown") {
		return Event{}, errors.New("invalid profile_source")
	}
	tag, extra, err := validateMetadata(raw, schemaVersion)
	if err != nil {
		return Event{}, err
	}

	return Event{
		EventID:                 raw.EventID,
		EventName:               raw.EventName,
		OccurredAt:              occurredAt.UTC(),
		ReceivedAt:              receivedAt.UTC(),
		AnonymousInstallationID: raw.AnonymousInstallationID,
		CommandPath:             raw.CommandPath,
		FlagNames:               append([]string(nil), raw.FlagNames...),
		ExitCode:                *raw.ExitCode,
		ErrorCode:               raw.ErrorCode,
		DurationMS:              *raw.DurationMS,
		CloudProvider:           raw.CloudProvider,
		RegionCode:              raw.RegionCode,
		CLIVersion:              raw.CLIVersion,
		OS:                      raw.OS,
		Arch:                    raw.Arch,
		InstallSource:           raw.InstallSource,
		ProfileSource:           raw.ProfileSource,
		Tag:                     tag,
		Extra:                   extra,
		SchemaVersion:           schemaVersion,
	}, nil
}

func validateMetadata(raw wireEvent, schemaVersion int) (string, json.RawMessage, error) {
	if schemaVersion == eventSchemaVersionV1 {
		if raw.Tag != nil || raw.Extra != nil {
			return "", nil, errors.New("metadata requires schema version 2")
		}
		return "", nil, nil
	}
	var tag string
	if raw.Tag != nil {
		tag = *raw.Tag
		if !validString(tag, maxTagBytes) {
			return "", nil, errors.New("invalid tag")
		}
	}
	extra, err := validateExtra(raw.Extra)
	if err != nil {
		return "", nil, err
	}
	return tag, extra, nil
}

func validateExtra(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("invalid extra")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid extra")
	}
	if walkForInvalidExtra(value, 0) {
		return nil, errors.New("invalid extra")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxExtraBytes {
		return nil, errors.New("invalid extra")
	}
	return json.RawMessage(encoded), nil
}

func walkForInvalidExtra(value any, depth int) bool {
	if depth > maxExtraDepth {
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, prohibited := disallowedFieldNames[strings.ToLower(key)]; prohibited || walkForInvalidExtra(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if walkForInvalidExtra(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func estimateEventBytes(event Event) int {
	encoded, err := json.Marshal(eventForSize(event))
	if err != nil {
		return 1
	}
	return len(encoded)
}

func eventForSize(event Event) map[string]any {
	return map[string]any{
		"event_id":                  event.EventID,
		"event_name":                event.EventName,
		"occurred_at":               event.OccurredAt,
		"anonymous_installation_id": event.AnonymousInstallationID,
		"command_path":              event.CommandPath,
		"flag_names":                event.FlagNames,
		"exit_code":                 event.ExitCode,
		"error_code":                event.ErrorCode,
		"duration_ms":               event.DurationMS,
		"cloud_provider":            event.CloudProvider,
		"region_code":               event.RegionCode,
		"cli_version":               event.CLIVersion,
		"os":                        event.OS,
		"arch":                      event.Arch,
		"install_source":            event.InstallSource,
		"profile_source":            event.ProfileSource,
		"tag":                       event.Tag,
		"extra":                     event.Extra,
		"schema_version":            event.SchemaVersion,
	}
}

func hasDisallowedField(body []byte) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	return walkForDisallowedField(value)
}

func walkForDisallowedField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, prohibited := disallowedFieldNames[strings.ToLower(key)]; prohibited {
				return true
			}
			if walkForDisallowedField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if walkForDisallowedField(child) {
				return true
			}
		}
	}
	return false
}

func validString(value string, maxBytes int) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
