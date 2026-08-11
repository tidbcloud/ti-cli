package iam

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/authz"
)

func TestSQLUserLifecycleRequests(t *testing.T) {
	requests := make([]string, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/v1beta1/clusters/cluster-1/sqlUsers" {
				if got := r.URL.Query().Get("pageSize"); got != "2" {
					t.Fatalf("unexpected pageSize %q", got)
				}
				_, _ = w.Write([]byte(`{
					"sqlUsers":[{"userName":"prefix.ti_rw","authMethod":"mysql_native_password","builtinRole":"role_readwrite"}],
					"nextPageToken":"token-2"
				}`))
				return
			}
			_, _ = w.Write([]byte(`{"userName":"prefix.ti_rw","authMethod":"mysql_native_password","builtinRole":"role_readwrite"}`))
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body["userName"] != "ti_rw" || body["builtinRole"] != "role_readwrite" || body["authMethod"] != "mysql_native_password" || body["password"] != "secret" || body["autoPrefix"] != true {
				t.Fatalf("unexpected create body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"userName":"prefix.ti_rw","authMethod":"mysql_native_password","builtinRole":"role_readwrite"}`))
		case http.MethodPatch:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			if body["password"] != "rotated" {
				t.Fatalf("unexpected update body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"userName":"prefix.ti_rw","authMethod":"mysql_native_password","builtinRole":"role_readwrite"}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	list, err := client.ListSQLUsers(context.Background(), "cluster-1", ListSQLUsersOptions{PageSize: 2})
	if err != nil {
		t.Fatalf("ListSQLUsers failed: %v", err)
	}
	if list.NextPageToken != "token-2" || len(list.SQLUsers) != 1 || list.SQLUsers[0].UserName != "prefix.ti_rw" {
		t.Fatalf("unexpected list response: %#v", list)
	}
	created, err := client.CreateSQLUser(context.Background(), "cluster-1", CreateSQLUserRequest{
		UserName:    "ti_rw",
		Password:    "secret",
		AuthMethod:  "mysql_native_password",
		AutoPrefix:  true,
		BuiltinRole: "role_readwrite",
	})
	if err != nil {
		t.Fatalf("CreateSQLUser failed: %v", err)
	}
	if created.BuiltinRole != "role_readwrite" {
		t.Fatalf("unexpected created user: %#v", created)
	}
	if _, err := client.GetSQLUser(context.Background(), "cluster-1", "prefix.ti_rw"); err != nil {
		t.Fatalf("GetSQLUser failed: %v", err)
	}
	if _, err := client.UpdateSQLUser(context.Background(), "cluster-1", "prefix.ti_rw", UpdateSQLUserRequest{Password: "rotated"}); err != nil {
		t.Fatalf("UpdateSQLUser failed: %v", err)
	}
	if err := client.DeleteSQLUser(context.Background(), "cluster-1", "prefix.ti_rw"); err != nil {
		t.Fatalf("DeleteSQLUser failed: %v", err)
	}

	want := []string{
		"GET /v1beta1/clusters/cluster-1/sqlUsers?pageSize=2",
		"POST /v1beta1/clusters/cluster-1/sqlUsers",
		"GET /v1beta1/clusters/cluster-1/sqlUsers/prefix.ti_rw",
		"PATCH /v1beta1/clusters/cluster-1/sqlUsers/prefix.ti_rw",
		"DELETE /v1beta1/clusters/cluster-1/sqlUsers/prefix.ti_rw",
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Fatalf("request %d: want %q, got %q", i, want[i], requests[i])
		}
	}
}

func newTestAPIClient(t *testing.T, baseURL string) *api.Client {
	t.Helper()
	client, err := api.New(api.Options{
		Endpoint: endpoints.Endpoint{
			Service: endpoints.ServiceIAM,
			BaseURL: baseURL,
		},
		ProfileName: "test",
		Permission:  authz.StarterSQLUserRead,
		HTTPClient:  http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("new API client: %v", err)
	}
	return client
}
