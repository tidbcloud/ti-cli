package db

import (
	"fmt"
	"strings"

	"github.com/tidbcloud/tdc/internal/api/endpoints"
	apistarter "github.com/tidbcloud/tdc/internal/api/starter"
	"github.com/tidbcloud/tdc/internal/config"
	configregion "github.com/tidbcloud/tdc/internal/config/region"
)

type clusterRegionScope struct {
	provider      string
	apiProvider   string
	nativeRegion  string
	apiRegionName string
}

func newClusterRegionScope(profile *config.Profile) clusterRegionScope {
	apiProvider := endpoints.APIProvider(profile.CloudProvider)
	return clusterRegionScope{
		provider:      profile.CloudProvider,
		apiProvider:   apiProvider,
		nativeRegion:  profile.RegionCode,
		apiRegionName: "regions/" + apiProvider + "-" + profile.RegionCode,
	}
}

func (s clusterRegionScope) apiFilter(userFilter string) string {
	regionFilter := fmt.Sprintf(`region.provider=%q AND region.name=%q`, s.apiProvider, s.apiRegionName)
	if userFilter = strings.TrimSpace(userFilter); userFilter != "" {
		return regionFilter + " AND " + userFilter
	}
	return regionFilter
}

func filterClustersByRegion(clusters []apistarter.Cluster, scope clusterRegionScope) []apistarter.Cluster {
	filtered := make([]apistarter.Cluster, 0, len(clusters))
	for _, cluster := range clusters {
		if scope.matches(cluster.Region) {
			filtered = append(filtered, cluster)
		}
	}
	return filtered
}

func (s clusterRegionScope) matches(actual apistarter.Region) bool {
	providerSeen := false
	regionSeen := false

	matchProvider := func(value string) bool {
		provider, ok := normalizeClusterProvider(value)
		if !ok || provider != s.provider {
			return false
		}
		providerSeen = true
		return true
	}
	matchRegion := func(value string) bool {
		if strings.TrimSpace(value) != s.nativeRegion {
			return false
		}
		regionSeen = true
		return true
	}
	matchQualifiedRegion := func(value string) bool {
		provider, nativeRegion, ok := splitQualifiedClusterRegion(value)
		if !ok || !matchProvider(provider) || !matchRegion(nativeRegion) {
			return false
		}
		return true
	}

	if value := strings.TrimSpace(actual.Name); value != "" && !matchQualifiedRegion(value) {
		return false
	}

	provider := strings.TrimSpace(actual.CloudProvider)
	if provider == "" {
		provider = strings.TrimSpace(actual.Provider)
	}
	if provider != "" && !matchProvider(provider) {
		return false
	}

	if value := strings.TrimSpace(actual.RegionID); value != "" {
		if _, _, qualified := splitQualifiedClusterRegion(value); qualified {
			if !matchQualifiedRegion(value) {
				return false
			}
		} else if !matchRegion(value) {
			return false
		}
	}

	return providerSeen && regionSeen
}

func normalizeClusterProvider(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case configregion.ProviderAWS:
		return configregion.ProviderAWS, true
	case "ali", "alicloud", configregion.ProviderAlibabaCloud:
		return configregion.ProviderAlibabaCloud, true
	default:
		return "", false
	}
}

func splitQualifiedClusterRegion(value string) (string, string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "regions/")
	for _, prefix := range []string{"alibaba_cloud", "alicloud", "aws", "ali"} {
		if nativeRegion, ok := strings.CutPrefix(value, prefix+"-"); ok && nativeRegion != "" {
			return prefix, nativeRegion, true
		}
	}
	return "", "", false
}
