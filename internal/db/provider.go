package db

import (
	"context"
	"errors"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
)

var ErrResolverNotApplicable = errors.New("database cluster resolver not applicable")

type ClusterSnapshot interface {
	DBClusterSnapshotID() string
}

type ResolvedCluster struct {
	ID          string
	Type        ClusterType
	ServicePlan string
	ClusterPlan string
	Snapshot    ClusterSnapshot
}

type ResolveClusterRequest struct {
	Profile    *config.Profile
	ClusterID  string
	View       string
	Permission authz.Permission
}

type ClusterResolver interface {
	ResolveCluster(context.Context, ResolveClusterRequest) (ResolvedCluster, error)
}

type Provider interface {
	ClusterType() ClusterType
	Permission(Operation) (authz.Permission, error)
}

func MissingPermission(clusterType ClusterType, operation Operation) error {
	return apperr.New(
		"authz.permission_mapping_missing",
		"usage",
		2,
		"internal permission mapping missing for database cluster type "+string(clusterType)+" operation "+string(operation),
	)
}

func MissingCapability(clusterType ClusterType, operation Operation) error {
	return apperr.New(
		"db.operation_not_supported",
		"usage",
		2,
		"database cluster type "+string(clusterType)+" does not support operation "+string(operation),
	)
}

func InvalidProductOptions(clusterType ClusterType, operation Operation) error {
	return apperr.New(
		"db.product_options_invalid",
		"usage",
		2,
		"invalid product-specific options for database cluster type "+string(clusterType)+" operation "+string(operation),
	)
}
