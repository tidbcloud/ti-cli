package region

import (
	"fmt"
	"sort"
	"strings"
)

const (
	ProviderAWS          = "aws"
	ProviderAlibabaCloud = "alibaba_cloud"
	ProviderGCP          = "gcp"
	ProviderAzure        = "azure"

	ProviderPrefixAWS                = "aws"
	ProviderPrefixAlibabaCloud       = "alicloud"
	ProviderPrefixAlibabaCloudLegacy = "ali"
	ProviderPrefixGCP                = "gcp"
	ProviderPrefixAzure              = "azure"
)

type Region struct {
	Code  string
	Label string
}

type Placement struct {
	Code          string
	Prefix        string
	Provider      string
	NativeCode    string
	ProviderLabel string
	RegionLabel   string
}

var supported = map[string][]Region{
	ProviderAWS: {
		{Code: "us-east-1", Label: "N. Virginia"},
		{Code: "us-west-2", Label: "Oregon"},
		{Code: "eu-central-1", Label: "Frankfurt"},
		{Code: "ap-northeast-1", Label: "Tokyo"},
		{Code: "ap-southeast-1", Label: "Singapore"},
	},
	ProviderAlibabaCloud: {
		{Code: "ap-southeast-1", Label: "Singapore"},
	},
	ProviderGCP: {
		{Code: "us-east1", Label: "South Carolina"},
	},
	ProviderAzure: {
		{Code: "centralus", Label: "Central US"},
	},
}

// filesystemOnly marks placements that serve TiDB Cloud Filesystem without a
// TiDB Cloud Starter placement. TiDB Cloud Starter resolution rejects them
// through Validate, while Filesystem resolution accepts them through
// ValidateFilesystem; the built-in Filesystem endpoint mapping remains the
// authority on whether a Filesystem endpoint exists for a placement.
var filesystemOnly = map[string]struct{}{
	placementKey(ProviderAzure, "centralus"): {},
}

func placementKey(provider, regionCode string) string {
	return provider + "\x00" + regionCode
}

var providerPrefixes = map[string]string{
	ProviderPrefixAWS:                ProviderAWS,
	ProviderPrefixAlibabaCloud:       ProviderAlibabaCloud,
	ProviderPrefixAlibabaCloudLegacy: ProviderAlibabaCloud,
	ProviderPrefixGCP:                ProviderGCP,
	ProviderPrefixAzure:              ProviderAzure,
}

var providerToPrefix = map[string]string{
	ProviderAWS:          ProviderPrefixAWS,
	ProviderAlibabaCloud: ProviderPrefixAlibabaCloud,
	ProviderGCP:          ProviderPrefixGCP,
	ProviderAzure:        ProviderPrefixAzure,
}

func Providers() []string {
	providers := make([]string, 0, len(supported))
	for provider := range supported {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func Regions(provider string) ([]Region, error) {
	regions, ok := supported[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported cloud provider %q", provider)
	}
	return append([]Region(nil), regions...), nil
}

func Placements() []Placement {
	placements := make([]Placement, 0)
	for provider, regions := range supported {
		prefix := providerToPrefix[provider]
		for _, r := range regions {
			placements = append(placements, Placement{
				Code:        prefix + "-" + r.Code,
				Prefix:      prefix,
				Provider:    provider,
				NativeCode:  r.Code,
				RegionLabel: r.Label,
			})
		}
	}
	sort.Slice(placements, func(i, j int) bool {
		return placements[i].Code < placements[j].Code
	})
	return placements
}

func DefaultPlacementCode() string {
	return ProviderPrefixAWS + "-us-east-1"
}

func DefaultRegion(provider string) (string, error) {
	regions, err := Regions(provider)
	if err != nil {
		return "", err
	}
	return regions[0].Code, nil
}

// CanonicalCode returns the canonical placement code for an accepted
// provider/region pair. It accepts every placement ti can address, including
// Filesystem-only placements such as azure-centralus.
func CanonicalCode(provider, nativeCode string) (string, error) {
	if err := validatePlacement(provider, nativeCode); err != nil {
		return "", err
	}
	prefix, ok := providerToPrefix[provider]
	if !ok {
		return "", fmt.Errorf("unsupported cloud provider %q; supported providers: %s", provider, joinProviders())
	}
	return prefix + "-" + nativeCode, nil
}

func ParsePlacementCode(code string) (Placement, error) {
	code = strings.TrimSpace(code)
	prefix, nativeCode, ok := strings.Cut(code, "-")
	if !ok || prefix == "" || nativeCode == "" {
		return Placement{}, fmt.Errorf("unsupported region code %q; expected values such as %s", code, DefaultPlacementCode())
	}
	provider, ok := providerPrefixes[prefix]
	if !ok {
		return Placement{}, fmt.Errorf("unsupported cloud provider prefix %q in region code %q; supported prefixes: %s", prefix, code, joinProviderPrefixes())
	}
	if err := validatePlacement(provider, nativeCode); err != nil {
		return Placement{}, err
	}
	regionLabel := ""
	if regions, _ := Regions(provider); len(regions) > 0 {
		for _, region := range regions {
			if region.Code == nativeCode {
				regionLabel = region.Label
				break
			}
		}
	}
	return Placement{
		Code:        providerToPrefix[provider] + "-" + nativeCode,
		Prefix:      providerToPrefix[provider],
		Provider:    provider,
		NativeCode:  nativeCode,
		RegionLabel: regionLabel,
	}, nil
}

// Validate reports whether provider/regionCode is a supported TiDB Cloud
// Starter placement. A placement that only serves TiDB Cloud Filesystem is
// rejected with an explicit Filesystem-only error instead of being reported as
// unknown.
func Validate(provider, regionCode string) error {
	if err := validatePlacement(provider, regionCode); err != nil {
		return err
	}
	if _, ok := filesystemOnly[placementKey(provider, regionCode)]; ok {
		return fmt.Errorf("placement %q serves TiDB Cloud Filesystem only, not TiDB Cloud Starter", providerToPrefix[provider]+"-"+regionCode)
	}
	return nil
}

// ValidateFilesystem reports whether provider/regionCode can be routed to a
// TiDB Cloud Filesystem endpoint. Whether an endpoint exists for the placement
// is decided by the built-in Filesystem region mapping.
func ValidateFilesystem(provider, regionCode string) error {
	return validatePlacement(provider, regionCode)
}

func validatePlacement(provider, regionCode string) error {
	regions, ok := supported[provider]
	if !ok {
		return fmt.Errorf("unsupported cloud provider %q; supported providers: %s", provider, joinProviders())
	}
	for _, region := range regions {
		if region.Code == regionCode {
			return nil
		}
	}
	return fmt.Errorf("unsupported region %q for cloud provider %q; supported regions: %s", regionCode, provider, joinRegions(regions))
}

func joinProviders() string {
	providers := Providers()
	if len(providers) == 0 {
		return ""
	}
	out := providers[0]
	for _, provider := range providers[1:] {
		out += ", " + provider
	}
	return out
}

func joinProviderPrefixes() string {
	prefixes := make([]string, 0, len(providerPrefixes))
	for prefix := range providerPrefixes {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	return strings.Join(prefixes, ", ")
}

func joinRegions(regions []Region) string {
	if len(regions) == 0 {
		return ""
	}
	codes := make([]string, 0, len(regions))
	for _, region := range regions {
		codes = append(codes, region.Code)
	}
	sort.Strings(codes)
	out := codes[0]
	for _, code := range codes[1:] {
		out += ", " + code
	}
	return out
}
