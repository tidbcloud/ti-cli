package starter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
)

func TestListClusters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/clusters" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("pageSize"); got != "2" {
			t.Fatalf("unexpected pageSize %q", got)
		}
		if got := r.URL.Query().Get("pageToken"); got != "token-1" {
			t.Fatalf("unexpected pageToken %q", got)
		}
		if got := r.URL.Query().Get("filter"); got != `region.name="regions/aws-us-east-1"` {
			t.Fatalf("unexpected filter %q", got)
		}
		_, _ = w.Write([]byte(`{
			"clusters": [
				{
					"name": "clusters/cluster-1",
					"clusterId": "cluster-1",
					"displayName": "demo-cluster",
					"region": {"name": "regions/aws-us-east-1", "regionId": "aws-us-east-1", "cloudProvider": "aws"},
					"state": "ACTIVE",
					"clusterPlan": "STARTER",
					"servicePlan": "Starter",
					"createTime": "2026-07-02T00:00:00Z"
				}
			],
			"nextPageToken": "token-2",
			"totalSize": 3
		}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListClusters(context.Background(), ListClustersOptions{
		PageSize:  2,
		PageToken: "token-1",
		Filter:    `region.name="regions/aws-us-east-1"`,
	})
	if err != nil {
		t.Fatalf("ListClusters failed: %v", err)
	}
	if response.NextPageToken != "token-2" || response.TotalSize != 3 {
		t.Fatalf("unexpected pagination: %#v", response)
	}
	if len(response.Clusters) != 1 || response.Clusters[0].ID != "cluster-1" || response.Clusters[0].DisplayName != "demo-cluster" {
		t.Fatalf("unexpected clusters: %#v", response.Clusters)
	}
	if response.Clusters[0].Region.Name != "regions/aws-us-east-1" {
		t.Fatalf("unexpected region: %#v", response.Clusters[0].Region)
	}
}

func TestListClustersDerivesIDFromResourceName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusters":[{"name":"clusters/cluster-from-name","displayName":"demo-cluster"}]}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListClusters(context.Background(), ListClustersOptions{})
	if err != nil {
		t.Fatalf("ListClusters failed: %v", err)
	}
	if len(response.Clusters) != 1 || response.Clusters[0].ID != "cluster-from-name" {
		t.Fatalf("unexpected clusters: %#v", response.Clusters)
	}
}

func TestCreateCluster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta1/clusters" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["displayName"] != "demo-cluster" {
			t.Fatalf("unexpected body: %#v", body)
		}
		labels := body["labels"].(map[string]any)
		if labels[ProjectLabelKey] != "project-1" {
			t.Fatalf("unexpected labels: %#v", labels)
		}
		region := body["region"].(map[string]any)
		if region["name"] != "regions/aws-us-east-1" {
			t.Fatalf("unexpected region: %#v", region)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	cluster, err := client.CreateCluster(context.Background(), CreateClusterRequest{
		DisplayName: "demo-cluster",
		RegionName:  "regions/aws-us-east-1",
		ProjectID:   "project-1",
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
	if cluster.ID != "cluster-1" || cluster.DisplayName != "demo-cluster" {
		t.Fatalf("unexpected cluster: %#v", cluster)
	}
}

func TestCreateClusterOmitsProjectLabelWhenUnset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, ok := body["labels"]; ok {
			t.Fatalf("request with no project ID must omit labels: %#v", body)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	if _, err := client.CreateCluster(context.Background(), CreateClusterRequest{
		DisplayName: "demo-cluster",
		RegionName:  "regions/aws-us-east-1",
	}); err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
}

func TestGetUpdateDeleteCluster(t *testing.T) {
	requests := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("view") != "FULL" {
				t.Fatalf("unexpected view %q", r.URL.Query().Get("view"))
			}
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
		case http.MethodPatch:
			var body struct {
				UpdateMask string `json:"updateMask"`
				Cluster    struct {
					DisplayName   string         `json:"displayName"`
					SpendingLimit *SpendingLimit `json:"spendingLimit"`
				} `json:"cluster"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.UpdateMask != "displayName,spendingLimit" || body.Cluster.DisplayName != "renamed" || body.Cluster.SpendingLimit.Monthly != 1000 {
				t.Fatalf("unexpected update body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"renamed","clusterPlan":"STARTER"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"renamed","state":"DELETING","clusterPlan":"STARTER"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	if _, err := client.GetCluster(context.Background(), "cluster-1", GetClusterOptions{View: "FULL"}); err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	name := "renamed"
	limit := &SpendingLimit{Monthly: 1000}
	if _, err := client.UpdateCluster(context.Background(), "cluster-1", UpdateClusterRequest{
		DisplayName:   &name,
		SpendingLimit: limit,
		UpdateMask:    []string{"displayName", "spendingLimit"},
	}); err != nil {
		t.Fatalf("UpdateCluster failed: %v", err)
	}
	deleted, err := client.DeleteCluster(context.Background(), "cluster-1")
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}
	if deleted.State != "DELETING" {
		t.Fatalf("unexpected deleted state %q", deleted.State)
	}

	want := []string{
		"GET /v1beta1/clusters/cluster-1?view=FULL",
		"PATCH /v1beta1/clusters/cluster-1",
		"DELETE /v1beta1/clusters/cluster-1",
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Fatalf("request %d: want %q, got %q", i, want[i], requests[i])
		}
	}
}

func TestClusterAuthAndPermissionErrors(t *testing.T) {
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
			_, err := client.GetCluster(context.Background(), "cluster-1", GetClusterOptions{})
			if err == nil {
				t.Fatal("expected GetCluster to fail")
			}
			if got := apperr.ExitCodeFor(err); got != tt.exitCode {
				t.Fatalf("expected exit code %d, got %d", tt.exitCode, got)
			}
		})
	}
}

func newTestAPIClient(t *testing.T, baseURL string) *api.Client {
	t.Helper()
	client, err := api.New(api.Options{
		Endpoint: endpoints.Endpoint{
			Service: endpoints.ServiceStarter,
			BaseURL: baseURL,
		},
		ProfileName: "test",
		Permission:  authz.StarterClusterRead,
		HTTPClient:  http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("new API client: %v", err)
	}
	return client
}
