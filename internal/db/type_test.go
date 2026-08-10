package db

import (
	"testing"

	"github.com/tidbcloud/tdc/internal/apperr"
)

func TestParseCLIClusterTypeRequiresExactStarter(t *testing.T) {
	tests := []struct {
		value    string
		want     ClusterType
		wantCode string
	}{
		{value: "starter", want: ClusterTypeStarter},
		{value: "", wantCode: "db.missing_required_flag"},
		{value: "   ", wantCode: "db.missing_required_flag"},
		{value: "STARTER", wantCode: "db.unsupported_cluster_type"},
		{value: "Starter", wantCode: "db.unsupported_cluster_type"},
		{value: "serverless", wantCode: "db.unsupported_cluster_type"},
		{value: "essential", wantCode: "db.unsupported_cluster_type"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseCLIClusterType(tt.value)
			if got != tt.want || apperr.CodeFor(err) != tt.wantCode {
				t.Fatalf("ParseCLIClusterType(%q) = (%q, %q), want (%q, %q)", tt.value, got, apperr.CodeFor(err), tt.want, tt.wantCode)
			}
		})
	}
}

func TestResolveServerClusterType(t *testing.T) {
	tests := []struct {
		name        string
		servicePlan string
		clusterPlan string
		want        ClusterType
		wantCode    string
	}{
		{name: "service plan", servicePlan: "STARTER", want: ClusterTypeStarter},
		{name: "legacy fallback", clusterPlan: "Essential", want: ClusterTypeEssential},
		{name: "matching fields", servicePlan: "Premium", clusterPlan: "PREMIUM", want: ClusterTypePremium},
		{name: "dedicated", servicePlan: "dedicated", want: ClusterTypeDedicated},
		{name: "missing", wantCode: "db.cluster_type_unknown"},
		{name: "unknown", servicePlan: "future", wantCode: "db.cluster_type_unknown"},
		{name: "conflict", servicePlan: "starter", clusterPlan: "essential", wantCode: "db.cluster_type_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveServerClusterType("cluster-1", tt.servicePlan, tt.clusterPlan)
			if got != tt.want || apperr.CodeFor(err) != tt.wantCode {
				t.Fatalf("ResolveServerClusterType() = (%q, %q), want (%q, %q)", got, apperr.CodeFor(err), tt.want, tt.wantCode)
			}
		})
	}
}
