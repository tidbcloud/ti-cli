package region

import (
	"fmt"
	"regexp"
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
// through Validate. Whether a Filesystem endpoint exists for a placement is not
// decided here: the Filesystem region catalog published by the backend is the
// authority.
var filesystemOnly = map[string]struct{}{
	placementKey(ProviderGCP, "us-east1"):    {},
	placementKey(ProviderAzure, "centralus"): {},
}

// nativeCodePattern is the structural shape of the native part of a canonical
// region code. It is deliberately independent of the Starter whitelist: the
// Filesystem region catalog decides which of these codes it publishes.
var nativeCodePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

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

// CanonicalCode returns the canonical placement code for a provider and native
// region code. It validates the pair structurally only: whether the placement
// is offered by TiDB Cloud Starter is decided by Validate, and whether it is
// offered by TiDB Cloud Filesystem is decided by the published Filesystem
// region catalog.
func CanonicalCode(provider, nativeCode string) (string, error) {
	placement, err := ParseCanonicalCode(providerToPrefixValue(provider) + "-" + strings.TrimSpace(nativeCode))
	if err != nil {
		return "", err
	}
	if placement.Provider != provider {
		return "", fmt.Errorf("unsupported cloud provider %q; supported providers: %s", provider, joinProviders())
	}
	return placement.Code, nil
}

// ParseCanonicalCode parses a canonical region code into its provider and
// native parts without consulting any whitelist. It rejects a missing
// separator, an empty part, an unknown provider prefix, or a native part that
// does not match the canonical shape. Callers that need Starter semantics must
// additionally call Validate, and callers that need Filesystem semantics must
// look the code up in the Filesystem region catalog.
func ParseCanonicalCode(code string) (Placement, error) {
	raw := strings.TrimSpace(code)
	code = strings.ToLower(raw)
	prefix, nativeCode, ok := strings.Cut(code, "-")
	if !ok || prefix == "" || nativeCode == "" {
		return Placement{}, fmt.Errorf("unsupported region code %q; expected values such as %s", raw, DefaultPlacementCode())
	}
	provider, ok := providerPrefixes[prefix]
	if !ok {
		return Placement{}, fmt.Errorf("unsupported cloud provider prefix %q in region code %q; supported prefixes: %s", prefix, raw, joinProviderPrefixes())
	}
	if len(nativeCode) > 63 || !nativeCodePattern.MatchString(nativeCode) {
		return Placement{}, fmt.Errorf("unsupported region code %q; expected values such as %s", raw, DefaultPlacementCode())
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

// ProviderForAPI maps a TiDB Cloud API cloud provider value, which is also the
// canonical region prefix, to the internal provider name.
func ProviderForAPI(apiProvider string) (string, bool) {
	provider, ok := providerPrefixes[strings.ToLower(strings.TrimSpace(apiProvider))]
	return provider, ok
}

func providerToPrefixValue(provider string) string {
	provider = strings.TrimSpace(provider)
	if prefix, ok := providerToPrefix[provider]; ok {
		return prefix
	}
	return provider
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
