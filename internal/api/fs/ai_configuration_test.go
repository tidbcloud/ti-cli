package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminTenantAIConfigurationClientContracts(t *testing.T) {
	const publicKey = "public-secret"
	const privateKey = "private-secret"
	updatedAt := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get(tidbCloudPublicKeyHeader) != publicKey || r.Header.Get(tidbCloudPrivateKeyHeader) != privateKey {
			t.Fatalf("credential headers = %q/%q", r.Header.Get(tidbCloudPublicKeyHeader), r.Header.Get(tidbCloudPrivateKeyHeader))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants/tenant-1/extract-config/audio":
			_ = json.NewEncoder(w).Encode(AdminTenantExtractConfiguration{Enabled: false, Source: "none"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/admin/tenants/tenant-1/extract-config/audio":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 6 || body["enabled"] != true || body["api_base"] != "https://provider.example/v1" || body["api_key"] != "provider-secret" || body["model"] != "audio-model" || body["protocol"] != "qwen-asr" || body["prompt"] != "transcribe" {
				t.Fatalf("extract update body = %#v", body)
			}
			masked := "pro********"
			base, model, protocol, prompt := "https://provider.example/v1", "audio-model", "qwen-asr", "transcribe"
			_ = json.NewEncoder(w).Encode(AdminTenantExtractConfiguration{Enabled: true, APIBase: &base, APIKey: &masked, Model: &model, Protocol: &protocol, Prompt: &prompt, Source: "custom", UpdatedAt: &updatedAt})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants/tenant-1/embedding-config":
			_ = json.NewEncoder(w).Encode(AdminTenantEmbeddingConfiguration{Enabled: false, Source: "database_auto"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/admin/tenants/tenant-1/embedding-config":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 4 || body["enabled"] != true || body["api_base"] != "https://provider.example/v1" || body["api_key"] != "provider-secret" || body["model"] != "embedding-model" {
				t.Fatalf("embedding update body = %#v", body)
			}
			masked := "pro********"
			base, model := "https://provider.example/v1", "embedding-model"
			_ = json.NewEncoder(w).Encode(AdminTenantEmbeddingConfiguration{Enabled: true, APIBase: &base, APIKey: &masked, Model: &model, Source: "custom", Generation: 3, UpdatedAt: &updatedAt})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	creds := TiDBCloudCredentials{PublicKey: publicKey, PrivateKey: privateKey}
	if result, err := client.GetAdminTenantExtractConfiguration(context.Background(), creds, "tenant-1", ExtractMediaTypeAudio); err != nil || result.Source != "none" {
		t.Fatalf("GetAdminTenantExtractConfiguration() = %#v, %v", result, err)
	}
	enabled := true
	base, key, audioModel, protocol, prompt := "https://provider.example/v1", "provider-secret", "audio-model", "qwen-asr", "transcribe"
	extract, err := client.UpdateAdminTenantExtractConfiguration(context.Background(), creds, "tenant-1", ExtractMediaTypeAudio, UpdateAdminTenantExtractConfigurationRequest{
		Enabled: &enabled, APIBase: &base, APIKey: &key, Model: &audioModel, Protocol: &protocol, Prompt: &prompt,
	})
	if err != nil || extract.APIKey == nil || *extract.APIKey != "pro********" {
		t.Fatalf("UpdateAdminTenantExtractConfiguration() = %#v, %v", extract, err)
	}
	if result, err := client.GetAdminTenantEmbeddingConfiguration(context.Background(), creds, "tenant-1"); err != nil || result.Source != "database_auto" {
		t.Fatalf("GetAdminTenantEmbeddingConfiguration() = %#v, %v", result, err)
	}
	embeddingModel := "embedding-model"
	embedding, err := client.UpdateAdminTenantEmbeddingConfiguration(context.Background(), creds, "tenant-1", UpdateAdminTenantEmbeddingConfigurationRequest{
		Enabled: true, APIBase: &base, APIKey: &key, Model: &embeddingModel,
	})
	if err != nil || embedding.Generation != 3 {
		t.Fatalf("UpdateAdminTenantEmbeddingConfiguration() = %#v, %v", embedding, err)
	}
	if requests != 4 {
		t.Fatalf("requests = %d, want 4", requests)
	}
}

func TestAdminTenantAIConfigurationUpdateDoesNotRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, `{"error":"provider request failed"}`, http.StatusBadGateway)
	}))
	defer server.Close()

	enabled := true
	_, err := testClient(t, server.URL).UpdateAdminTenantExtractConfiguration(context.Background(), TiDBCloudCredentials{PublicKey: "public", PrivateKey: "private"}, "tenant-1", ExtractMediaTypeImage, UpdateAdminTenantExtractConfigurationRequest{Enabled: &enabled})
	if err == nil {
		t.Fatal("update should fail")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want one non-retried PUT", got)
	}
}
