package db

import (
	"fmt"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type ClusterType string

const (
	ClusterTypeStarter   ClusterType = "starter"
	ClusterTypeEssential ClusterType = "essential"
	ClusterTypePremium   ClusterType = "premium"
	ClusterTypeDedicated ClusterType = "dedicated"
)

func ParseCLIClusterType(value string) (ClusterType, error) {
	if strings.TrimSpace(value) == "" {
		return "", apperr.New("db.missing_required_flag", "usage", 2, "--db-cluster-type is required")
	}
	if value != string(ClusterTypeStarter) {
		return "", apperr.New("db.unsupported_cluster_type", "usage", 2, "--db-cluster-type must be one of: starter")
	}
	return ClusterTypeStarter, nil
}

func ResolveServerClusterType(clusterID, servicePlan, clusterPlan string) (ClusterType, error) {
	serviceType, serviceKnown := parseServerPlan(servicePlan)
	legacyType, legacyKnown := parseServerPlan(clusterPlan)
	servicePresent := strings.TrimSpace(servicePlan) != ""
	legacyPresent := strings.TrimSpace(clusterPlan) != ""

	if servicePresent && !serviceKnown || legacyPresent && !legacyKnown {
		return "", clusterTypeUnknown(clusterID, servicePlan, clusterPlan)
	}
	if servicePresent && legacyPresent && serviceType != legacyType {
		return "", clusterTypeUnknown(clusterID, servicePlan, clusterPlan)
	}
	if servicePresent {
		return serviceType, nil
	}
	if legacyPresent {
		return legacyType, nil
	}
	return "", clusterTypeUnknown(clusterID, servicePlan, clusterPlan)
}

func parseServerPlan(value string) (ClusterType, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ClusterTypeStarter):
		return ClusterTypeStarter, true
	case string(ClusterTypeEssential):
		return ClusterTypeEssential, true
	case string(ClusterTypePremium):
		return ClusterTypePremium, true
	case string(ClusterTypeDedicated):
		return ClusterTypeDedicated, true
	default:
		return "", false
	}
}

func clusterTypeUnknown(clusterID, servicePlan, clusterPlan string) error {
	return apperr.New(
		"db.cluster_type_unknown",
		"api",
		1,
		fmt.Sprintf("cannot determine the database cluster type for cluster %q from servicePlan %q and clusterPlan %q", strings.TrimSpace(clusterID), strings.TrimSpace(servicePlan), strings.TrimSpace(clusterPlan)),
	)
}

func UnsupportedClusterType(clusterID string, clusterType ClusterType) error {
	return apperr.New(
		"db.cluster_type_not_supported",
		"usage",
		2,
		fmt.Sprintf("cluster %q uses database cluster type %q, which is not supported by this tdc version", strings.TrimSpace(clusterID), clusterType),
	)
}
