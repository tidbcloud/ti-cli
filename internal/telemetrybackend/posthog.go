package telemetrybackend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type PostHogSink struct {
	batchURL     string
	projectToken string
	environment  string
	client       *http.Client
}

type postHogBatch struct {
	APIKey              string         `json:"api_key"`
	HistoricalMigration bool           `json:"historical_migration"`
	Batch               []postHogEvent `json:"batch"`
}

type postHogEvent struct {
	Event      string            `json:"event"`
	Timestamp  string            `json:"timestamp"`
	Properties postHogProperties `json:"properties"`
}

type postHogProperties struct {
	DistinctID           string          `json:"distinct_id"`
	ProcessPersonProfile bool            `json:"$process_person_profile"`
	SchemaVersion        int             `json:"schema_version"`
	EventID              string          `json:"event_id"`
	CommandPath          string          `json:"command_path"`
	FlagNames            []string        `json:"flag_names"`
	ExitCode             int             `json:"exit_code"`
	ErrorCode            string          `json:"error_code"`
	DurationMS           int64           `json:"duration_ms"`
	CloudProvider        string          `json:"cloud_provider"`
	RegionCode           string          `json:"region_code"`
	CLIVersion           string          `json:"cli_version"`
	OS                   string          `json:"os"`
	Arch                 string          `json:"arch"`
	InstallSource        string          `json:"install_source"`
	ProfileSource        string          `json:"profile_source"`
	TDCEnvironment       string          `json:"tdc_environment"`
	Tag                  string          `json:"tag,omitempty"`
	Extra                json.RawMessage `json:"extra,omitempty"`
}

func NewPostHogSink(apiHost, projectToken, environment string, client *http.Client) (*PostHogSink, error) {
	u, err := url.Parse(apiHost)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid PostHog API host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/batch/"
	u.RawQuery = ""
	u.Fragment = ""
	if client == nil {
		client = http.DefaultClient
	}
	return &PostHogSink{
		batchURL:     u.String(),
		projectToken: projectToken,
		environment:  environment,
		client:       client,
	}, nil
}

func (s *PostHogSink) Name() string {
	return "posthog"
}

func (s *PostHogSink) Ready(context.Context) error {
	u, err := url.Parse(s.batchURL)
	if err != nil || u.Scheme == "" || u.Host == "" || s.projectToken == "" {
		return fmt.Errorf("PostHog sink is not configured")
	}
	return nil
}

func (s *PostHogSink) Write(ctx context.Context, events []Event) error {
	payload := postHogBatch{
		APIKey:              s.projectToken,
		HistoricalMigration: false,
		Batch:               make([]postHogEvent, 0, len(events)),
	}
	for _, event := range events {
		payload.Batch = append(payload.Batch, postHogEvent{
			Event:     event.EventName,
			Timestamp: event.OccurredAt.Format(timeRFC3339Nano),
			Properties: postHogProperties{
				DistinctID:           event.AnonymousInstallationID,
				ProcessPersonProfile: false,
				SchemaVersion:        event.SchemaVersion,
				EventID:              event.EventID,
				CommandPath:          event.CommandPath,
				FlagNames:            append([]string(nil), event.FlagNames...),
				ExitCode:             event.ExitCode,
				ErrorCode:            event.ErrorCode,
				DurationMS:           event.DurationMS,
				CloudProvider:        event.CloudProvider,
				RegionCode:           event.RegionCode,
				CLIVersion:           event.CLIVersion,
				OS:                   event.OS,
				Arch:                 event.Arch,
				InstallSource:        event.InstallSource,
				ProfileSource:        event.ProfileSource,
				TDCEnvironment:       s.environment,
				Tag:                  event.Tag,
				Extra:                event.Extra,
			},
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode PostHog batch: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.batchURL, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create PostHog request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("send PostHog batch: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("PostHog returned status %d", response.StatusCode)
	}
	return nil
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
