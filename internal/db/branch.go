package db

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/db/validate"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
)

type ListBranchesOptions struct {
	Profile   *config.Profile
	ClusterID string
	PageSize  int32
	PageToken string
}

type CreateBranchOptions struct {
	Profile         *config.Profile
	ClusterID       string
	DisplayName     string
	WaitUntilActive bool
}

type DescribeBranchOptions struct {
	Profile   *config.Profile
	ClusterID string
	BranchID  string
	View      string
}

type DeleteBranchOptions struct {
	Profile   *config.Profile
	ClusterID string
	BranchID  string
}

type ListBranchesResult struct {
	Branches      []apistarter.Branch `json:"branches"`
	NextPageToken string              `json:"next_page_token,omitempty"`
	TotalSize     int64               `json:"total_size,omitempty"`
}

type BranchResult struct {
	apistarter.Branch
}

func (s Service) ListBranches(ctx context.Context, opts ListBranchesOptions) (ListBranchesResult, error) {
	clusterID, err := validateListBranchesOptions(opts)
	if err != nil {
		return ListBranchesResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterBranchRead, "list Starter DB cluster branches")
	if err != nil {
		return ListBranchesResult{}, err
	}
	if err := ensureStarterClusterByID(ctx, client, clusterID); err != nil {
		return ListBranchesResult{}, err
	}
	response, err := client.ListBranches(ctx, clusterID, apistarter.ListBranchesOptions{
		PageSize:  opts.PageSize,
		PageToken: opts.PageToken,
	})
	if err != nil {
		return ListBranchesResult{}, err
	}
	return ListBranchesResult{
		Branches:      response.Branches,
		NextPageToken: response.NextPageToken,
		TotalSize:     response.TotalSize,
	}, nil
}

func (s Service) CreateBranch(ctx context.Context, opts CreateBranchOptions) (BranchResult, error) {
	clusterID, request, err := s.createBranchRequest(opts)
	if err != nil {
		return BranchResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterBranchCreate, "create Starter DB cluster branch")
	if err != nil {
		return BranchResult{}, err
	}
	if err := ensureStarterClusterByID(ctx, client, clusterID); err != nil {
		return BranchResult{}, err
	}
	branch, err := client.CreateBranch(ctx, clusterID, request)
	if err != nil {
		return BranchResult{}, err
	}
	if opts.WaitUntilActive {
		branch, err = s.waitUntilBranchActive(ctx, client, clusterID, branch)
		if err != nil {
			return BranchResult{}, err
		}
	}
	return BranchResult{Branch: branch}, nil
}

func (s Service) DescribeBranch(ctx context.Context, opts DescribeBranchOptions) (BranchResult, error) {
	clusterID, branchID, err := validateBranchIdentity(opts.ClusterID, opts.BranchID)
	if err != nil {
		return BranchResult{}, err
	}
	if err := validate.View(opts.View); err != nil {
		return BranchResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterBranchRead, "describe Starter DB cluster branch")
	if err != nil {
		return BranchResult{}, err
	}
	if err := ensureStarterClusterByID(ctx, client, clusterID); err != nil {
		return BranchResult{}, err
	}
	branch, err := client.GetBranch(ctx, clusterID, branchID, apistarter.GetBranchOptions{View: opts.View})
	if err != nil {
		return BranchResult{}, err
	}
	return BranchResult{Branch: branch}, nil
}

func (s Service) DeleteBranch(ctx context.Context, opts DeleteBranchOptions) (BranchResult, error) {
	clusterID, branchID, err := validateBranchIdentity(opts.ClusterID, opts.BranchID)
	if err != nil {
		return BranchResult{}, err
	}
	client, err := s.starterClient(opts.Profile, authz.StarterBranchDelete, "delete Starter DB cluster branch")
	if err != nil {
		return BranchResult{}, err
	}
	if err := ensureStarterClusterByID(ctx, client, clusterID); err != nil {
		return BranchResult{}, err
	}
	branch, err := client.GetBranch(ctx, clusterID, branchID, apistarter.GetBranchOptions{})
	if err != nil {
		return BranchResult{}, err
	}
	branch, err = client.DeleteBranch(ctx, clusterID, branchID)
	if err != nil {
		return BranchResult{}, err
	}
	return BranchResult{Branch: branch}, nil
}

func (s Service) DryRunCreateBranch(ctx context.Context, commandPath string, opts CreateBranchOptions) (dryrun.Result, error) {
	clusterID, request, endpoint, err := s.createBranchRequestAndEndpoint(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.StarterBranchCreate)},
		{Name: "cluster_id", Status: "passed", Message: clusterID},
		{Name: "starter_cluster_precondition", Status: "passed", Message: "normal execution verifies the parent cluster is Starter before creating the branch"},
	}
	if opts.WaitUntilActive {
		checks = append(checks, dryrun.Check{
			Name:    "post_create_wait",
			Status:  "passed",
			Message: fmt.Sprintf("normal execution waits up to %s for state ACTIVE", s.branchWaitTimeout()),
		})
	}
	return dryrun.New(
		commandPath,
		"create_db_cluster_branch",
		dryrun.RequestSummary{
			Method: "POST",
			Path:   "/v1beta1/clusters/" + clusterID + "/branches",
			Body: map[string]any{
				"displayName": request.DisplayName,
			},
		},
		checks...,
	), nil
}

func (s Service) waitUntilBranchActive(ctx context.Context, client *apistarter.Client, clusterID string, branch apistarter.Branch) (apistarter.Branch, error) {
	if branch.State == "ACTIVE" {
		return branch, nil
	}
	if strings.TrimSpace(branch.ID) == "" {
		return apistarter.Branch{}, apperr.New(
			"db.branch_wait_missing_id",
			"api",
			1,
			fmt.Sprintf("Starter branch creation in cluster %q was accepted but the response did not include a branch ID; list DB cluster branches before retrying", clusterID),
		)
	}

	timeout := s.branchWaitTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(s.branchWaitPollInterval())
	defer ticker.Stop()

	for {
		current, err := client.GetBranch(waitCtx, clusterID, branch.ID, apistarter.GetBranchOptions{})
		if err != nil {
			if waitErr := branchWaitContextError(ctx, waitCtx, clusterID, branch.ID, timeout); waitErr != nil {
				return apistarter.Branch{}, waitErr
			}
			return apistarter.Branch{}, apperr.Wrap(
				"db.branch_wait_read_failed",
				"api",
				1,
				fmt.Sprintf("DB branch %q was created in cluster %q but ti could not read its state while waiting for ACTIVE; the branch was not deleted", branch.ID, clusterID),
				err,
			)
		}
		switch current.State {
		case "ACTIVE":
			return current, nil
		case "DELETED":
			return apistarter.Branch{}, apperr.New(
				"db.branch_wait_terminal_state",
				"api",
				1,
				fmt.Sprintf("DB branch %q in cluster %q was created but entered state DELETED before becoming ACTIVE; the branch was not recreated", branch.ID, clusterID),
			)
		}

		select {
		case <-waitCtx.Done():
			return apistarter.Branch{}, branchWaitContextError(ctx, waitCtx, clusterID, branch.ID, timeout)
		case <-ticker.C:
		}
	}
}

func branchWaitContextError(parent, waitCtx context.Context, clusterID, branchID string, timeout time.Duration) error {
	if parent.Err() != nil {
		return apperr.Wrap(
			"db.branch_wait_canceled",
			"runtime",
			1,
			fmt.Sprintf("waiting for DB branch %q in cluster %q to become ACTIVE was canceled; the branch was not deleted", branchID, clusterID),
			parent.Err(),
		)
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return apperr.New(
			"db.branch_wait_timeout",
			"api",
			1,
			fmt.Sprintf("DB branch %q in cluster %q did not become ACTIVE within %s; the branch was not deleted", branchID, clusterID, timeout),
		)
	}
	return nil
}

func (s Service) branchWaitTimeout() time.Duration {
	if s.BranchWaitTimeout > 0 {
		return s.BranchWaitTimeout
	}
	return defaultBranchWaitTimeout
}

func (s Service) branchWaitPollInterval() time.Duration {
	if s.BranchWaitPollInterval > 0 {
		return s.BranchWaitPollInterval
	}
	return defaultBranchWaitPollInterval
}

func (s Service) DryRunDeleteBranch(ctx context.Context, commandPath string, opts DeleteBranchOptions) (dryrun.Result, error) {
	clusterID, branchID, endpoint, err := s.deleteBranchRequestAndEndpoint(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"delete_db_cluster_branch",
		dryrun.RequestSummary{
			Method:      http.MethodDelete,
			Path:        "/v1beta1/clusters/" + clusterID + "/branches/" + branchID,
			Description: "normal execution first verifies the parent cluster is Starter, then reads the branch before deleting",
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
		dryrun.Check{Name: "permission_requirement", Status: "passed", Message: string(authz.StarterBranchDelete)},
		dryrun.Check{Name: "cluster_id", Status: "passed", Message: clusterID},
		dryrun.Check{Name: "branch_id", Status: "passed", Message: branchID},
		dryrun.Check{Name: "starter_cluster_precondition", Status: "passed", Message: "normal execution verifies the parent cluster is Starter before deleting the branch"},
	), nil
}

func ensureStarterClusterByID(ctx context.Context, client *apistarter.Client, clusterID string) error {
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{})
	if err != nil {
		return err
	}
	return ensureStarterCluster(cluster)
}

func (s Service) createBranchRequest(opts CreateBranchOptions) (string, apistarter.CreateBranchRequest, error) {
	clusterID, request, _, err := s.createBranchRequestAndEndpoint(opts)
	return clusterID, request, err
}

func (s Service) createBranchRequestAndEndpoint(opts CreateBranchOptions) (string, apistarter.CreateBranchRequest, endpoints.Endpoint, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", apistarter.CreateBranchRequest{}, endpoints.Endpoint{}, err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return "", apistarter.CreateBranchRequest{}, endpoints.Endpoint{}, err
	}
	if err := validate.BranchName(opts.DisplayName); err != nil {
		return "", apistarter.CreateBranchRequest{}, endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return "", apistarter.CreateBranchRequest{}, endpoints.Endpoint{}, err
	}
	return clusterID, apistarter.CreateBranchRequest{DisplayName: strings.TrimSpace(opts.DisplayName)}, endpoint, nil
}

func (s Service) deleteBranchRequestAndEndpoint(opts DeleteBranchOptions) (string, string, endpoints.Endpoint, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", "", endpoints.Endpoint{}, err
	}
	clusterID, branchID, err := validateBranchIdentity(opts.ClusterID, opts.BranchID)
	if err != nil {
		return "", "", endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveStarter(opts.Profile)
	if err != nil {
		return "", "", endpoints.Endpoint{}, err
	}
	return clusterID, branchID, endpoint, nil
}

func validateListBranchesOptions(opts ListBranchesOptions) (string, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return "", err
	}
	if err := validate.NonNegative("--page-size", opts.PageSize); err != nil {
		return "", err
	}
	return clusterID, nil
}

func validateBranchIdentity(clusterIDValue, branchIDValue string) (string, string, error) {
	clusterID, err := validate.ClusterID(clusterIDValue)
	if err != nil {
		return "", "", err
	}
	branchID, err := validate.BranchID(branchIDValue)
	if err != nil {
		return "", "", err
	}
	return clusterID, branchID, nil
}

func (r ListBranchesResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tDISPLAY_NAME\tCLUSTER_ID\tSTATE\tPARENT\tCREATED")
	for _, branch := range r.Branches {
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			branch.ID,
			branch.DisplayName,
			branch.ClusterID,
			branch.State,
			branch.ParentID,
			branch.CreateTime,
		)
	}
	if r.NextPageToken != "" {
		_, _ = fmt.Fprintf(writer, "next_page_token\t%s\t\t\t\t\n", r.NextPageToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r BranchResult) Human() string {
	lines := []string{
		"ID: " + r.ID,
		"Display name: " + r.DisplayName,
		"Cluster ID: " + r.ClusterID,
		"State: " + r.State,
	}
	if r.ParentID != "" {
		lines = append(lines, "Parent: "+r.ParentID)
	}
	if r.CreateTime != "" {
		lines = append(lines, "Created: "+r.CreateTime)
	}
	return strings.Join(lines, "\n")
}
