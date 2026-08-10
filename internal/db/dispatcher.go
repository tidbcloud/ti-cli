package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/db/connectionstring"
	"github.com/tidbcloud/ti-cli/internal/db/sqlresult"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
)

type Dispatcher struct {
	resolvers []ClusterResolver
	providers map[ClusterType]Provider
}

func NewDispatcher(resolvers []ClusterResolver, providers ...Provider) (*Dispatcher, error) {
	if len(resolvers) == 0 {
		return nil, apperr.New("db.resolver_missing", "runtime", 1, "no database cluster resolver is registered")
	}
	d := &Dispatcher{
		resolvers: append([]ClusterResolver(nil), resolvers...),
		providers: make(map[ClusterType]Provider, len(providers)),
	}
	for _, provider := range providers {
		if provider == nil {
			return nil, apperr.New("db.provider_invalid", "runtime", 1, "nil database provider is registered")
		}
		clusterType := provider.ClusterType()
		if clusterType == "" {
			return nil, apperr.New("db.provider_invalid", "runtime", 1, "database provider has an empty cluster type")
		}
		if _, exists := d.providers[clusterType]; exists {
			return nil, apperr.New("db.provider_duplicate", "runtime", 1, "multiple database providers are registered for cluster type "+string(clusterType))
		}
		d.providers[clusterType] = provider
	}
	return d, nil
}

func (d *Dispatcher) CreateCluster(ctx context.Context, opts CreateClusterOptions) (ClusterResult, error) {
	provider, permission, err := d.selectedProvider(opts.ClusterType, OperationClusterCreate)
	if err != nil {
		return ClusterResult{}, err
	}
	capability, ok := provider.(ClusterCreator)
	if !ok {
		return ClusterResult{}, MissingCapability(provider.ClusterType(), OperationClusterCreate)
	}
	opts.Dispatch.OperationPermission = permission
	return capability.CreateCluster(ctx, opts)
}

func (d *Dispatcher) DryRunCreateCluster(ctx context.Context, commandPath string, opts CreateClusterOptions) (dryrun.Result, error) {
	provider, permission, err := d.selectedProvider(opts.ClusterType, OperationClusterCreate)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(ClusterCreator)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationClusterCreate)
	}
	opts.Dispatch.OperationPermission = permission
	return capability.DryRunCreateCluster(ctx, commandPath, opts)
}

func (d *Dispatcher) ListClusters(ctx context.Context, opts ListClustersOptions) (ListClustersResult, error) {
	provider, permission, err := d.selectedProvider(opts.ClusterType, OperationClusterList)
	if err != nil {
		return ListClustersResult{}, err
	}
	capability, ok := provider.(ClusterLister)
	if !ok {
		return ListClustersResult{}, MissingCapability(provider.ClusterType(), OperationClusterList)
	}
	want, err := resultPageSize(opts.PageSize)
	if err != nil {
		return ListClustersResult{}, err
	}
	expected := newListCursor(opts, provider.ClusterType())
	cursor, err := decodeListCursor(opts.PageToken, expected)
	if err != nil {
		return ListClustersResult{}, err
	}

	result := ListClustersResult{Clusters: make([]apistarter.Cluster, 0, want)}
	for len(result.Clusters) < want {
		pageTokenUsed := cursor.UpstreamPageToken
		page, err := capability.ListClusterPage(ctx, ListClusterPageOptions{
			Profile:             opts.Profile,
			UpstreamPageSize:    upstreamClusterPageSize,
			UpstreamPageToken:   pageTokenUsed,
			Filter:              opts.Filter,
			OrderBy:             opts.OrderBy,
			OperationPermission: permission,
		})
		if err != nil {
			return ListClustersResult{}, err
		}

		fingerprint := pageFingerprint(page.Clusters)
		if cursor.MatchedOffset > 0 && fingerprint != cursor.PageFingerprint {
			return ListClustersResult{}, apperr.New("db.page_token_stale", "api", 1, "the database cluster page changed after the page token was issued; restart listing without --page-token")
		}
		if cursor.MatchedOffset > len(page.Clusters) {
			return ListClustersResult{}, apperr.New("db.page_token_stale", "api", 1, "the database cluster page no longer contains the saved position; restart listing without --page-token")
		}

		remaining := want - len(result.Clusters)
		available := page.Clusters[cursor.MatchedOffset:]
		if len(available) > remaining {
			result.Clusters = append(result.Clusters, available[:remaining]...)
			cursor.UpstreamPageToken = pageTokenUsed
			cursor.MatchedOffset += remaining
			cursor.PageFingerprint = fingerprint
			result.NextPageToken, err = encodeListCursor(cursor)
			return result, err
		}
		result.Clusters = append(result.Clusters, available...)

		if page.NextPageToken == "" {
			return result, nil
		}
		if page.NextPageToken == pageTokenUsed {
			return ListClustersResult{}, apperr.New("db.pagination_cycle", "api", 1, "TiDB Cloud returned the same database cluster page token twice")
		}
		cursor.UpstreamPageToken = page.NextPageToken
		cursor.MatchedOffset = 0
		cursor.PageFingerprint = ""
	}

	result.NextPageToken, err = encodeListCursor(cursor)
	return result, err
}

func (d *Dispatcher) DescribeCluster(ctx context.Context, opts DescribeClusterOptions) (ClusterResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, opts.View, OperationClusterDescribe)
	if err != nil {
		return ClusterResult{}, err
	}
	capability, ok := provider.(ClusterDescriber)
	if !ok {
		return ClusterResult{}, MissingCapability(provider.ClusterType(), OperationClusterDescribe)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DescribeCluster(ctx, opts)
}

func (d *Dispatcher) UpdateCluster(ctx context.Context, opts UpdateClusterOptions) (ClusterResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationClusterUpdate)
	if err != nil {
		return ClusterResult{}, err
	}
	capability, ok := provider.(ClusterUpdater)
	if !ok {
		return ClusterResult{}, MissingCapability(provider.ClusterType(), OperationClusterUpdate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.UpdateCluster(ctx, opts)
}

func (d *Dispatcher) DryRunUpdateCluster(ctx context.Context, commandPath string, opts UpdateClusterOptions) (dryrun.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationClusterUpdate)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(ClusterUpdater)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationClusterUpdate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DryRunUpdateCluster(ctx, commandPath, opts)
}

func (d *Dispatcher) DeleteCluster(ctx context.Context, opts DeleteClusterOptions) (ClusterResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationClusterDelete)
	if err != nil {
		return ClusterResult{}, err
	}
	capability, ok := provider.(ClusterDeleter)
	if !ok {
		return ClusterResult{}, MissingCapability(provider.ClusterType(), OperationClusterDelete)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DeleteCluster(ctx, opts)
}

func (d *Dispatcher) DryRunDeleteCluster(ctx context.Context, commandPath string, opts DeleteClusterOptions) (dryrun.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationClusterDelete)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(ClusterDeleter)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationClusterDelete)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DryRunDeleteCluster(ctx, commandPath, opts)
}

func (d *Dispatcher) ListBranches(ctx context.Context, opts ListBranchesOptions) (ListBranchesResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchList)
	if err != nil {
		return ListBranchesResult{}, err
	}
	capability, ok := provider.(BranchLister)
	if !ok {
		return ListBranchesResult{}, MissingCapability(provider.ClusterType(), OperationBranchList)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.ListBranches(ctx, opts)
}

func (d *Dispatcher) CreateBranch(ctx context.Context, opts CreateBranchOptions) (BranchResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchCreate)
	if err != nil {
		return BranchResult{}, err
	}
	capability, ok := provider.(BranchCreator)
	if !ok {
		return BranchResult{}, MissingCapability(provider.ClusterType(), OperationBranchCreate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.CreateBranch(ctx, opts)
}

func (d *Dispatcher) DryRunCreateBranch(ctx context.Context, commandPath string, opts CreateBranchOptions) (dryrun.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchCreate)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(BranchCreator)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationBranchCreate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DryRunCreateBranch(ctx, commandPath, opts)
}

func (d *Dispatcher) DescribeBranch(ctx context.Context, opts DescribeBranchOptions) (BranchResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchDescribe)
	if err != nil {
		return BranchResult{}, err
	}
	capability, ok := provider.(BranchDescriber)
	if !ok {
		return BranchResult{}, MissingCapability(provider.ClusterType(), OperationBranchDescribe)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DescribeBranch(ctx, opts)
}

func (d *Dispatcher) DeleteBranch(ctx context.Context, opts DeleteBranchOptions) (BranchResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchDelete)
	if err != nil {
		return BranchResult{}, err
	}
	capability, ok := provider.(BranchDeleter)
	if !ok {
		return BranchResult{}, MissingCapability(provider.ClusterType(), OperationBranchDelete)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DeleteBranch(ctx, opts)
}

func (d *Dispatcher) DryRunDeleteBranch(ctx context.Context, commandPath string, opts DeleteBranchOptions) (dryrun.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "BASIC", OperationBranchDelete)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(BranchDeleter)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationBranchDelete)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DryRunDeleteBranch(ctx, commandPath, opts)
}

func (d *Dispatcher) PrepareQueryAccess(ctx context.Context, opts PrepareQueryAccessOptions) (PrepareQueryAccessResult, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "FULL", OperationSQLUserCreate)
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	capability, ok := provider.(SQLUserCreator)
	if !ok {
		return PrepareQueryAccessResult{}, MissingCapability(provider.ClusterType(), OperationSQLUserCreate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.PrepareQueryAccess(ctx, opts)
}

func (d *Dispatcher) DryRunPrepareQueryAccess(ctx context.Context, commandPath string, opts PrepareQueryAccessOptions) (dryrun.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "FULL", OperationSQLUserCreate)
	if err != nil {
		return dryrun.Result{}, err
	}
	capability, ok := provider.(SQLUserCreator)
	if !ok {
		return dryrun.Result{}, MissingCapability(provider.ClusterType(), OperationSQLUserCreate)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.DryRunPrepareQueryAccess(ctx, commandPath, opts)
}

func (d *Dispatcher) CreateConnectionString(ctx context.Context, opts CreateConnectionStringOptions) (connectionstring.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "FULL", OperationConnectionStringFormat)
	if err != nil {
		return connectionstring.Result{}, err
	}
	capability, ok := provider.(ConnectionStringFormatter)
	if !ok {
		return connectionstring.Result{}, MissingCapability(provider.ClusterType(), OperationConnectionStringFormat)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.CreateConnectionString(ctx, opts)
}

func (d *Dispatcher) ExecuteSQL(ctx context.Context, opts ExecuteSQLOptions) (sqlresult.Result, error) {
	provider, resolved, permission, err := d.discoveredProvider(ctx, opts.Profile, opts.ClusterID, "FULL", OperationSQLExecute)
	if err != nil {
		return sqlresult.Result{}, err
	}
	capability, ok := provider.(SQLExecutor)
	if !ok {
		return sqlresult.Result{}, MissingCapability(provider.ClusterType(), OperationSQLExecute)
	}
	opts.ClusterID = resolved.ID
	opts.Dispatch = dispatchContext(resolved, permission)
	return capability.ExecuteSQL(ctx, opts)
}

func (d *Dispatcher) selectedProvider(rawType string, operation Operation) (Provider, authz.Permission, error) {
	clusterType, err := ParseCLIClusterType(rawType)
	if err != nil {
		return nil, "", err
	}
	provider, ok := d.providers[clusterType]
	if !ok {
		return nil, "", UnsupportedClusterType("", clusterType)
	}
	permission, err := provider.Permission(operation)
	if err != nil {
		return nil, "", err
	}
	return provider, permission, nil
}

func (d *Dispatcher) discoveredProvider(ctx context.Context, profile *config.Profile, clusterID, view string, operation Operation) (Provider, ResolvedCluster, authz.Permission, error) {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return nil, ResolvedCluster{}, "", apperr.New("db.missing_required_flag", "usage", 2, "--db-cluster-id is required")
	}
	var resolved ResolvedCluster
	var err error
	for _, resolver := range d.resolvers {
		resolved, err = resolver.ResolveCluster(ctx, ResolveClusterRequest{
			Profile: profile, ClusterID: clusterID, View: view, Permission: authz.DBClusterDiscover,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrResolverNotApplicable) {
			return nil, ResolvedCluster{}, "", err
		}
	}
	if err != nil {
		return nil, ResolvedCluster{}, "", apperr.Wrap("db.cluster_resolver_not_found", "api", 1, fmt.Sprintf("no database resolver could load cluster %q", clusterID), err)
	}
	if resolved.ID == "" || resolved.Type == "" || resolved.Snapshot == nil || resolved.Snapshot.DBClusterSnapshotID() != resolved.ID {
		return nil, ResolvedCluster{}, "", apperr.New("db.cluster_resolver_invalid", "api", 1, fmt.Sprintf("database resolver returned an invalid snapshot for cluster %q", clusterID))
	}
	provider, ok := d.providers[resolved.Type]
	if !ok {
		return nil, ResolvedCluster{}, "", UnsupportedClusterType(clusterID, resolved.Type)
	}
	permission, err := provider.Permission(operation)
	if err != nil {
		return nil, ResolvedCluster{}, "", err
	}
	return provider, resolved, permission, nil
}

func dispatchContext(resolved ResolvedCluster, permission authz.Permission) DispatchContext {
	return DispatchContext{Resolved: &resolved, DiscoveryPermission: authz.DBClusterDiscover, OperationPermission: permission}
}
