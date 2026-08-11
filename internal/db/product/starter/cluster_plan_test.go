package starter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestEnsureStarterClusterPlanResolution(t *testing.T) {
	tests := []struct {
		name        string
		cluster     apistarter.Cluster
		wantCode    string
		wantMessage string
	}{
		{
			name:    "current service plan",
			cluster: apistarter.Cluster{ID: "cluster-1", ServicePlan: "Starter"},
		},
		{
			name:    "normalized service plan",
			cluster: apistarter.Cluster{ID: "cluster-1", ServicePlan: "  sTaRtEr  "},
		},
		{
			name:    "matching current and legacy plans",
			cluster: apistarter.Cluster{ID: "cluster-1", ServicePlan: "Starter", ClusterPlan: "STARTER"},
		},
		{
			name:    "legacy fallback",
			cluster: apistarter.Cluster{ID: "cluster-1", ClusterPlan: "STARTER"},
		},
		{
			name:        "essential",
			cluster:     apistarter.Cluster{ID: "cluster-1", ServicePlan: "Essential"},
			wantCode:    "db.not_starter_cluster",
			wantMessage: "Essential",
		},
		{
			name:        "premium",
			cluster:     apistarter.Cluster{ID: "cluster-1", ServicePlan: "Premium"},
			wantCode:    "db.not_starter_cluster",
			wantMessage: "Premium",
		},
		{
			name:        "byoc",
			cluster:     apistarter.Cluster{ID: "cluster-1", ServicePlan: "BYOC"},
			wantCode:    "db.not_starter_cluster",
			wantMessage: "BYOC",
		},
		{
			name:        "unknown non-starter value",
			cluster:     apistarter.Cluster{ID: "cluster-1", ServicePlan: "Future"},
			wantCode:    "db.not_starter_cluster",
			wantMessage: "Future",
		},
		{
			name:        "missing plans",
			cluster:     apistarter.Cluster{ID: "cluster-1"},
			wantCode:    "db.cluster_plan_unknown",
			wantMessage: "omitted servicePlan and clusterPlan",
		},
		{
			name:        "conflicting plans",
			cluster:     apistarter.Cluster{ID: "cluster-1", ServicePlan: "Starter", ClusterPlan: "ESSENTIAL"},
			wantCode:    "db.cluster_plan_unknown",
			wantMessage: "conflicts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureStarterCluster(tt.cluster)
			if got := apperr.CodeFor(err); got != tt.wantCode {
				t.Fatalf("error code = %q, want %q: %v", got, tt.wantCode, err)
			}
			if tt.wantMessage != "" && !strings.Contains(apperr.MessageFor(err), tt.wantMessage) {
				t.Fatalf("error message %q does not contain %q", apperr.MessageFor(err), tt.wantMessage)
			}
		})
	}
}

func TestListClustersFiltersNonStarterAndUnverifiableResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"clusters":[
				{"clusterId":"starter-1","displayName":"starter","servicePlan":"Starter","region":{"name":"regions/aws-us-east-1"}},
				{"clusterId":"essential-1","displayName":"essential","servicePlan":"Essential","region":{"name":"regions/aws-us-east-1"}},
				{"clusterId":"unknown-1","displayName":"unknown","region":{"name":"regions/aws-us-east-1"}},
				{"clusterId":"conflict-1","displayName":"conflict","servicePlan":"Starter","clusterPlan":"ESSENTIAL","region":{"name":"regions/aws-us-east-1"}}
			],
			"nextPageToken":"token-2",
			"totalSize":4
		}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).ListClusters(context.Background(), ListClustersOptions{Profile: testProfile()})
	if err != nil {
		t.Fatalf("ListClusters failed: %v", err)
	}
	if len(result.Clusters) != 1 || result.Clusters[0].ID != "starter-1" {
		t.Fatalf("unexpected clusters: %#v", result.Clusters)
	}
	if result.NextPageToken != "token-2" {
		t.Fatalf("next page token = %q", result.NextPageToken)
	}
	if human := result.Human(); !strings.Contains(human, "Starter") || strings.Contains(human, "essential") {
		t.Fatalf("unexpected text output:\n%s", human)
	}
}

func TestListClustersCanReturnEmptyFilteredPageWithNextToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusters":[{"clusterId":"essential-1","servicePlan":"Essential","region":{"name":"regions/aws-us-east-1"}}],"nextPageToken":"token-2","totalSize":1}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).ListClusters(context.Background(), ListClustersOptions{Profile: testProfile()})
	if err != nil {
		t.Fatalf("ListClusters failed: %v", err)
	}
	if len(result.Clusters) != 0 || result.NextPageToken != "token-2" {
		t.Fatalf("unexpected filtered result: %#v", result)
	}
}

func TestClusterMutationsRejectNonStarterBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		run  func(Service) error
	}{
		{name: "update", run: func(service Service) error {
			_, err := service.UpdateCluster(context.Background(), UpdateClusterOptions{
				Profile: testProfile(), ClusterID: "cluster-1", DisplayName: "renamed", Product: UpdateOptions{MonthlySpendingLimitUSDCents: -1},
			})
			return err
		}},
		{name: "delete", run: func(service Service) error {
			_, err := service.DeleteCluster(context.Background(), DeleteClusterOptions{Profile: testProfile(), ClusterID: "cluster-1"})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
					t.Fatalf("mutation was sent before rejection: %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Essential"}`))
			}))
			defer server.Close()

			err := tt.run(testService(server.URL))
			if apperr.CodeFor(err) != "db.not_starter_cluster" {
				t.Fatalf("unexpected error: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want only the cluster preflight", requests)
			}
		})
	}
}

func TestCreatePlanFailureRetainsAcceptedClusterIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"clusterId":"cluster-created","servicePlan":"Essential","state":"CREATING"}`))
	}))
	defer server.Close()

	_, err := testService(server.URL).CreateCluster(context.Background(), CreateClusterOptions{
		Profile: testProfile(), DisplayName: "demo", ClusterType: "starter", Product: CreateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if apperr.CodeFor(err) != "db.not_starter_cluster" {
		t.Fatalf("unexpected error: %v", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "cluster-created") || !strings.Contains(message, "CREATING") || !strings.Contains(message, "was retained") {
		t.Fatalf("error did not retain accepted cluster identity: %q", message)
	}
}

func TestUpdateResponsePlanFailureReportsPossiblyAppliedUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
		case http.MethodPatch:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Essential"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	_, err := testService(server.URL).UpdateCluster(context.Background(), UpdateClusterOptions{
		Profile: testProfile(), ClusterID: "cluster-1", DisplayName: "renamed", Product: UpdateOptions{MonthlySpendingLimitUSDCents: -1},
	})
	if apperr.CodeFor(err) != "db.not_starter_cluster" {
		t.Fatalf("unexpected error: %v", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "cluster-1") || !strings.Contains(message, "may have been applied") {
		t.Fatalf("error did not preserve accepted update context: %q", message)
	}
}

func TestCreateWaitPlanFailureRetainsAcceptedClusterIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"clusterId":"cluster-created","servicePlan":"Starter","state":"CREATING"}`))
			return
		}
		_, _ = w.Write([]byte(`{"clusterId":"cluster-created","servicePlan":"Essential","state":"CREATING"}`))
	}))
	defer server.Close()

	service := testService(server.URL)
	service.ClusterWaitTimeout = time.Second
	service.ClusterWaitPollInterval = time.Millisecond
	_, err := service.CreateCluster(context.Background(), CreateClusterOptions{
		Profile: testProfile(), DisplayName: "demo", ClusterType: "starter", Product: CreateOptions{MonthlySpendingLimitUSDCents: -1}, WaitUntilActive: true,
	})
	if apperr.CodeFor(err) != "db.not_starter_cluster" {
		t.Fatalf("unexpected error: %v", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "cluster-created") || !strings.Contains(message, "CREATING") || !strings.Contains(message, "was retained") {
		t.Fatalf("error did not retain accepted cluster identity: %q", message)
	}
}

func TestDeleteWaitPlanFailureReportsAcceptedDeletion(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			plan := "Starter"
			if gets > 1 {
				plan = "Essential"
			}
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"` + plan + `","state":"DELETING"}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","state":"DELETING"}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	service := testService(server.URL)
	service.ClusterWaitTimeout = time.Second
	service.ClusterWaitPollInterval = time.Millisecond
	_, err := service.DeleteCluster(context.Background(), DeleteClusterOptions{Profile: testProfile(), ClusterID: "cluster-1", WaitUntilDeleted: true})
	if apperr.CodeFor(err) != "db.not_starter_cluster" {
		t.Fatalf("unexpected error: %v", err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "cluster-1") || !strings.Contains(message, "may still be in progress") {
		t.Fatalf("error did not preserve accepted deletion context: %q", message)
	}
}
