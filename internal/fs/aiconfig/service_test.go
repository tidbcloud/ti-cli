package aiconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

func TestExtractConfigurationValidation(t *testing.T) {
	falseValue := false
	trueValue := true
	base := "https://provider.example/v1"
	model := "model"
	qwen := ProtocolQwenASR
	tooLargePrompt := strings.Repeat("x", maxPromptBytes+1)
	tests := []struct {
		name string
		opts UpdateExtractOptions
		code string
	}{
		{name: "no changes", opts: UpdateExtractOptions{MediaType: MediaTypeImage}, code: "fs.extract_configuration_no_changes"},
		{name: "text media", opts: UpdateExtractOptions{MediaType: "text", Enabled: &falseValue}, code: "fs.invalid_media_type"},
		{name: "disable with prompt", opts: UpdateExtractOptions{MediaType: MediaTypeImage, Enabled: &falseValue, Prompt: stringPointer("clear")}, code: "fs.invalid_extract_configuration"},
		{name: "partial trio", opts: UpdateExtractOptions{MediaType: MediaTypeImage, Enabled: &trueValue, ProviderAPIBase: &base}, code: "fs.incomplete_ai_provider"},
		{name: "non https", opts: UpdateExtractOptions{MediaType: MediaTypeImage, ProviderAPIBase: stringPointer("http://provider.example"), ProviderModel: &model, ProviderAPIKey: "secret", ProviderAPIKeySupplied: true}, code: "fs.invalid_provider_api_base"},
		{name: "qwen image", opts: UpdateExtractOptions{MediaType: MediaTypeImage, ProviderProtocol: &qwen}, code: "fs.invalid_provider_protocol"},
		{name: "qwen audio needs trio", opts: UpdateExtractOptions{MediaType: MediaTypeAudio, ProviderProtocol: &qwen}, code: "fs.incomplete_ai_provider"},
		{name: "prompt too large", opts: UpdateExtractOptions{MediaType: MediaTypeVideo, Prompt: &tooLargePrompt}, code: "fs.invalid_extract_prompt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateExtractUpdate(test.opts)
			if err == nil || apperr.CodeFor(err) != test.code {
				t.Fatalf("error = %v (%s), want %s", err, apperr.CodeFor(err), test.code)
			}
		})
	}

	request, err := validateExtractUpdate(UpdateExtractOptions{
		MediaType: MediaTypeAudio, Enabled: &trueValue, ProviderAPIBase: stringPointer("  " + base + "  "), ProviderModel: stringPointer("  " + model + "  "),
		ProviderAPIKey: "  secret  ", ProviderAPIKeySupplied: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Protocol == nil || *request.Protocol != ProtocolOpenAI || request.APIBase == nil || *request.APIBase != base || request.Model == nil || *request.Model != model || request.APIKey == nil || *request.APIKey != "secret" {
		t.Fatalf("request = %#v", request)
	}
	prompt := ""
	request, err = validateExtractUpdate(UpdateExtractOptions{MediaType: MediaTypeImage, Prompt: &prompt})
	if err != nil || request.Prompt == nil || *request.Prompt != "" {
		t.Fatalf("empty prompt request = %#v, %v", request, err)
	}
}

func TestEmbeddingConfigurationValidation(t *testing.T) {
	base := "https://provider.example/v1"
	model := "embedding-model"
	if _, err := validateEmbeddingUpdate(UpdateEmbeddingOptions{Enabled: true, ProviderAPIBase: &base, ProviderModel: &model}); apperr.CodeFor(err) != "fs.incomplete_ai_provider" {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := validateEmbeddingUpdate(UpdateEmbeddingOptions{Enabled: false, ProviderModel: &model}); apperr.CodeFor(err) != "fs.invalid_embedding_configuration" {
		t.Fatalf("disable provider error = %v", err)
	}
	request, err := validateEmbeddingUpdate(UpdateEmbeddingOptions{Enabled: true, ProviderAPIBase: &base, ProviderModel: &model, ProviderAPIKey: "secret", ProviderAPIKeySupplied: true})
	if err != nil || request.APIKey == nil || *request.APIKey != "secret" {
		t.Fatalf("request = %#v, %v", request, err)
	}
}

func TestAIConfigurationServiceOutputDryRunAndRedaction(t *testing.T) {
	const secret = "provider-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assertCredentialHeaders(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants/tenant-1/extract-config/image":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "source": "none"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/admin/tenants/tenant-1/extract-config/image":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["api_key"] != secret {
				t.Fatalf("body = %#v", body)
			}
			base, key, model, protocol := "https://provider.example/v1", "pro********", "vision-model", ProtocolOpenAI
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "api_base": base, "api_key": key, "model": model, "protocol": protocol, "source": "custom"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	service := testService(server.URL)
	profile := testProfile()
	described, err := service.DescribeExtract(context.Background(), DescribeExtractOptions{Profile: profile, FileSystemID: "tenant-1", MediaType: MediaTypeImage})
	if err != nil || described.Source != "none" || !strings.Contains(described.Human(), "Provider API key: none") {
		t.Fatalf("describe = %#v, %v, text=%q", described, err, described.Human())
	}
	enabled := true
	base, model := "https://provider.example/v1", "vision-model"
	opts := UpdateExtractOptions{
		Profile: profile, FileSystemID: "tenant-1", MediaType: MediaTypeImage, Enabled: &enabled,
		ProviderAPIBase: &base, ProviderModel: &model, ProviderAPIKey: secret, ProviderAPIKeySupplied: true,
	}
	updated, err := service.UpdateExtract(context.Background(), opts)
	if err != nil || updated.APIKey == nil || *updated.APIKey != "pro********" {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	beforeDryRun := requests
	dryRun, err := service.DryRunUpdateExtract("ti fs update-file-system-extract-configuration", opts)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dryRun)
	if err != nil {
		t.Fatal(err)
	}
	if requests != beforeDryRun || strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("dry-run requests=%d body=%s", requests-beforeDryRun, encoded)
	}
	for _, check := range []string{"config_and_credentials", "endpoint_selection", "permission_requirement", "provider_validation", "remote_mutation"} {
		if !strings.Contains(string(encoded), check) {
			t.Fatalf("dry-run body does not contain %q: %s", check, encoded)
		}
	}
}

func TestAIConfigurationErrorMappings(t *testing.T) {
	t.Run("database auto conflict", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"database auto embedding"}`, http.StatusConflict)
		}))
		defer server.Close()
		_, err := testService(server.URL).UpdateEmbedding(context.Background(), UpdateEmbeddingOptions{Profile: testProfile(), FileSystemID: "tenant-1", Enabled: false})
		if apperr.CodeFor(err) != "fs.embedding_configuration_not_applicable" {
			t.Fatalf("error = %v (%s)", err, apperr.CodeFor(err))
		}
	})

	t.Run("provider secret is redacted", func(t *testing.T) {
		const secret = "provider-secret"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"provider validation failed for provider-secret"}`))
		}))
		defer server.Close()
		enabled := true
		base, model := "https://provider.example/v1", "model"
		_, err := testService(server.URL).UpdateEmbedding(context.Background(), UpdateEmbeddingOptions{
			Profile: testProfile(), FileSystemID: "tenant-1", Enabled: enabled, ProviderAPIBase: &base,
			ProviderModel: &model, ProviderAPIKey: secret, ProviderAPIKeySupplied: true,
		})
		if err == nil || apperr.CodeFor(err) != "fs.ai_provider_validation_failed" || strings.Contains(err.Error(), secret) {
			t.Fatalf("error = %v (%s)", err, apperr.CodeFor(err))
		}
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing source", body: `{"enabled":false}`},
		{name: "unmasked key", body: `{"enabled":true,"source":"custom","api_key":"provider-secret"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			_, err := testService(server.URL).DescribeEmbedding(context.Background(), DescribeEmbeddingOptions{Profile: testProfile(), FileSystemID: "tenant-1"})
			if apperr.CodeFor(err) != "fs.api_contract" {
				t.Fatalf("error = %v (%s)", err, apperr.CodeFor(err))
			}
		})
	}
}

func TestAIConfigurationResponseSources(t *testing.T) {
	masked := "key********"
	for _, source := range []string{"custom", "default", "none", "database_auto", "future_source"} {
		if err := validateResponse(source, &masked); err != nil {
			t.Fatalf("source %q rejected: %v", source, err)
		}
	}
}

func testService(baseURL string) Service {
	return Service{Resolver: endpoints.Resolver{FSManifest: &endpoints.FSRegionManifest{Regions: []endpoints.FSRegionManifestEntry{{
		RegionCode: "aws-us-east-1", Mode: endpoints.DefaultFSMode, ServerURL: baseURL, CloudProvider: "aws", TiDBRegion: "us-east-1",
	}}}}}
}

func testProfile() *config.Profile {
	return &config.Profile{Name: "test", PlacementRegionCode: "aws-us-east-1", CloudProvider: "aws", RegionCode: "us-east-1", TiDBCloudPublicKey: "public", TiDBCloudPrivateKey: "private"}
}

func assertCredentialHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("X-TiDBCloud-Public-Key") != "public" || request.Header.Get("X-TiDBCloud-Private-Key") != "private" {
		t.Fatalf("credential headers = %q/%q", request.Header.Get("X-TiDBCloud-Public-Key"), request.Header.Get("X-TiDBCloud-Private-Key"))
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected bearer header %q", request.Header.Get("Authorization"))
	}
}

func stringPointer(value string) *string {
	return &value
}
