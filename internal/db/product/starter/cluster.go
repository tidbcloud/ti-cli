package starter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	rootdb "github.com/tidbcloud/ti-cli/internal/db"
	"github.com/tidbcloud/ti-cli/internal/db/validate"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
)

const (
	monthlySpendingLimitUnset      int32 = -1
	defaultClusterWaitTimeout            = 12 * time.Minute
	defaultClusterWaitPollInterval       = 2 * time.Second
	defaultBranchWaitTimeout             = 5 * time.Minute
	defaultBranchWaitPollInterval        = 2 * time.Second
)

type Service struct {
	Resolver                endpoints.Resolver
	HTTPClient              *http.Client
	Transport               http.RoundTripper
	Timeout                 time.Duration
	ClusterWaitTimeout      time.Duration
	ClusterWaitPollInterval time.Duration
	BranchWaitTimeout       time.Duration
	BranchWaitPollInterval  time.Duration
	Debug                   bool
	DebugWriter             io.Writer
	HomeDir                 string
	SQLHTTPBaseURL          string
	MySQLDriverName         string
}

// ListClusters is retained as a package-level compatibility helper for focused
// Starter tests. Product-aware callers use db.Dispatcher.ListClusters.
func (s Service) ListClusters(ctx context.Context, opts ListClustersOptions) (ListClustersResult, error) {
	page, err := s.ListClusterPage(ctx, rootdb.ListClusterPageOptions{
		Profile:             opts.Profile,
		UpstreamPageSize:    opts.PageSize,
		UpstreamPageToken:   opts.PageToken,
		Filter:              opts.Filter,
		OrderBy:             opts.OrderBy,
		OperationPermission: authz.StarterClusterRead,
	})
	if err != nil {
		return ListClustersResult{}, err
	}
	return ListClustersResult{Clusters: page.Clusters, NextPageToken: page.NextPageToken}, nil
}

func (s Service) ListClusterPage(ctx context.Context, opts rootdb.ListClusterPageOptions) (rootdb.ListClusterPageResult, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return rootdb.ListClusterPageResult{}, err
	}
	regionScope := newClusterRegionScope(opts.Profile)
	client, err := s.starterClient(opts.Profile, opts.OperationPermission, "list Starter DB clusters")
	if err != nil {
		return rootdb.ListClusterPageResult{}, err
	}
	response, err := client.ListClusters(ctx, apistarter.ListClustersOptions{
		PageSize:  opts.UpstreamPageSize,
		PageToken: opts.UpstreamPageToken,
		Filter:    regionScope.apiFilter(opts.Filter),
		OrderBy:   opts.OrderBy,
	})
	if err != nil {
		return rootdb.ListClusterPageResult{}, err
	}
	return rootdb.ListClusterPageResult{
		Clusters:      filterClustersByRegion(filterStarterClusters(response.Clusters), regionScope),
		NextPageToken: response.NextPageToken,
	}, nil
}

func (s Service) CreateCluster(ctx context.Context, opts CreateClusterOptions) (ClusterResult, error) {
	request, err := s.createRequest(opts)
	if err != nil {
		return ClusterResult{}, err
	}
	client, err := s.starterClient(opts.Profile, operationPermission(opts.Dispatch, authz.StarterClusterCreate), "create Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := client.CreateCluster(ctx, request)
	if err != nil {
		return ClusterResult{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
		return ClusterResult{}, createdClusterPlanError(cluster, err)
	}
	if opts.WaitUntilActive {
		cluster, err = s.waitUntilClusterActive(ctx, client, cluster)
		if err != nil {
			return ClusterResult{}, err
		}
	}
	return ClusterResult{Cluster: cluster}, nil
}

func (s Service) DescribeCluster(ctx context.Context, opts DescribeClusterOptions) (ClusterResult, error) {
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return ClusterResult{}, err
	}
	if err := validate.View(opts.View); err != nil {
		return ClusterResult{}, err
	}
	cluster, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, opts.View, authz.StarterClusterRead, "describe Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	return ClusterResult{Cluster: cluster}, nil
}

func (s Service) UpdateCluster(ctx context.Context, opts UpdateClusterOptions) (ClusterResult, error) {
	clusterID, request, err := s.updateRequest(opts)
	if err != nil {
		return ClusterResult{}, err
	}
	client, err := s.starterClient(opts.Profile, operationPermission(opts.Dispatch, authz.StarterClusterUpdate), "update Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, "BASIC", authz.StarterClusterUpdate, "update Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err = client.UpdateCluster(ctx, clusterID, request)
	if err != nil {
		return ClusterResult{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
		return ClusterResult{}, updatedClusterPlanError(cluster, err)
	}
	return ClusterResult{Cluster: cluster}, nil
}

func (s Service) DeleteCluster(ctx context.Context, opts DeleteClusterOptions) (ClusterResult, error) {
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return ClusterResult{}, err
	}
	client, err := s.starterClient(opts.Profile, operationPermission(opts.Dispatch, authz.StarterClusterDelete), "delete Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, "BASIC", authz.StarterClusterDelete, "delete Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err = client.DeleteCluster(ctx, clusterID)
	if err != nil {
		return ClusterResult{}, err
	}
	if opts.WaitUntilDeleted {
		cluster, err = s.waitUntilClusterDeleted(ctx, client, cluster)
		if err != nil {
			return ClusterResult{}, err
		}
	}
	return ClusterResult{Cluster: cluster}, nil
}

func (s Service) DryRunCreateCluster(ctx context.Context, commandPath string, opts CreateClusterOptions) (dryrun.Result, error) {
	request, endpoint, err := s.createRequestAndEndpoint(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
		{Name: "operation_permission", Status: "passed", Message: string(operationPermission(opts.Dispatch, authz.StarterClusterCreate))},
		{Name: "cluster_type", Status: "passed", Message: validate.ClusterTypeStarter},
	}
	if opts.WaitUntilActive {
		checks = append(checks, dryrun.Check{
			Name:    "post_create_wait",
			Status:  "passed",
			Message: fmt.Sprintf("normal execution waits up to %s for state ACTIVE", s.clusterWaitTimeout()),
		})
	}
	body := map[string]any{
		"displayName": request.DisplayName,
		"region": map[string]string{
			"name": request.RegionName,
		},
		"spendingLimit": request.SpendingLimit,
	}
	return dryrun.New(
		commandPath,
		"create_db_cluster",
		dryrun.RequestSummary{
			Method: "POST",
			Path:   "/v1beta1/clusters",
			Body:   body,
		},
		checks...,
	), nil
}

func (s Service) waitUntilClusterActive(ctx context.Context, client *apistarter.Client, cluster apistarter.Cluster) (apistarter.Cluster, error) {
	if cluster.State == "ACTIVE" {
		return cluster, nil
	}
	if strings.TrimSpace(cluster.ID) == "" {
		return apistarter.Cluster{}, apperr.New(
			"db.cluster_wait_missing_id",
			"api",
			1,
			"Starter cluster creation was accepted but the response did not include a cluster ID; list DB clusters before retrying",
		)
	}

	timeout := s.clusterWaitTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(s.clusterWaitPollInterval())
	defer ticker.Stop()

	for {
		current, err := client.GetCluster(waitCtx, cluster.ID, apistarter.GetClusterOptions{})
		if err != nil {
			if waitErr := clusterWaitContextError(ctx, waitCtx, cluster.ID, timeout); waitErr != nil {
				return apistarter.Cluster{}, waitErr
			}
			return apistarter.Cluster{}, apperr.Wrap(
				"db.cluster_wait_read_failed",
				"api",
				1,
				fmt.Sprintf("DB cluster %q was created but ti could not read its state while waiting for ACTIVE; the cluster was not deleted; inspect it with `ti db describe-db-cluster --db-cluster-id %s`", cluster.ID, cluster.ID),
				err,
			)
		}
		if err := ensureStarterCluster(current); err != nil {
			return apistarter.Cluster{}, createdClusterPlanError(cluster, err)
		}
		switch current.State {
		case "ACTIVE":
			return current, nil
		case "DELETING", "DELETED", "INACTIVE":
			return apistarter.Cluster{}, apperr.New(
				"db.cluster_wait_terminal_state",
				"api",
				1,
				fmt.Sprintf("DB cluster %q was created but entered state %q before becoming ACTIVE; the cluster was not deleted; inspect it with `ti db describe-db-cluster --db-cluster-id %s`", cluster.ID, current.State, cluster.ID),
			)
		}

		select {
		case <-waitCtx.Done():
			return apistarter.Cluster{}, clusterWaitContextError(ctx, waitCtx, cluster.ID, timeout)
		case <-ticker.C:
		}
	}
}

func clusterWaitContextError(parent, waitCtx context.Context, clusterID string, timeout time.Duration) error {
	if parent.Err() != nil {
		return apperr.Wrap(
			"db.cluster_wait_canceled",
			"runtime",
			1,
			fmt.Sprintf("waiting for DB cluster %q to become ACTIVE was canceled; the cluster was not deleted", clusterID),
			parent.Err(),
		)
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return apperr.New(
			"db.cluster_wait_timeout",
			"api",
			1,
			fmt.Sprintf("DB cluster %q was created but did not become ACTIVE within %s; the cluster was not deleted; inspect it with `ti db describe-db-cluster --db-cluster-id %s`", clusterID, timeout, clusterID),
		)
	}
	return nil
}

func (s Service) clusterWaitTimeout() time.Duration {
	if s.ClusterWaitTimeout > 0 {
		return s.ClusterWaitTimeout
	}
	return defaultClusterWaitTimeout
}

func (s Service) clusterWaitPollInterval() time.Duration {
	if s.ClusterWaitPollInterval > 0 {
		return s.ClusterWaitPollInterval
	}
	return defaultClusterWaitPollInterval
}

func (s Service) DryRunUpdateCluster(ctx context.Context, commandPath string, opts UpdateClusterOptions) (dryrun.Result, error) {
	clusterID, request, endpoint, err := s.updateRequestAndEndpoint(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	body := map[string]any{
		"updateMask": strings.Join(request.UpdateMask, ","),
		"cluster":    map[string]any{},
	}
	clusterBody := body["cluster"].(map[string]any)
	if request.DisplayName != nil {
		clusterBody["displayName"] = *request.DisplayName
	}
	if request.SpendingLimit != nil {
		clusterBody["spendingLimit"] = request.SpendingLimit
	}
	return dryrun.New(
		commandPath,
		"update_db_cluster",
		dryrun.RequestSummary{
			Method: "PATCH",
			Path:   "/v1beta1/clusters/" + clusterID,
			Body:   body,
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
		dryrun.Check{Name: "cluster_discovery_permission", Status: "passed", Message: string(opts.Dispatch.DiscoveryPermission)},
		dryrun.Check{Name: "operation_permission", Status: "passed", Message: string(opts.Dispatch.OperationPermission)},
		dryrun.Check{Name: "starter_cluster_precondition", Status: "passed", Message: "normal execution verifies the cluster is Starter before updating"},
	), nil
}

func (s Service) DryRunDeleteCluster(ctx context.Context, commandPath string, opts DeleteClusterOptions) (dryrun.Result, error) {
	clusterID, endpoint, err := s.deleteRequestAndEndpoint(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
		{Name: "cluster_discovery_permission", Status: "passed", Message: string(opts.Dispatch.DiscoveryPermission)},
		{Name: "operation_permission", Status: "passed", Message: string(opts.Dispatch.OperationPermission)},
		{Name: "starter_cluster_precondition", Status: "passed", Message: "normal execution verifies the cluster is Starter before deleting"},
	}
	if opts.WaitUntilDeleted {
		checks = append(checks, dryrun.Check{
			Name:    "post_delete_wait",
			Status:  "passed",
			Message: fmt.Sprintf("normal execution waits up to %s for state DELETED or for the cluster to become inaccessible after deletion", s.clusterWaitTimeout()),
		})
	}
	return dryrun.New(
		commandPath,
		"delete_db_cluster",
		dryrun.RequestSummary{
			Method:      "DELETE",
			Path:        "/v1beta1/clusters/" + clusterID,
			Description: "normal execution first reads the cluster and verifies it is a Starter cluster before deleting",
		},
		checks...,
	), nil
}

func (s Service) waitUntilClusterDeleted(ctx context.Context, client *apistarter.Client, cluster apistarter.Cluster) (apistarter.Cluster, error) {
	if cluster.State == "DELETED" {
		return cluster, nil
	}
	if strings.TrimSpace(cluster.ID) == "" {
		return apistarter.Cluster{}, apperr.New(
			"db.cluster_delete_wait_missing_id",
			"api",
			1,
			"Starter cluster deletion was accepted but the response did not include a cluster ID; list DB clusters before retrying",
		)
	}

	timeout := s.clusterWaitTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(s.clusterWaitPollInterval())
	defer ticker.Stop()

	for {
		current, err := client.GetCluster(waitCtx, cluster.ID, apistarter.GetClusterOptions{})
		if err != nil {
			if waitErr := clusterDeleteWaitContextError(ctx, waitCtx, cluster.ID, timeout); waitErr != nil {
				return apistarter.Cluster{}, waitErr
			}
			if isDeletedClusterReadError(err) {
				cluster.State = "DELETED"
				return cluster, nil
			}
			return apistarter.Cluster{}, apperr.Wrap(
				"db.cluster_delete_wait_read_failed",
				"api",
				1,
				fmt.Sprintf("DB cluster %q deletion was accepted but ti could not confirm completion; deletion may still be in progress", cluster.ID),
				err,
			)
		}
		if err := ensureStarterCluster(current); err != nil {
			return apistarter.Cluster{}, deletingClusterPlanError(cluster, err)
		}
		if current.State == "DELETED" {
			return current, nil
		}

		select {
		case <-waitCtx.Done():
			return apistarter.Cluster{}, clusterDeleteWaitContextError(ctx, waitCtx, cluster.ID, timeout)
		case <-ticker.C:
		}
	}
}

func isDeletedClusterReadError(err error) bool {
	switch apperr.CodeFor(err) {
	case "api.not_found", "authz.permission_denied":
		return true
	default:
		return false
	}
}

func clusterDeleteWaitContextError(parent, waitCtx context.Context, clusterID string, timeout time.Duration) error {
	if parent.Err() != nil {
		return apperr.Wrap(
			"db.cluster_delete_wait_canceled",
			"runtime",
			1,
			fmt.Sprintf("waiting for DB cluster %q deletion was canceled; deletion may still be in progress", clusterID),
			parent.Err(),
		)
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return apperr.New(
			"db.cluster_delete_wait_timeout",
			"api",
			1,
			fmt.Sprintf("DB cluster %q did not become DELETED within %s; deletion may still be in progress", clusterID, timeout),
		)
	}
	return nil
}

func (s Service) createRequest(opts CreateClusterOptions) (apistarter.CreateClusterRequest, error) {
	request, _, err := s.createRequestAndEndpoint(opts)
	return request, err
}

func (s Service) createRequestAndEndpoint(opts CreateClusterOptions) (apistarter.CreateClusterRequest, endpoints.Endpoint, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.ClusterType(opts.ClusterType); err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.ClusterName(opts.DisplayName); err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	product, err := createOptions(opts.Product)
	if err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.OptionalNonNegative("--monthly-spending-limit-usd-cents", product.MonthlySpendingLimitUSDCents); err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	return apistarter.CreateClusterRequest{
		DisplayName:   opts.DisplayName,
		RegionName:    endpoint.RegionName,
		SpendingLimit: spendingLimit(product.MonthlySpendingLimitUSDCents),
	}, endpoint, nil
}

func (s Service) updateRequest(opts UpdateClusterOptions) (string, apistarter.UpdateClusterRequest, error) {
	clusterID, request, _, err := s.updateRequestAndEndpoint(opts)
	return clusterID, request, err
}

func (s Service) updateRequestAndEndpoint(opts UpdateClusterOptions) (string, apistarter.UpdateClusterRequest, endpoints.Endpoint, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.OptionalClusterName(opts.DisplayName); err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}
	product, err := updateOptions(opts.Product)
	if err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.OptionalNonNegative("--monthly-spending-limit-usd-cents", product.MonthlySpendingLimitUSDCents); err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}

	request := apistarter.UpdateClusterRequest{}
	if opts.DisplayName != "" {
		request.DisplayName = &opts.DisplayName
		request.UpdateMask = append(request.UpdateMask, "displayName")
	}
	if product.MonthlySpendingLimitUSDCents != monthlySpendingLimitUnset {
		request.SpendingLimit = spendingLimit(product.MonthlySpendingLimitUSDCents)
		request.UpdateMask = append(request.UpdateMask, "spendingLimit")
	}
	if len(request.UpdateMask) == 0 {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, apperr.New(
			"db.update_empty",
			"usage",
			2,
			"update-db-cluster requires at least one update flag, such as --db-cluster-name or --monthly-spending-limit-usd-cents",
		)
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}
	return clusterID, request, endpoint, nil
}

func (s Service) deleteRequestAndEndpoint(opts DeleteClusterOptions) (string, endpoints.Endpoint, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", endpoints.Endpoint{}, err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return "", endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return "", endpoints.Endpoint{}, err
	}
	return clusterID, endpoint, nil
}

func (s Service) starterClient(profile *config.Profile, permission authz.Permission, action string) (*apistarter.Client, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	endpoint, err := s.resolveStarter(profile)
	if err != nil {
		return nil, err
	}
	client, err := api.NewDigestClient(profile, endpoint, permission, api.Options{
		Action:      action,
		HTTPClient:  s.HTTPClient,
		Transport:   s.Transport,
		Timeout:     s.Timeout,
		Debug:       s.Debug,
		DebugWriter: s.DebugWriter,
		UserAgent:   "ti db cluster",
	})
	if err != nil {
		return nil, err
	}
	return apistarter.New(client), nil
}

func (s Service) resolveStarter(profile *config.Profile) (endpoints.Endpoint, error) {
	return s.resolver().ResolveStarter(profile.CloudProvider, profile.RegionCode)
}

func (s Service) resolver() endpoints.Resolver {
	if s.Resolver.IsZero() {
		return endpoints.NewResolver()
	}
	return s.Resolver
}

func validateProfile(profile *config.Profile) error {
	if profile == nil {
		return apperr.New("db.missing_profile", "config", 2, "active profile is required")
	}
	return nil
}

func spendingLimit(cents int32) *apistarter.SpendingLimit {
	if cents == monthlySpendingLimitUnset {
		return nil
	}
	return &apistarter.SpendingLimit{Monthly: cents}
}

func profileName(profile *config.Profile) string {
	if profile == nil || profile.Name == "" {
		return config.DefaultProfile
	}
	return profile.Name
}
