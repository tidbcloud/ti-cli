package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminTenantClientContracts(t *testing.T) {
	const publicKey = "public-secret"
	const privateKey = "private-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get(tidbCloudPublicKeyHeader) != publicKey || r.Header.Get(tidbCloudPrivateKeyHeader) != privateKey {
			t.Fatalf("credential headers = %q/%q", r.Header.Get(tidbCloudPublicKeyHeader), r.Header.Get(tidbCloudPrivateKeyHeader))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/tenants":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["display_name"] != "agent-workspace" {
				t.Fatalf("create body = %#v", body)
			}
			if _, ok := body["public_key"]; ok {
				t.Fatalf("create body leaked credentials: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenant_id": "tenant-1", "display_name": "agent-workspace", "label": map[string]string{"environment": "production"},
				"api_key": "owner-token", "status": "provisioning", "cloud_provider": "aws", "region": "ap-southeast-1", "future": true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants":
			query := r.URL.Query()
			if query.Get("page") != "2" || query.Get("page_size") != "100" || query.Get("display_name") != "workspace" || query.Get("label") != "environment==production" {
				t.Fatalf("list query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenants": []map[string]any{{"tenant_id": "tenant-1", "display_name": "agent-workspace", "label": nil, "status": "active", "kind": "live"}},
				"page":    2, "page_size": 100, "next_page": 3,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tenants/tenant-1":
			_ = json.NewEncoder(w).Encode(AdminTenant{TenantID: "tenant-1", DisplayName: "agent-workspace", Status: "active", Kind: "live"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/admin/tenants/tenant-1":
			_ = json.NewEncoder(w).Encode(DeleteAdminTenantResponse{TenantID: "tenant-1", Status: "deleting"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	creds := TiDBCloudCredentials{PublicKey: publicKey, PrivateKey: privateKey}
	created, err := client.CreateAdminTenant(context.Background(), creds, AdminTenantCreateRequest{DisplayName: "agent-workspace", Labels: map[string]string{"environment": "production"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.TenantID != "tenant-1" || created.APIKey != "owner-token" || created.Labels["environment"] != "production" {
		t.Fatalf("create response = %#v", created)
	}
	listed, err := client.ListAdminTenants(context.Background(), creds, ListAdminTenantsOptions{Page: 2, PageSize: 100, DisplayName: "workspace", Label: &AdminTenantLabelFilter{Key: "environment", Value: "production"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tenants) != 1 || listed.NextPage != 3 || listed.Tenants[0].Labels == nil {
		t.Fatalf("list response = %#v", listed)
	}
	described, err := client.GetAdminTenant(context.Background(), creds, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if described.TenantID != "tenant-1" || described.Labels == nil {
		t.Fatalf("get response = %#v", described)
	}
	deleted, err := client.DeleteAdminTenant(context.Background(), creds, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != "deleting" || requests != 4 {
		t.Fatalf("delete response = %#v, requests = %d", deleted, requests)
	}
}

func TestAdminTenantCreateOmitsEmptyMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Fatalf("body = %#v, want empty metadata object", body)
		}
		_ = json.NewEncoder(w).Encode(AdminTenantCreateResponse{TenantID: "tenant-1", DisplayName: "tenant-1", APIKey: "owner-token", Status: "active"})
	}))
	defer server.Close()

	response, err := testClient(t, server.URL).CreateAdminTenant(context.Background(), TiDBCloudCredentials{PublicKey: "public", PrivateKey: "private"}, AdminTenantCreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Labels == nil {
		t.Fatal("labels must be normalized to an empty object")
	}
}
