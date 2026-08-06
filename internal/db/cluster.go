package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/tdc/internal/api"
	"github.com/tidbcloud/tdc/internal/api/endpoints"
	apistarter "github.com/tidbcloud/tdc/internal/api/starter"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/db/validate"
	"github.com/tidbcloud/tdc/internal/dryrun"
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

type ListClustersOptions struct {
	Profile   *config.Profile
	PageSize  int32
	PageToken string
	Filter    string
	OrderBy   string
	Skip      int32
}

type CreateClusterOptions struct {
	Profile                      *config.Profile
	DisplayName                  string
	ClusterType                  string
	ProjectID                    string
	ProjectIDExplicit            bool
	MonthlySpendingLimitUSDCents int32
	WaitUntilActive              bool
}

type DescribeClusterOptions struct {
	Profile   *config.Profile
	ClusterID string
	View      string
}

type UpdateClusterOptions struct {
	Profile                      *config.Profile
	ClusterID                    string
	DisplayName                  string
	MonthlySpendingLimitUSDCents int32
}

type DeleteClusterOptions struct {
	Profile          *config.Profile
	ClusterID        string
	WaitUntilDeleted bool
}

type ListClustersResult struct {
	Clusters      []apistarter.Cluster `json:"clusters"`
	NextPageToken string               `json:"next_page_token,omitempty"`
	TotalSize     int64                `json:"total_size,omitempty"`
}

type ClusterResult struct {
	apistarter.Cluster
}

func (s Service) ListClusters(ctx context.Context, opts ListClustersOptions) (ListClustersResult, error) {
	if err := validateListOptions(opts); err != nil {
		return ListClustersResult{}, err
	}
	regionScope := newClusterRegionScope(opts.Profile)
	client, err := s.starterClient(opts.Profile, authz.StarterClusterRead, "list Starter DB clusters")
	if err != nil {
		return ListClustersResult{}, err
	}
	response, err := client.ListClusters(ctx, apistarter.ListClustersOptions{
		PageSize:  opts.PageSize,
		PageToken: opts.PageToken,
		Filter:    regionScope.apiFilter(opts.Filter),
		OrderBy:   opts.OrderBy,
		Skip:      opts.Skip,
	})
	if err != nil {
		return ListClustersResult{}, err
	}
	return ListClustersResult{
		Clusters:      filterClustersByRegion(filterStarterClusters(response.Clusters), regionScope),
		NextPageToken: response.NextPageToken,
	}, nil
}

func (s Service) CreateCluster(ctx context.Context, opts CreateClusterOptions) (ClusterResult, error) {
	request, err := s.createRequest(opts)
	if err != nil {
		return ClusterResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterClusterCreate, "create Starter DB cluster")
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
	client, err := s.starterClient(opts.Profile, authz.StarterClusterRead, "describe Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{View: opts.View})
	if err != nil {
		return ClusterResult{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
		return ClusterResult{}, err
	}
	return ClusterResult{Cluster: cluster}, nil
}

func (s Service) UpdateCluster(ctx context.Context, opts UpdateClusterOptions) (ClusterResult, error) {
	clusterID, request, err := s.updateRequest(opts)
	if err != nil {
		return ClusterResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterClusterUpdate, "update Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{})
	if err != nil {
		return ClusterResult{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
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
	client, err := s.starterClient(opts.Profile, authz.StarterClusterDelete, "delete Starter DB cluster")
	if err != nil {
		return ClusterResult{}, err
	}
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{})
	if err != nil {
		return ClusterResult{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
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
		{Name: "permission_requirement", Status: "passed", Message: string(authz.StarterClusterCreate)},
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
	if request.ProjectID != "" {
		body["labels"] = map[string]string{
			apistarter.ProjectLabelKey: request.ProjectID,
		}
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
				fmt.Sprintf("DB cluster %q was created but tdc could not read its state while waiting for ACTIVE; the cluster was not deleted; inspect it with `tdc db describe-db-cluster --db-cluster-id %s`", cluster.ID, cluster.ID),
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
				fmt.Sprintf("DB cluster %q was created but entered state %q before becoming ACTIVE; the cluster was not deleted; inspect it with `tdc db describe-db-cluster --db-cluster-id %s`", cluster.ID, current.State, cluster.ID),
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
			fmt.Sprintf("DB cluster %q was created but did not become ACTIVE within %s; the cluster was not deleted; inspect it with `tdc db describe-db-cluster --db-cluster-id %s`", clusterID, timeout, clusterID),
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
		dryrun.Check{Name: "permission_requirement", Status: "passed", Message: string(authz.StarterClusterUpdate)},
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
		{Name: "permission_requirement", Status: "passed", Message: string(authz.StarterClusterDelete)},
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
				fmt.Sprintf("DB cluster %q deletion was accepted but tdc could not confirm completion; deletion may still be in progress", cluster.ID),
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
	projectID, err := resolveCreateProjectID(opts)
	if err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.OptionalNonNegative("--monthly-spending-limit-usd-cents", opts.MonthlySpendingLimitUSDCents); err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return apistarter.CreateClusterRequest{}, endpoints.Endpoint{}, err
	}
	return apistarter.CreateClusterRequest{
		DisplayName:   opts.DisplayName,
		RegionName:    endpoint.RegionName,
		ProjectID:     projectID,
		SpendingLimit: spendingLimit(opts.MonthlySpendingLimitUSDCents),
	}, endpoint, nil
}

func resolveCreateProjectID(opts CreateClusterOptions) (string, error) {
	projectID := strings.TrimSpace(opts.ProjectID)
	if opts.ProjectIDExplicit && projectID == "" {
		return "", apperr.New("db.empty_project_id", "usage", 2, "--project-id cannot be empty")
	}
	if projectID != "" {
		return projectID, nil
	}
	if opts.Profile != nil {
		projectID = strings.TrimSpace(opts.Profile.ProjectID)
	}
	if projectID != "" {
		return projectID, nil
	}
	return "", nil
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
	if err := validate.OptionalNonNegative("--monthly-spending-limit-usd-cents", opts.MonthlySpendingLimitUSDCents); err != nil {
		return "", apistarter.UpdateClusterRequest{}, endpoints.Endpoint{}, err
	}

	request := apistarter.UpdateClusterRequest{}
	if opts.DisplayName != "" {
		request.DisplayName = &opts.DisplayName
		request.UpdateMask = append(request.UpdateMask, "displayName")
	}
	if opts.MonthlySpendingLimitUSDCents != monthlySpendingLimitUnset {
		request.SpendingLimit = spendingLimit(opts.MonthlySpendingLimitUSDCents)
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
		UserAgent:   "tdc db cluster",
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

func validateListOptions(opts ListClustersOptions) error {
	if err := validateProfile(opts.Profile); err != nil {
		return err
	}
	if err := validate.NonNegative("--page-size", opts.PageSize); err != nil {
		return err
	}
	return validate.NonNegative("--skip", opts.Skip)
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

func (r ListClustersResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tDISPLAY_NAME\tREGION\tSTATE\tPLAN\tCREATED")
	for _, cluster := range r.Clusters {
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			cluster.ID,
			cluster.DisplayName,
			cluster.Region.Name,
			cluster.State,
			clusterPlanDisplay(cluster),
			cluster.CreateTime,
		)
	}
	if r.NextPageToken != "" {
		_, _ = fmt.Fprintf(writer, "next_page_token\t%s\t\t\t\t\n", r.NextPageToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r ClusterResult) Human() string {
	lines := []string{
		"ID: " + r.ID,
		"Display name: " + r.DisplayName,
		"Region: " + r.Region.Name,
		"State: " + r.State,
	}
	if plan := clusterPlanDisplay(r.Cluster); plan != "" {
		lines = append(lines, "Plan: "+plan)
	}
	if r.CreateTime != "" {
		lines = append(lines, "Created: "+r.CreateTime)
	}
	return strings.Join(lines, "\n")
}
