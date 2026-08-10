package db

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type listProvider struct {
	pages int
	load  func(page int) ListClusterPageResult
}

func (*listProvider) ClusterType() ClusterType { return ClusterTypeStarter }

func (*listProvider) Permission(operation Operation) (authz.Permission, error) {
	if operation != OperationClusterList {
		return "", MissingPermission(ClusterTypeStarter, operation)
	}
	return authz.StarterClusterRead, nil
}

func (p *listProvider) ListClusterPage(_ context.Context, opts ListClusterPageOptions) (ListClusterPageResult, error) {
	page := 0
	if opts.UpstreamPageToken != "" {
		parsed, err := strconv.Atoi(opts.UpstreamPageToken)
		if err != nil {
			return ListClusterPageResult{}, err
		}
		page = parsed
	}
	p.pages++
	return p.load(page), nil
}

type inertResolver struct{}

func (inertResolver) ResolveCluster(context.Context, ResolveClusterRequest) (ResolvedCluster, error) {
	return ResolvedCluster{}, ErrResolverNotApplicable
}

func TestListClustersFillsResultAcrossUpstreamPagesWithoutLoadingAll(t *testing.T) {
	provider := &listProvider{load: func(page int) ListClusterPageResult {
		result := ListClusterPageResult{}
		if page%100 == 99 {
			result.Clusters = []apistarter.Cluster{{ID: fmt.Sprintf("starter-%02d", page/100)}}
		}
		if page < 999 {
			result.NextPageToken = strconv.Itoa(page + 1)
		}
		return result
	}}
	dispatcher, err := NewDispatcher([]ClusterResolver{inertResolver{}}, provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.ListClusters(context.Background(), ListClustersOptions{
		Profile:     testDispatcherProfile(),
		ClusterType: "starter",
		PageSize:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clusters) != 10 || provider.pages != 1000 || result.NextPageToken != "" {
		t.Fatalf("unexpected sparse 100k-resource result: clusters=%d pages=%d next=%q", len(result.Clusters), provider.pages, result.NextPageToken)
	}
}

func TestListClustersCursorReplaysPartialPage(t *testing.T) {
	provider := &listProvider{load: func(page int) ListClusterPageResult {
		if page != 0 {
			t.Fatalf("unexpected upstream page %d", page)
		}
		return ListClusterPageResult{Clusters: []apistarter.Cluster{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	}}
	dispatcher, err := NewDispatcher([]ClusterResolver{inertResolver{}}, provider)
	if err != nil {
		t.Fatal(err)
	}
	first, err := dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: testDispatcherProfile(), ClusterType: "starter", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Clusters) != 2 || first.NextPageToken == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: testDispatcherProfile(), ClusterType: "starter", PageSize: 2, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Clusters) != 1 || second.Clusters[0].ID != "c" || second.NextPageToken != "" || provider.pages != 2 {
		t.Fatalf("unexpected replay page: %#v, calls=%d", second, provider.pages)
	}
}

func TestListClustersRejectsChangedReplayPage(t *testing.T) {
	changed := false
	provider := &listProvider{load: func(_ int) ListClusterPageResult {
		if changed {
			return ListClusterPageResult{Clusters: []apistarter.Cluster{{ID: "a"}, {ID: "changed"}, {ID: "c"}}}
		}
		return ListClusterPageResult{Clusters: []apistarter.Cluster{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	}}
	dispatcher, err := NewDispatcher([]ClusterResolver{inertResolver{}}, provider)
	if err != nil {
		t.Fatal(err)
	}
	first, err := dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: testDispatcherProfile(), ClusterType: "starter", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	changed = true
	_, err = dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: testDispatcherProfile(), ClusterType: "starter", PageSize: 2, PageToken: first.NextPageToken})
	if apperr.CodeFor(err) != "db.page_token_stale" {
		t.Fatalf("expected stale cursor, got %v", err)
	}
}

func TestListClustersCursorBindsRegionAndProfile(t *testing.T) {
	provider := &listProvider{load: func(_ int) ListClusterPageResult {
		return ListClusterPageResult{Clusters: []apistarter.Cluster{{ID: "a"}, {ID: "b"}}}
	}}
	dispatcher, err := NewDispatcher([]ClusterResolver{inertResolver{}}, provider)
	if err != nil {
		t.Fatal(err)
	}
	first, err := dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: testDispatcherProfile(), ClusterType: "starter", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	other := testDispatcherProfile()
	other.PlacementRegionCode = "aws-us-west-2"
	_, err = dispatcher.ListClusters(context.Background(), ListClustersOptions{Profile: other, ClusterType: "starter", PageSize: 1, PageToken: first.NextPageToken})
	if apperr.CodeFor(err) != "db.page_token_context_mismatch" {
		t.Fatalf("expected cursor context mismatch, got %v", err)
	}
}

func testDispatcherProfile() *config.Profile {
	return &config.Profile{Name: "default", PlacementRegionCode: "aws-us-east-1", CloudProvider: "aws", RegionCode: "us-east-1"}
}
