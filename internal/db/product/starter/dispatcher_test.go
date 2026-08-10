package starter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	rootdb "github.com/tidbcloud/ti-cli/internal/db"
)

func TestDispatcherReusesStarterDiscoverySnapshot(t *testing.T) {
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		getCalls++
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"STARTER","region":{"name":"regions/aws-us-east-1"}}`))
	}))
	defer server.Close()

	service := testService(server.URL)
	dispatcher, err := rootdb.NewDispatcher([]rootdb.ClusterResolver{service}, service)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DescribeCluster(context.Background(), rootdb.DescribeClusterOptions{
		Profile: testProfile(), ClusterID: "cluster-1", View: "BASIC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "cluster-1" || getCalls != 1 {
		t.Fatalf("result=%#v discovery GET calls=%d, want one", result, getCalls)
	}
}

func TestDispatcherNormalizesPrefixedClusterID(t *testing.T) {
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		getCalls++
		_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"STARTER","region":{"name":"regions/aws-us-east-1"}}`))
	}))
	defer server.Close()

	service := testService(server.URL)
	dispatcher, err := rootdb.NewDispatcher([]rootdb.ClusterResolver{service}, service)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.DescribeCluster(context.Background(), rootdb.DescribeClusterOptions{
		Profile: testProfile(), ClusterID: "clusters/cluster-1", View: "BASIC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "cluster-1" || getCalls != 1 {
		t.Fatalf("result=%#v discovery GET calls=%d, want one", result, getCalls)
	}
}
