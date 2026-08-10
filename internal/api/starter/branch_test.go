package starter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestListBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1/branches" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("pageSize"); got != "2" {
			t.Fatalf("unexpected pageSize %q", got)
		}
		if got := r.URL.Query().Get("pageToken"); got != "token-1" {
			t.Fatalf("unexpected pageToken %q", got)
		}
		_, _ = w.Write([]byte(`{
			"branches": [
				{
					"name": "clusters/cluster-1/branches/branch-1",
					"branchId": "branch-1",
					"displayName": "dev",
					"clusterId": "cluster-1",
					"parentId": "cluster-1",
					"state": "ACTIVE",
					"createTime": "2026-07-02T00:00:00Z"
				}
			],
			"nextPageToken": "token-2",
			"totalSize": 3
		}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListBranches(context.Background(), "cluster-1", ListBranchesOptions{
		PageSize:  2,
		PageToken: "token-1",
	})
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if response.NextPageToken != "token-2" || response.TotalSize != 3 {
		t.Fatalf("unexpected pagination: %#v", response)
	}
	if len(response.Branches) != 1 || response.Branches[0].ID != "branch-1" || response.Branches[0].DisplayName != "dev" {
		t.Fatalf("unexpected branches: %#v", response.Branches)
	}
}

func TestListBranchesDerivesIDFromResourceName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"branches":[{"name":"clusters/cluster-1/branches/branch-from-name","displayName":"dev"}]}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListBranches(context.Background(), "cluster-1", ListBranchesOptions{})
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(response.Branches) != 1 || response.Branches[0].ID != "branch-from-name" {
		t.Fatalf("unexpected branches: %#v", response.Branches)
	}
}

func TestCreateBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta1/clusters/cluster-1/branches" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["displayName"] != "dev" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"CREATING"}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	branch, err := client.CreateBranch(context.Background(), "cluster-1", CreateBranchRequest{
		DisplayName: "dev",
	})
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	if branch.ID != "branch-1" || branch.DisplayName != "dev" || branch.ClusterID != "cluster-1" {
		t.Fatalf("unexpected branch: %#v", branch)
	}
}

func TestGetDeleteBranch(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("view") != "FULL" {
				t.Fatalf("unexpected view %q", r.URL.Query().Get("view"))
			}
			_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"ACTIVE"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"branchId":"branch-1","displayName":"dev","clusterId":"cluster-1","state":"DELETED"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	if _, err := client.GetBranch(context.Background(), "cluster-1", "branch-1", GetBranchOptions{View: "FULL"}); err != nil {
		t.Fatalf("GetBranch failed: %v", err)
	}
	deleted, err := client.DeleteBranch(context.Background(), "cluster-1", "branch-1")
	if err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}
	if deleted.State != "DELETED" {
		t.Fatalf("unexpected deleted state %q", deleted.State)
	}

	want := []string{
		"GET /v1beta1/clusters/cluster-1/branches/branch-1?view=FULL",
		"DELETE /v1beta1/clusters/cluster-1/branches/branch-1",
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Fatalf("request %d: want %q, got %q", i, want[i], requests[i])
		}
	}
}

func TestBranchAuthAndPermissionErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		exitCode   int
	}{
		{name: "unauthenticated", statusCode: http.StatusUnauthorized, exitCode: 3},
		{name: "permission denied", statusCode: http.StatusForbidden, exitCode: 4},
		{name: "not found", statusCode: http.StatusNotFound, exitCode: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"message":"denied"}`))
			}))
			defer server.Close()

			client := New(newTestAPIClient(t, server.URL))
			_, err := client.GetBranch(context.Background(), "cluster-1", "branch-1", GetBranchOptions{})
			if err == nil {
				t.Fatal("expected GetBranch to fail")
			}
			if got := apperr.ExitCodeFor(err); got != tt.exitCode {
				t.Fatalf("expected exit code %d, got %d", tt.exitCode, got)
			}
		})
	}
}
