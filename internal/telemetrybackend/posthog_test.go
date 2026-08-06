package telemetrybackend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostHogSinkUsesBatchEndpointAndDisablesPersonProfiles(t *testing.T) {
	var requestPath string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestPath = request.URL.Path
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sink, err := NewPostHogSink(server.URL, "phc_secret", "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent()
	event.SchemaVersion = eventSchemaVersionV2
	event.Tag = "e2b-preview"
	event.Extra = json.RawMessage(`{"campaign":"launch"}`)
	if err := sink.Write(context.Background(), []Event{event}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if requestPath != "/batch/" {
		t.Fatalf("request path = %q, want /batch/", requestPath)
	}
	if payload["api_key"] != "phc_secret" {
		t.Fatalf("api_key = %#v", payload["api_key"])
	}
	batch := payload["batch"].([]any)
	properties := batch[0].(map[string]any)["properties"].(map[string]any)
	if properties["$process_person_profile"] != false {
		t.Fatalf("$process_person_profile = %#v", properties["$process_person_profile"])
	}
	if properties["tag"] != "e2b-preview" {
		t.Fatalf("tag = %#v", properties["tag"])
	}
	if extra, ok := properties["extra"].(map[string]any); !ok || extra["campaign"] != "launch" {
		t.Fatalf("extra = %#v", properties["extra"])
	}
	for _, prohibited := range []string{"sql", "path", "password", "token", "profile_name", "cluster_id"} {
		if _, exists := properties[prohibited]; exists {
			t.Fatalf("PostHog properties include prohibited field %q", prohibited)
		}
	}
}

func TestPostHogSinkReturnsGenericStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "sensitive upstream body", http.StatusUnauthorized)
	}))
	defer server.Close()
	sink, _ := NewPostHogSink(server.URL, "phc_secret", "test", server.Client())
	err := sink.Write(context.Background(), []Event{testEvent()})
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("Write error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive upstream body") {
		t.Fatalf("Write exposed response body: %v", err)
	}
}
