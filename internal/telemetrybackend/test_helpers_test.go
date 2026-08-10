package telemetrybackend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

func testConfig() Config {
	return Config{
		BindAddr:             ":8080",
		PublicHost:           "telemetry.example.com",
		Environment:          "test",
		MaxBodyBytes:         64 * 1024,
		MaxEventsPerRequest:  20,
		BufferMaxEvents:      100,
		FlushMaxEvents:       10,
		FlushMaxBytes:        256 * 1024,
		FlushInterval:        time.Hour,
		ShutdownDrainTimeout: time.Second,
		SinkTimeout:          time.Second,
		RateLimitPerMinute:   60,
		RateLimitBurst:       120,
		TiDBDSN:              "user:password@tcp(localhost:4000)/telemetry?tls=true",
		PostHogAPIHost:       "https://us.i.posthog.com",
		PostHogProjectToken:  "phc_test",
	}
}

func testEvent() Event {
	return Event{
		EventID:                 "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
		EventName:               "ti.command.finished",
		OccurredAt:              time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		ReceivedAt:              time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC),
		AnonymousInstallationID: "ti_01j0a0n8m9f4q2x6cn0b9q3k3z",
		CommandPath:             "ti fs create-file-system",
		FlagNames:               []string{"file-system-name", "output"},
		ExitCode:                0,
		ErrorCode:               "",
		DurationMS:              182,
		CloudProvider:           "aws",
		RegionCode:              "aws-us-east-1",
		CLIVersion:              "0.2.0",
		OS:                      "darwin",
		Arch:                    "arm64",
		InstallSource:           "github-release",
		ProfileSource:           "default",
		SchemaVersion:           1,
	}
}

func validRequestBody() []byte {
	exitCode := 0
	duration := int64(182)
	body, _ := json.Marshal(batchRequest{
		SchemaVersion: 1,
		SentAt:        "2026-07-24T12:00:00Z",
		Events: []wireEvent{{
			EventID:                 "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
			EventName:               "ti.command.finished",
			OccurredAt:              "2026-07-24T12:00:00Z",
			AnonymousInstallationID: "ti_01j0a0n8m9f4q2x6cn0b9q3k3z",
			CommandPath:             "ti fs create-file-system",
			FlagNames:               []string{"file-system-name", "output"},
			ExitCode:                &exitCode,
			DurationMS:              &duration,
			CloudProvider:           "aws",
			RegionCode:              "aws-us-east-1",
			CLIVersion:              "0.2.0",
			OS:                      "linux",
			Arch:                    "amd64",
			InstallSource:           "github-release",
			ProfileSource:           "default",
		}},
	})
	return body
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingSink struct {
	name  string
	err   error
	delay time.Duration

	mu      sync.Mutex
	batches [][]Event
	called  chan struct{}
}

func newRecordingSink(name string, err error) *recordingSink {
	return &recordingSink{name: name, err: err, called: make(chan struct{}, 16)}
}

func (s *recordingSink) Name() string {
	return s.name
}

func (s *recordingSink) Write(ctx context.Context, events []Event) error {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	batch := make([]Event, len(events))
	for index, event := range events {
		batch[index] = cloneEvent(event)
	}
	s.batches = append(s.batches, batch)
	s.mu.Unlock()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return s.err
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

func (s *recordingSink) eventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, batch := range s.batches {
		total += len(batch)
	}
	return total
}

type readinessStub struct {
	err error
}

func (s readinessStub) Ready(context.Context) error {
	return s.err
}

var errTestSink = errors.New("sink unavailable")
