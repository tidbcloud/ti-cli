package db

import (
	"testing"

	apistarter "github.com/tidbcloud/tdc/internal/api/starter"
	"github.com/tidbcloud/tdc/internal/config"
)

func TestClusterRegionScopeMatchesDocumentedRepresentations(t *testing.T) {
	awsScope := newClusterRegionScope(&config.Profile{CloudProvider: "aws", RegionCode: "us-east-1"})
	aliScope := newClusterRegionScope(&config.Profile{CloudProvider: "alibaba_cloud", RegionCode: "ap-southeast-1"})

	tests := []struct {
		name   string
		scope  clusterRegionScope
		region apistarter.Region
		want   bool
	}{
		{name: "name only", scope: awsScope, region: apistarter.Region{Name: "regions/aws-us-east-1"}, want: true},
		{name: "qualified region id only", scope: awsScope, region: apistarter.Region{RegionID: "aws-us-east-1"}, want: true},
		{name: "native id and cloud provider", scope: awsScope, region: apistarter.Region{RegionID: "us-east-1", CloudProvider: "AWS"}, want: true},
		{name: "native id and legacy provider", scope: awsScope, region: apistarter.Region{RegionID: "us-east-1", Provider: "aws"}, want: true},
		{name: "alicloud name", scope: aliScope, region: apistarter.Region{Name: "regions/alicloud-ap-southeast-1"}, want: true},
		{name: "alicloud provider and native id", scope: aliScope, region: apistarter.Region{RegionID: "ap-southeast-1", CloudProvider: "alicloud"}, want: true},
		{name: "internal alibaba provider", scope: aliScope, region: apistarter.Region{RegionID: "ap-southeast-1", CloudProvider: "alibaba_cloud"}, want: true},
		{name: "tdc-qualified alibaba id", scope: aliScope, region: apistarter.Region{RegionID: "ali-ap-southeast-1"}, want: true},
		{name: "other aws region", scope: awsScope, region: apistarter.Region{Name: "regions/aws-us-west-2"}},
		{name: "same native region different provider", scope: aliScope, region: apistarter.Region{Name: "regions/aws-ap-southeast-1"}},
		{name: "conflicting name and region id", scope: awsScope, region: apistarter.Region{Name: "regions/aws-us-east-1", RegionID: "us-west-2"}},
		{name: "canonical provider overrides conflicting legacy", scope: awsScope, region: apistarter.Region{RegionID: "us-east-1", CloudProvider: "alicloud", Provider: "aws"}},
		{name: "native region without provider", scope: awsScope, region: apistarter.Region{RegionID: "us-east-1"}},
		{name: "provider without region", scope: awsScope, region: apistarter.Region{CloudProvider: "aws"}},
		{name: "missing metadata", scope: awsScope, region: apistarter.Region{}},
		{name: "malformed name with valid fallback fields", scope: awsScope, region: apistarter.Region{Name: "us-east-1", RegionID: "us-east-1", CloudProvider: "aws"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.matches(tt.region); got != tt.want {
				t.Fatalf("matches(%#v) = %v, want %v", tt.region, got, tt.want)
			}
		})
	}
}

func TestClusterRegionScopeAPIFilter(t *testing.T) {
	tests := []struct {
		name       string
		profile    *config.Profile
		userFilter string
		want       string
	}{
		{
			name:    "aws",
			profile: &config.Profile{CloudProvider: "aws", RegionCode: "us-east-1"},
			want:    `region.provider="aws" AND region.name="regions/aws-us-east-1"`,
		},
		{
			name:       "alibaba with user filter",
			profile:    &config.Profile{CloudProvider: "alibaba_cloud", RegionCode: "ap-southeast-1"},
			userFilter: ` state="ACTIVE" `,
			want:       `region.provider="alicloud" AND region.name="regions/alicloud-ap-southeast-1" AND state="ACTIVE"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newClusterRegionScope(tt.profile).apiFilter(tt.userFilter); got != tt.want {
				t.Fatalf("apiFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterClustersByRegionExcludesUnverifiableResources(t *testing.T) {
	scope := newClusterRegionScope(&config.Profile{CloudProvider: "aws", RegionCode: "us-east-1"})
	clusters := []apistarter.Cluster{
		{ID: "east", Region: apistarter.Region{Name: "regions/aws-us-east-1"}},
		{ID: "west", Region: apistarter.Region{Name: "regions/aws-us-west-2"}},
		{ID: "missing"},
	}

	filtered := filterClustersByRegion(clusters, scope)
	if len(filtered) != 1 || filtered[0].ID != "east" {
		t.Fatalf("unexpected filtered clusters: %#v", filtered)
	}
}
