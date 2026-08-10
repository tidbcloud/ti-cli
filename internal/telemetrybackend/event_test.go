package telemetrybackend

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeAndValidateBatchAcceptsAllowlistedEvent(t *testing.T) {
	receivedAt := time.Now()
	events, err := decodeAndValidateBatch(validRequestBody(), 20, receivedAt)
	if err != nil {
		t.Fatalf("decodeAndValidateBatch returned error: %v", err)
	}
	if len(events) != 1 || events[0].EventName != "ti.command.finished" {
		t.Fatalf("events = %#v", events)
	}
	if !events[0].ReceivedAt.Equal(receivedAt.UTC()) {
		t.Fatalf("ReceivedAt = %v, want %v", events[0].ReceivedAt, receivedAt.UTC())
	}
}

func TestDecodeAndValidateBatchAcceptsLegacyTDCEvent(t *testing.T) {
	body := bytes.ReplaceAll(validRequestBody(), []byte("ti.command.finished"), []byte("tdc.command.finished"))
	body = bytes.ReplaceAll(body, []byte("ti_01j0a0n8m9f4q2x6cn0b9q3k3z"), []byte("tdc_01j0a0n8m9f4q2x6cn0b9q3k3z"))
	body = bytes.ReplaceAll(body, []byte("ti db"), []byte("tdc db"))
	events, err := decodeAndValidateBatch(body, 20, time.Now())
	if err != nil {
		t.Fatalf("legacy event should remain accepted during v0.2.x: %v", err)
	}
	if len(events) != 1 || events[0].EventName != "tdc.command.finished" {
		t.Fatalf("unexpected legacy event: %#v", events)
	}
}

func TestDecodeAndValidateBatchSupportsV1AndV2Metadata(t *testing.T) {
	receivedAt := time.Now()
	for _, test := range []struct {
		name          string
		schemaVersion int
		tag           any
		extra         any
		extraSet      bool
		wantTag       string
		wantExtra     string
		wantAccepted  bool
	}{
		{name: "v1 unchanged", schemaVersion: 1, wantAccepted: true},
		{name: "v1 metadata rejected", schemaVersion: 1, tag: "e2b", wantAccepted: false},
		{name: "v2 metadata", schemaVersion: 2, tag: "e2b-preview", extra: map[string]any{"campaign": "launch"}, wantTag: "e2b-preview", wantExtra: `{"campaign":"launch"}`, wantAccepted: true},
		{name: "v2 string extra", schemaVersion: 2, extra: "sandbox", wantExtra: `"sandbox"`, wantAccepted: true},
		{name: "v2 array extra", schemaVersion: 2, extra: []any{"sandbox", float64(1)}, wantExtra: `["sandbox",1]`, wantAccepted: true},
		{name: "v2 number extra", schemaVersion: 2, extra: float64(1.25), wantExtra: `1.25`, wantAccepted: true},
		{name: "v2 boolean extra", schemaVersion: 2, extra: true, wantExtra: `true`, wantAccepted: true},
		{name: "v2 null extra", schemaVersion: 2, extraSet: true, wantExtra: `null`, wantAccepted: true},
		{name: "v2 prohibited extra", schemaVersion: 2, extra: map[string]any{"nested": map[string]any{"token": "secret"}}, wantAccepted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request map[string]any
			if err := json.Unmarshal(validRequestBody(), &request); err != nil {
				t.Fatal(err)
			}
			request["schema_version"] = test.schemaVersion
			event := request["events"].([]any)[0].(map[string]any)
			if test.tag != nil {
				event["tag"] = test.tag
			}
			if test.extra != nil || test.extraSet {
				event["extra"] = test.extra
			}
			body, _ := json.Marshal(request)
			events, err := decodeAndValidateBatch(body, 20, receivedAt)
			if (err == nil) != test.wantAccepted {
				t.Fatalf("decodeAndValidateBatch error = %v, accepted = %t", err, test.wantAccepted)
			}
			if err == nil && (events[0].SchemaVersion != test.schemaVersion || events[0].Tag != test.wantTag || string(events[0].Extra) != test.wantExtra) {
				t.Fatalf("event = %#v", events[0])
			}
		})
	}
}

func TestDecodeAndValidateBatchRejectsInvalidV2Metadata(t *testing.T) {
	tooDeep := any("leaf")
	for range maxExtraDepth + 1 {
		tooDeep = map[string]any{"level": tooDeep}
	}
	for _, test := range []struct {
		name  string
		tag   any
		extra any
	}{
		{name: "too long tag", tag: strings.Repeat("a", maxTagBytes+1)},
		{name: "too large extra", extra: strings.Repeat("a", maxExtraBytes+1)},
		{name: "too deep extra", extra: tooDeep},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request map[string]any
			_ = json.Unmarshal(validRequestBody(), &request)
			request["schema_version"] = 2
			event := request["events"].([]any)[0].(map[string]any)
			if test.tag != nil {
				event["tag"] = test.tag
			}
			if test.extra != nil {
				event["extra"] = test.extra
			}
			body, _ := json.Marshal(request)
			if _, err := decodeAndValidateBatch(body, 20, time.Now()); err == nil {
				t.Fatal("invalid metadata was accepted")
			}
		})
	}
}

func TestDecodeAndValidateBatchRejectsUnknownAndProhibitedFields(t *testing.T) {
	base := validRequestBody()
	var request map[string]any
	if err := json.Unmarshal(base, &request); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unknown top level",
			mutate: func(value map[string]any) {
				value["extra"] = "value"
			},
		},
		{
			name: "prohibited event field",
			mutate: func(value map[string]any) {
				value["events"].([]any)[0].(map[string]any)["sql"] = "select secret"
			},
		},
		{
			name: "nested prohibited field",
			mutate: func(value map[string]any) {
				value["extra"] = map[string]any{"password": "secret"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var copyValue map[string]any
			encoded, _ := json.Marshal(request)
			_ = json.Unmarshal(encoded, &copyValue)
			test.mutate(copyValue)
			body, _ := json.Marshal(copyValue)
			if _, err := decodeAndValidateBatch(body, 20, time.Now()); err == nil {
				t.Fatal("decodeAndValidateBatch accepted invalid body")
			}
		})
	}
}

func TestDecodeAndValidateBatchRejectsMissingRequiredNumbers(t *testing.T) {
	for _, field := range []string{"exit_code", "duration_ms"} {
		t.Run(field, func(t *testing.T) {
			var request map[string]any
			_ = json.Unmarshal(validRequestBody(), &request)
			delete(request["events"].([]any)[0].(map[string]any), field)
			body, _ := json.Marshal(request)
			if _, err := decodeAndValidateBatch(body, 20, time.Now()); err == nil {
				t.Fatalf("missing %s was accepted", field)
			}
		})
	}
}

func TestDecodeAndValidateBatchRejectsInvalidEnumsAndLimits(t *testing.T) {
	tests := []struct {
		field string
		value any
	}{
		{"event_name", "custom.event"},
		{"command_path", "rm -rf"},
		{"command_path", "ti db list-db-clusters select secret"},
		{"cloud_provider", "gcp"},
		{"region_code", "us-east-1"},
		{"install_source", "curl"},
		{"profile_source", "production"},
		{"os", "my-secret-host"},
		{"arch", "custom-cpu"},
		{"cli_version", "version with spaces"},
		{"exit_code", 256},
		{"duration_ms", 86_400_001},
	}
	for _, test := range tests {
		t.Run(test.field, func(t *testing.T) {
			var request map[string]any
			_ = json.Unmarshal(validRequestBody(), &request)
			request["events"].([]any)[0].(map[string]any)[test.field] = test.value
			body, _ := json.Marshal(request)
			if _, err := decodeAndValidateBatch(body, 20, time.Now()); err == nil {
				t.Fatalf("%s=%v was accepted", test.field, test.value)
			}
		})
	}
}

func TestDecodeAndValidateBatchRejectsTooManyEventsAndTrailingJSON(t *testing.T) {
	var request map[string]any
	_ = json.Unmarshal(validRequestBody(), &request)
	event := request["events"].([]any)[0]
	request["events"] = []any{event, event}
	body, _ := json.Marshal(request)
	if _, err := decodeAndValidateBatch(body, 1, time.Now()); err == nil {
		t.Fatal("too many events were accepted")
	}

	body = append(validRequestBody(), []byte(` {}`)...)
	if _, err := decodeAndValidateBatch(body, 20, time.Now()); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}

func TestRejectedBodyDoesNotNeedToExposeValues(t *testing.T) {
	body := bytes.Replace(validRequestBody(), []byte(`"events":[`), []byte(`"sql":"highly-sensitive","events":[`), 1)
	_, err := decodeAndValidateBatch(body, 20, time.Now())
	if err == nil {
		t.Fatal("prohibited field was accepted")
	}
	if strings.Contains(err.Error(), "highly-sensitive") {
		t.Fatalf("error exposed rejected value: %v", err)
	}
}
