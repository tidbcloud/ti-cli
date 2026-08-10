package db

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/db/connectionstring"
	"github.com/tidbcloud/ti-cli/internal/db/sqlaccess"
	"github.com/tidbcloud/ti-cli/internal/db/sqlresult"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
)

type ProductOptions interface {
	DBProductOptions()
}

type DispatchContext struct {
	Resolved            *ResolvedCluster
	DiscoveryPermission authz.Permission
	OperationPermission authz.Permission
}

type ListClustersOptions struct {
	Profile     *config.Profile
	ClusterType string
	PageSize    int32
	PageToken   string
	Filter      string
	OrderBy     string
}

type CreateClusterOptions struct {
	Profile           *config.Profile
	DisplayName       string
	ClusterType       string
	ProjectID         string
	ProjectIDExplicit bool
	WaitUntilActive   bool
	Product           ProductOptions
	Dispatch          DispatchContext
}

type DescribeClusterOptions struct {
	Profile   *config.Profile
	ClusterID string
	View      string
	Dispatch  DispatchContext
}

type UpdateClusterOptions struct {
	Profile     *config.Profile
	ClusterID   string
	DisplayName string
	Product     ProductOptions
	Dispatch    DispatchContext
}

type DeleteClusterOptions struct {
	Profile          *config.Profile
	ClusterID        string
	WaitUntilDeleted bool
	Dispatch         DispatchContext
}

type ListClustersResult struct {
	Clusters      []apistarter.Cluster `json:"clusters"`
	NextPageToken string               `json:"next_page_token,omitempty"`
}

type ClusterResult struct {
	apistarter.Cluster
}

type ListBranchesOptions struct {
	Profile   *config.Profile
	ClusterID string
	PageSize  int32
	PageToken string
	Dispatch  DispatchContext
}

type CreateBranchOptions struct {
	Profile         *config.Profile
	ClusterID       string
	DisplayName     string
	WaitUntilActive bool
	Dispatch        DispatchContext
}

type DescribeBranchOptions struct {
	Profile   *config.Profile
	ClusterID string
	BranchID  string
	View      string
	Dispatch  DispatchContext
}

type DeleteBranchOptions struct {
	Profile   *config.Profile
	ClusterID string
	BranchID  string
	Dispatch  DispatchContext
}

type ListBranchesResult struct {
	Branches      []apistarter.Branch `json:"branches"`
	NextPageToken string              `json:"next_page_token,omitempty"`
	TotalSize     int64               `json:"total_size,omitempty"`
}

type BranchResult struct {
	apistarter.Branch
}

type PrepareQueryAccessOptions struct {
	Profile   *config.Profile
	ClusterID string
	Dispatch  DispatchContext
}

type CreateConnectionStringOptions struct {
	Profile                *config.Profile
	ClusterID              string
	Database               string
	ReadOnly               bool
	ReadWrite              bool
	Admin                  bool
	Format                 string
	EnvPrefix              string
	EnvIncludeDatabaseURL  bool
	EnvDatabaseURLVariable string
	Dispatch               DispatchContext
}

type ExecuteSQLOptions struct {
	Profile   *config.Profile
	ClusterID string
	Database  string
	SQL       string
	ReadOnly  bool
	ReadWrite bool
	Admin     bool
	Transport string
	Dispatch  DispatchContext
}

type PrepareQueryAccessResult struct {
	sqlaccess.Result
}

type ListClusterPageOptions struct {
	Profile             *config.Profile
	UpstreamPageSize    int32
	UpstreamPageToken   string
	Filter              string
	OrderBy             string
	OperationPermission authz.Permission
}

type ListClusterPageResult struct {
	Clusters      []apistarter.Cluster
	NextPageToken string
}

type ClusterCreator interface {
	CreateCluster(context.Context, CreateClusterOptions) (ClusterResult, error)
	DryRunCreateCluster(context.Context, string, CreateClusterOptions) (dryrun.Result, error)
}

type ClusterLister interface {
	ListClusterPage(context.Context, ListClusterPageOptions) (ListClusterPageResult, error)
}

type ClusterDescriber interface {
	DescribeCluster(context.Context, DescribeClusterOptions) (ClusterResult, error)
}

type ClusterUpdater interface {
	UpdateCluster(context.Context, UpdateClusterOptions) (ClusterResult, error)
	DryRunUpdateCluster(context.Context, string, UpdateClusterOptions) (dryrun.Result, error)
}

type ClusterDeleter interface {
	DeleteCluster(context.Context, DeleteClusterOptions) (ClusterResult, error)
	DryRunDeleteCluster(context.Context, string, DeleteClusterOptions) (dryrun.Result, error)
}

type BranchLister interface {
	ListBranches(context.Context, ListBranchesOptions) (ListBranchesResult, error)
}

type BranchCreator interface {
	CreateBranch(context.Context, CreateBranchOptions) (BranchResult, error)
	DryRunCreateBranch(context.Context, string, CreateBranchOptions) (dryrun.Result, error)
}

type BranchDescriber interface {
	DescribeBranch(context.Context, DescribeBranchOptions) (BranchResult, error)
}

type BranchDeleter interface {
	DeleteBranch(context.Context, DeleteBranchOptions) (BranchResult, error)
	DryRunDeleteBranch(context.Context, string, DeleteBranchOptions) (dryrun.Result, error)
}

type SQLUserCreator interface {
	PrepareQueryAccess(context.Context, PrepareQueryAccessOptions) (PrepareQueryAccessResult, error)
	DryRunPrepareQueryAccess(context.Context, string, PrepareQueryAccessOptions) (dryrun.Result, error)
}

type ConnectionStringFormatter interface {
	CreateConnectionString(context.Context, CreateConnectionStringOptions) (connectionstring.Result, error)
}

type SQLExecutor interface {
	ExecuteSQL(context.Context, ExecuteSQLOptions) (sqlresult.Result, error)
}

func (r ListClustersResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tDISPLAY_NAME\tREGION\tSTATE\tPLAN\tCREATED")
	for _, cluster := range r.Clusters {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", cluster.ID, cluster.DisplayName, cluster.Region.Name, cluster.State, clusterPlanDisplay(cluster), cluster.CreateTime)
	}
	if r.NextPageToken != "" {
		_, _ = fmt.Fprintf(writer, "next_page_token\t%s\t\t\t\t\n", r.NextPageToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r ClusterResult) Human() string {
	lines := []string{"ID: " + r.ID, "Display name: " + r.DisplayName, "Region: " + r.Region.Name, "State: " + r.State}
	if plan := clusterPlanDisplay(r.Cluster); plan != "" {
		lines = append(lines, "Plan: "+plan)
	}
	if r.CreateTime != "" {
		lines = append(lines, "Created: "+r.CreateTime)
	}
	return strings.Join(lines, "\n")
}

func (r ListBranchesResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tDISPLAY_NAME\tCLUSTER_ID\tSTATE\tPARENT\tCREATED")
	for _, branch := range r.Branches {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", branch.ID, branch.DisplayName, branch.ClusterID, branch.State, branch.ParentID, branch.CreateTime)
	}
	if r.NextPageToken != "" {
		_, _ = fmt.Fprintf(writer, "next_page_token\t%s\t\t\t\t\n", r.NextPageToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r BranchResult) Human() string {
	lines := []string{"ID: " + r.ID, "Display name: " + r.DisplayName, "Cluster ID: " + r.ClusterID, "State: " + r.State}
	if r.ParentID != "" {
		lines = append(lines, "Parent: "+r.ParentID)
	}
	if r.CreateTime != "" {
		lines = append(lines, "Created: "+r.CreateTime)
	}
	return strings.Join(lines, "\n")
}

func clusterPlanDisplay(cluster apistarter.Cluster) string {
	if plan := strings.TrimSpace(cluster.ServicePlan); plan != "" {
		return plan
	}
	return strings.TrimSpace(cluster.ClusterPlan)
}
