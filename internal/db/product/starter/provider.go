package starter

import (
	"context"
	"fmt"

	apistarter "github.com/tidbcloud/tdc/internal/api/starter"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
	rootdb "github.com/tidbcloud/tdc/internal/db"
	"github.com/tidbcloud/tdc/internal/db/validate"
)

type clusterSnapshot struct {
	cluster apistarter.Cluster
}

func (s clusterSnapshot) DBClusterSnapshotID() string { return s.cluster.ID }

func (Service) ClusterType() rootdb.ClusterType { return rootdb.ClusterTypeStarter }

func (Service) Permission(operation rootdb.Operation) (authz.Permission, error) {
	switch operation {
	case rootdb.OperationClusterCreate:
		return authz.StarterClusterCreate, nil
	case rootdb.OperationClusterList, rootdb.OperationClusterDescribe:
		return authz.StarterClusterRead, nil
	case rootdb.OperationClusterUpdate:
		return authz.StarterClusterUpdate, nil
	case rootdb.OperationClusterDelete:
		return authz.StarterClusterDelete, nil
	case rootdb.OperationBranchCreate:
		return authz.StarterBranchCreate, nil
	case rootdb.OperationBranchList, rootdb.OperationBranchDescribe:
		return authz.StarterBranchRead, nil
	case rootdb.OperationBranchDelete:
		return authz.StarterBranchDelete, nil
	case rootdb.OperationSQLUserCreate:
		return authz.StarterSQLUserCreate, nil
	case rootdb.OperationConnectionStringFormat:
		return authz.StarterSQLUserRead, nil
	case rootdb.OperationSQLExecute:
		return authz.StarterSQLExecute, nil
	default:
		return "", rootdb.MissingPermission(rootdb.ClusterTypeStarter, operation)
	}
}

func (s Service) ResolveCluster(ctx context.Context, request rootdb.ResolveClusterRequest) (rootdb.ResolvedCluster, error) {
	if err := validateProfile(request.Profile); err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	clusterID, err := validate.ClusterID(request.ClusterID)
	if err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	if err := validate.View(request.View); err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	client, err := s.starterClient(request.Profile, request.Permission, "discover database cluster type")
	if err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{View: request.View})
	if err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	clusterType, err := rootdb.ResolveServerClusterType(cluster.ID, cluster.ServicePlan, cluster.ClusterPlan)
	if err != nil {
		return rootdb.ResolvedCluster{}, err
	}
	return rootdb.ResolvedCluster{
		ID:          cluster.ID,
		Type:        clusterType,
		ServicePlan: cluster.ServicePlan,
		ClusterPlan: cluster.ClusterPlan,
		Snapshot:    clusterSnapshot{cluster: cluster},
	}, nil
}

func clusterFromDispatch(dispatch rootdb.DispatchContext, clusterID string) (apistarter.Cluster, error) {
	if dispatch.Resolved == nil || dispatch.Resolved.Snapshot == nil {
		return apistarter.Cluster{}, apperr.New("db.cluster_snapshot_missing", "runtime", 1, fmt.Sprintf("database cluster %q has no discovery snapshot", clusterID))
	}
	snapshot, ok := dispatch.Resolved.Snapshot.(clusterSnapshot)
	if !ok || snapshot.cluster.ID != clusterID {
		return apistarter.Cluster{}, apperr.New("db.cluster_snapshot_invalid", "runtime", 1, fmt.Sprintf("database cluster %q has an incompatible discovery snapshot", clusterID))
	}
	return snapshot.cluster, nil
}

func (s Service) clusterFromDispatchOrRead(ctx context.Context, profile *config.Profile, dispatch rootdb.DispatchContext, clusterID, view string, fallbackPermission authz.Permission, action string) (apistarter.Cluster, error) {
	if dispatch.Resolved != nil {
		return clusterFromDispatch(dispatch, clusterID)
	}
	client, err := s.starterClient(profile, fallbackPermission, action)
	if err != nil {
		return apistarter.Cluster{}, err
	}
	cluster, err := client.GetCluster(ctx, clusterID, apistarter.GetClusterOptions{View: view})
	if err != nil {
		return apistarter.Cluster{}, err
	}
	if err := ensureStarterCluster(cluster); err != nil {
		return apistarter.Cluster{}, err
	}
	return cluster, nil
}

func operationPermission(dispatch rootdb.DispatchContext, fallback authz.Permission) authz.Permission {
	if dispatch.OperationPermission != "" {
		return dispatch.OperationPermission
	}
	return fallback
}
