package starter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

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
			t.Fatalf("unexpected displayName: %#v", body)
		}
		region := body["region"].(map[string]any)
		if region["name"] != "regions/aws-us-east-1" {
			t.Fatalf("unexpected region: %#v", region)
		}
		if _, ok := body["labels"]; ok {
			t.Fatalf("create request must not select a project: %#v", body)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","region":{"name":"regions/aws-us-east-1"}}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).CreateCluster(context.Background(), CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
		ClusterType: "starter",
		Product:     CreateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
	if result.ID != "cluster-1" || result.DisplayName != "demo-cluster" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCreateClusterWaitsUntilActive(t *testing.T) {
	requests := make([]string, 0, 3)
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method)
		switch r.Method {
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"CREATING"}`))
		case http.MethodGet:
			if r.URL.Path != "/v1beta1/clusters/cluster-1" {
				t.Fatalf("unexpected GET path %s", r.URL.Path)
			}
			gets++
			state := "CREATING"
			if gets == 2 {
				state = "ACTIVE"
			}
			_, _ = fmt.Fprintf(w, `{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":%q}`, state)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	service := testService(server.URL)
	service.ClusterWaitTimeout = time.Second
	service.ClusterWaitPollInterval = time.Millisecond
	result, err := service.CreateCluster(context.Background(), CreateClusterOptions{
		Profile:         testProfile(),
		DisplayName:     "demo-cluster",
		ClusterType:     "starter",
		Product:         CreateOptions{MonthlySpendingLimitUSDCents: -1},
		WaitUntilActive: true,
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
	if result.ID != "cluster-1" || result.State != "ACTIVE" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := strings.Join(requests, ","); got != "POST,GET,GET" {
		t.Fatalf("unexpected requests %q", got)
	}
}

func TestCreateClusterWaitReturnsImmediatelyWhenCreateIsActive(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"ACTIVE"}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).CreateCluster(context.Background(), CreateClusterOptions{
		Profile:         testProfile(),
		DisplayName:     "demo-cluster",
		ClusterType:     "starter",
		Product:         CreateOptions{MonthlySpendingLimitUSDCents: -1},
		WaitUntilActive: true,
	})
	if err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
	if result.State != "ACTIVE" || requests != 1 {
		t.Fatalf("unexpected result %#v or request count %d", result, requests)
	}
}

func TestCreateClusterWaitErrorsPreserveCreatedCluster(t *testing.T) {
	tests := []struct {
		name        string
		getResponse func(http.ResponseWriter)
		timeout     time.Duration
		wantCode    string
		wantText    string
	}{
		{
			name: "terminal state",
			getResponse: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"DELETED"}`))
			},
			timeout:  time.Second,
			wantCode: "db.cluster_wait_terminal_state",
			wantText: "DELETED",
		},
		{
			name: "read failure",
			getResponse: func(w http.ResponseWriter) {
				http.Error(w, `{"message":"backend unavailable"}`, http.StatusInternalServerError)
			},
			timeout:  time.Second,
			wantCode: "db.cluster_wait_read_failed",
			wantText: "could not read its state",
		},
		{
			name: "timeout",
			getResponse: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"CREATING"}`))
			},
			timeout:  10 * time.Millisecond,
			wantCode: "db.cluster_wait_timeout",
			wantText: "was not deleted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"CREATING"}`))
				case http.MethodGet:
					tt.getResponse(w)
				default:
					t.Fatalf("unexpected method %s", r.Method)
				}
			}))
			defer server.Close()

			service := testService(server.URL)
			service.ClusterWaitTimeout = tt.timeout
			service.ClusterWaitPollInterval = time.Millisecond
			_, err := service.CreateCluster(context.Background(), CreateClusterOptions{
				Profile:         testProfile(),
				DisplayName:     "demo-cluster",
				ClusterType:     "starter",
				Product:         CreateOptions{MonthlySpendingLimitUSDCents: -1},
				WaitUntilActive: true,
			})
			if apperr.CodeFor(err) != tt.wantCode {
				t.Fatalf("error code = %q, want %q: %v", apperr.CodeFor(err), tt.wantCode, err)
			}
			message := apperr.MessageFor(err)
			if !strings.Contains(message, "cluster-1") || !strings.Contains(message, tt.wantText) {
				t.Fatalf("error should preserve cluster identity and context, got %q", message)
			}
		})
	}
}

func TestCreateClusterAllowsServerDefaultProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["labels"]; ok {
			t.Fatalf("request with no resolved project must omit labels: %#v", body)
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
	}))
	defer server.Close()

	if _, err := testService(server.URL).CreateCluster(context.Background(), CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
		ClusterType: "starter",
		Product:     CreateOptions{MonthlySpendingLimitUSDCents: -1},
	}); err != nil {
		t.Fatalf("CreateCluster failed: %v", err)
	}
}

func TestListClusters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageSize") != "1" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("filter"); got != `region.provider="aws" AND region.name="regions/aws-us-east-1" AND state="ACTIVE"` {
			t.Fatalf("unexpected region-scoped filter %q", got)
		}
		_, _ = w.Write([]byte(`{
			"clusters":[{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"ACTIVE","region":{"name":"regions/aws-us-east-1"}}],
			"nextPageToken":"token-2",
			"totalSize":1
		}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).ListClusters(context.Background(), ListClustersOptions{
		Profile:  testProfile(),
		PageSize: 1,
		Filter:   `state="ACTIVE"`,
	})
	if err != nil {
		t.Fatalf("ListClusters failed: %v", err)
	}
	if len(result.Clusters) != 1 || result.Clusters[0].ID != "cluster-1" {
		t.Fatalf("unexpected clusters: %#v", result.Clusters)
	}
	if human := result.Human(); !strings.Contains(human, "demo-cluster") || !strings.Contains(human, "token-2") {
		t.Fatalf("unexpected text output:\n%s", human)
	}
}

func TestDescribeRejectsNonStarterCluster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"essential-cluster","clusterPlan":"ESSENTIAL"}`))
	}))
	defer server.Close()

	_, err := testService(server.URL).DescribeCluster(context.Background(), DescribeClusterOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
	})
	if err == nil {
		t.Fatal("expected non-starter cluster to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}

func TestUpdateCluster(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
		case http.MethodPatch:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["updateMask"] != "displayName,spendingLimit" {
				t.Fatalf("unexpected update mask: %#v", body)
			}
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"renamed-cluster","clusterPlan":"STARTER"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).UpdateCluster(context.Background(), UpdateClusterOptions{
		Profile:     testProfile(),
		ClusterID:   "cluster-1",
		DisplayName: "renamed-cluster",
		Product:     UpdateOptions{MonthlySpendingLimitUSDCents: 1000},
	})
	if err != nil {
		t.Fatalf("UpdateCluster failed: %v", err)
	}
	if result.DisplayName != "renamed-cluster" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Join(requests, ",") != "GET,PATCH" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestDeleteClusterReadsBeforeDelete(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"DELETED"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).DeleteCluster(context.Background(), DeleteClusterOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
	})
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}
	if result.ID != "cluster-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Join(requests, ",") != "GET,DELETE" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestDeleteClusterWaitsUntilDeleted(t *testing.T) {
	requests := make([]string, 0, 4)
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method)
		switch r.Method {
		case http.MethodGet:
			gets++
			state := "ACTIVE"
			if gets == 2 {
				state = "DELETING"
			}
			if gets == 3 {
				state = "DELETED"
			}
			_, _ = fmt.Fprintf(w, `{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":%q}`, state)
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"DELETING"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	service := testService(server.URL)
	service.ClusterWaitTimeout = time.Second
	service.ClusterWaitPollInterval = time.Millisecond
	result, err := service.DeleteCluster(context.Background(), DeleteClusterOptions{
		Profile:          testProfile(),
		ClusterID:        "cluster-1",
		WaitUntilDeleted: true,
	})
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}
	if result.ID != "cluster-1" || result.State != "DELETED" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := strings.Join(requests, ","); got != "GET,DELETE,GET,GET" {
		t.Fatalf("unexpected requests %q", got)
	}
}

func TestDeleteClusterWaitTreatsPostDeleteNotFoundAsDeleted(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			if gets == 1 {
				_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"ACTIVE"}`))
				return
			}
			http.NotFound(w, r)
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":"DELETING"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).DeleteCluster(context.Background(), DeleteClusterOptions{
		Profile:          testProfile(),
		ClusterID:        "cluster-1",
		WaitUntilDeleted: true,
	})
	if err != nil {
		t.Fatalf("DeleteCluster failed: %v", err)
	}
	if result.ID != "cluster-1" || result.State != "DELETED" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDeleteClusterWaitTimeoutPreservesAcceptedDeletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := "ACTIVE"
		if r.Method == http.MethodDelete {
			state = "DELETING"
		}
		_, _ = fmt.Fprintf(w, `{"clusterId":"cluster-1","displayName":"demo-cluster","clusterPlan":"STARTER","state":%q}`, state)
	}))
	defer server.Close()

	service := testService(server.URL)
	service.ClusterWaitTimeout = 10 * time.Millisecond
	service.ClusterWaitPollInterval = time.Millisecond
	_, err := service.DeleteCluster(context.Background(), DeleteClusterOptions{
		Profile:          testProfile(),
		ClusterID:        "cluster-1",
		WaitUntilDeleted: true,
	})
	if apperr.CodeFor(err) != "db.cluster_delete_wait_timeout" || !strings.Contains(apperr.MessageFor(err), "may still be in progress") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDryRunCreateClusterDoesNotSendRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	result, err := testService(server.URL).DryRunCreateCluster(context.Background(), "ti db create-db-cluster", CreateClusterOptions{
		Profile:         testProfile(),
		DisplayName:     "demo-cluster",
		ClusterType:     "starter",
		Product:         CreateOptions{MonthlySpendingLimitUSDCents: -1},
		WaitUntilActive: true,
	})
	if err != nil {
		t.Fatalf("DryRunCreateCluster failed: %v", err)
	}
	if !result.DryRun || result.Request.Method != http.MethodPost {
		t.Fatalf("unexpected dry-run: %#v", result)
	}
	if called {
		t.Fatal("dry-run should not send a request")
	}
	foundWait := false
	for _, check := range result.Checks {
		if check.Name == "post_create_wait" && strings.Contains(check.Message, "12m0s") {
			foundWait = true
		}
	}
	if !foundWait {
		t.Fatalf("dry-run should describe the post-create wait: %#v", result.Checks)
	}
}

func TestDryRunCreateClusterOmitsProjectLabelWhenUnset(t *testing.T) {
	result, err := testService("https://starter.test").DryRunCreateCluster(context.Background(), "ti db create-db-cluster", CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
		ClusterType: "starter",
		Product:     CreateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if err != nil {
		t.Fatalf("DryRunCreateCluster failed: %v", err)
	}
	body, ok := result.Request.Body.(map[string]any)
	if !ok {
		t.Fatalf("unexpected dry-run body type %T", result.Request.Body)
	}
	if _, ok := body["labels"]; ok {
		t.Fatalf("dry-run with no resolved project must omit labels: %#v", body)
	}
}

func TestDryRunDeleteClusterDescribesWait(t *testing.T) {
	result, err := testService("https://starter.test").DryRunDeleteCluster(context.Background(), "ti db delete-db-cluster", DeleteClusterOptions{
		Profile:          testProfile(),
		ClusterID:        "cluster-1",
		WaitUntilDeleted: true,
	})
	if err != nil {
		t.Fatalf("DryRunDeleteCluster failed: %v", err)
	}
	foundWait := false
	foundGuard := false
	for _, check := range result.Checks {
		if check.Name == "post_delete_wait" && strings.Contains(check.Message, "12m0s") {
			foundWait = true
		}
		if check.Name == "starter_cluster_precondition" {
			foundGuard = true
		}
	}
	if !foundWait || !foundGuard {
		t.Fatalf("dry-run should describe the post-delete wait and Starter precondition: %#v", result.Checks)
	}
}

func TestDryRunUpdateClusterDescribesStarterPrecondition(t *testing.T) {
	result, err := testService("https://starter.test").DryRunUpdateCluster(context.Background(), "ti db update-db-cluster", UpdateClusterOptions{
		Profile: testProfile(), ClusterID: "cluster-1", DisplayName: "renamed", Product: UpdateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if err != nil {
		t.Fatalf("DryRunUpdateCluster failed: %v", err)
	}
	for _, check := range result.Checks {
		if check.Name == "starter_cluster_precondition" {
			return
		}
	}
	t.Fatalf("dry-run should describe the Starter precondition: %#v", result.Checks)
}

func TestCreateAcceptsExplicitStarterType(t *testing.T) {
	result, err := Service{}.DryRunCreateCluster(context.Background(), "ti db create-db-cluster", CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
		ClusterType: "starter",
		Product:     CreateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if err != nil {
		t.Fatalf("expected explicit Starter type to pass: %v", err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "cluster_type" && check.Message == "starter" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dry-run checks to use starter, got %#v", result.Checks)
	}
}

func TestCreateRejectsUnsupportedClusterType(t *testing.T) {
	_, err := Service{}.DryRunCreateCluster(context.Background(), "ti db create-db-cluster", CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
		ClusterType: "essential",
	})
	if apperr.CodeFor(err) != "db.unsupported_cluster_type" {
		t.Fatalf("expected unsupported cluster type error, got %v", err)
	}
}

func TestCreateRejectsMissingClusterType(t *testing.T) {
	_, err := Service{}.DryRunCreateCluster(context.Background(), "ti db create-db-cluster", CreateClusterOptions{
		Profile:     testProfile(),
		DisplayName: "demo-cluster",
	})
	if apperr.CodeFor(err) != "db.missing_required_flag" {
		t.Fatalf("expected missing cluster type error, got %v", err)
	}
}

func testService(baseURL string) Service {
	return Service{
		Resolver: endpoints.Resolver{StarterBaseURL: baseURL},
	}
}

func testProfile() *config.Profile {
	return &config.Profile{
		Name:                "test",
		CloudProvider:       "aws",
		RegionCode:          "us-east-1",
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
	}
}
