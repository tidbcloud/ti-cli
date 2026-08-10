package db

import (
	"fmt"
	"strings"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const starterPlan = "starter"

func ensureStarterCluster(cluster apistarter.Cluster) error {
	plan, err := resolveClusterPlan(cluster)
	if err != nil {
		return err
	}
	if plan == starterPlan {
		return nil
	}
	return apperr.New(
		"db.not_starter_cluster",
		"usage",
		2,
		fmt.Sprintf("cluster %q uses service plan %q; ti db only manages Starter clusters", cluster.ID, clusterPlanDisplay(cluster)),
	)
}

func resolveClusterPlan(cluster apistarter.Cluster) (string, error) {
	servicePlan := normalizeClusterPlan(cluster.ServicePlan)
	legacyPlan := normalizeClusterPlan(cluster.ClusterPlan)

	if servicePlan != "" && legacyPlan != "" && servicePlan != legacyPlan {
		return "", apperr.New(
			"db.cluster_plan_unknown",
			"api",
			1,
			fmt.Sprintf("cannot verify cluster %q is a Starter cluster because servicePlan %q conflicts with clusterPlan %q; no operation was performed", cluster.ID, strings.TrimSpace(cluster.ServicePlan), strings.TrimSpace(cluster.ClusterPlan)),
		)
	}
	if servicePlan != "" {
		return servicePlan, nil
	}
	if legacyPlan != "" {
		return legacyPlan, nil
	}
	return "", apperr.New(
		"db.cluster_plan_unknown",
		"api",
		1,
		fmt.Sprintf("cannot verify cluster %q is a Starter cluster because the API response omitted servicePlan and clusterPlan; no operation was performed", cluster.ID),
	)
}

func normalizeClusterPlan(plan string) string {
	return strings.ToLower(strings.TrimSpace(plan))
}

func clusterPlanDisplay(cluster apistarter.Cluster) string {
	if plan := strings.TrimSpace(cluster.ServicePlan); plan != "" {
		return plan
	}
	return strings.TrimSpace(cluster.ClusterPlan)
}

func filterStarterClusters(clusters []apistarter.Cluster) []apistarter.Cluster {
	filtered := make([]apistarter.Cluster, 0, len(clusters))
	for _, cluster := range clusters {
		if ensureStarterCluster(cluster) == nil {
			filtered = append(filtered, cluster)
		}
	}
	return filtered
}

func createdClusterPlanError(cluster apistarter.Cluster, cause error) error {
	return apperr.Wrap(
		apperr.CodeFor(cause),
		apperr.CategoryFor(cause),
		apperr.ExitCodeFor(cause),
		fmt.Sprintf("DB cluster %q creation was accepted in state %q but ti could not verify it as a Starter cluster; the cluster was retained; inspect it with `ti db describe-db-cluster --db-cluster-id %s`", cluster.ID, clusterStateDisplay(cluster), cluster.ID),
		cause,
	)
}

func updatedClusterPlanError(cluster apistarter.Cluster, cause error) error {
	return apperr.Wrap(
		apperr.CodeFor(cause),
		apperr.CategoryFor(cause),
		apperr.ExitCodeFor(cause),
		fmt.Sprintf("DB cluster %q update was accepted but ti could not verify the response as Starter; the update may have been applied; inspect it with `ti db describe-db-cluster --db-cluster-id %s`", cluster.ID, cluster.ID),
		cause,
	)
}

func deletingClusterPlanError(cluster apistarter.Cluster, cause error) error {
	return apperr.Wrap(
		apperr.CodeFor(cause),
		apperr.CategoryFor(cause),
		apperr.ExitCodeFor(cause),
		fmt.Sprintf("DB cluster %q deletion was accepted but ti could not verify a later response as Starter; deletion may still be in progress", cluster.ID),
		cause,
	)
}

func clusterStateDisplay(cluster apistarter.Cluster) string {
	if state := strings.TrimSpace(cluster.State); state != "" {
		return state
	}
	return "unknown"
}
