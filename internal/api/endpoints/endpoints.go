package endpoints

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/fsregion"
	"github.com/tidbcloud/ti-cli/internal/version"
)

type Service string

const (
	ServiceStarter Service = "starter"
	ServiceIAM     Service = "iam"
	ServiceFS      Service = "fs"
)

const (
	DefaultStarterBaseURL = "https://serverless.tidbapi.com"
	DefaultIAMBaseURL     = "https://iam.tidbapi.com"
)

type ProviderRegion struct {
	Provider string
	Region   string
}

type Endpoint struct {
	Service     Service `json:"service"`
	BaseURL     string  `json:"base_url"`
	Provider    string  `json:"provider,omitempty"`
	APIProvider string  `json:"api_provider,omitempty"`
	RegionCode  string  `json:"region_code,omitempty"`
	RegionName  string  `json:"region_name,omitempty"`
}

type Resolver struct {
	StarterBaseURL string
	IAMBaseURL     string

	// FSRegions is a preloaded catalog snapshot. Tests inject it to keep
	// endpoint resolution offline; production leaves it nil and requests the
	// published catalog.
	FSRegions *fsregion.Catalog
	// FSRegionsBaseURL overrides the catalog endpoint. It is only set by the
	// hidden TI_TEST_FS_REGIONS_URL control.
	FSRegionsBaseURL    string
	FSRegionsHTTPClient *http.Client
	// Debug receives debug transport output for Filesystem endpoint discovery.
	Debug       bool
	DebugWriter io.Writer
}

// testControlsAllowed gates the TI_TEST_* endpoint overrides. It is a variable
// so tests, whose binaries are not local builds, can turn the controls on.
var testControlsAllowed = func() bool { return version.Current().TestControlsAllowed() }

func NewResolver() Resolver {
	resolver := Resolver{
		StarterBaseURL: DefaultStarterBaseURL,
		IAMBaseURL:     DefaultIAMBaseURL,
	}
	if testControlsAllowed() {
		if baseURL := strings.TrimSpace(os.Getenv("TI_TEST_STARTER_BASE_URL")); baseURL != "" {
			resolver.StarterBaseURL = baseURL
		}
		if baseURL := strings.TrimSpace(os.Getenv("TI_TEST_IAM_BASE_URL")); baseURL != "" {
			resolver.IAMBaseURL = baseURL
		}
		if regionsURL := strings.TrimSpace(os.Getenv("TI_TEST_FS_REGIONS_URL")); regionsURL != "" {
			resolver.FSRegionsBaseURL = regionsURL
		}
	}
	return resolver
}

func (r Resolver) IsZero() bool {
	return r.StarterBaseURL == "" &&
		r.IAMBaseURL == "" &&
		r.FSRegions == nil &&
		r.FSRegionsBaseURL == "" &&
		r.FSRegionsHTTPClient == nil &&
		r.DebugWriter == nil &&
		!r.Debug
}

func (r Resolver) ResolveStarter(provider, regionCode string) (Endpoint, error) {
	if err := region.Validate(provider, regionCode); err != nil {
		return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	baseURL := r.StarterBaseURL
	if baseURL == "" {
		baseURL = DefaultStarterBaseURL
	}
	if err := validateBaseURL(baseURL); err != nil {
		return Endpoint{}, err
	}
	apiProvider := APIProvider(provider)
	return Endpoint{
		Service:     ServiceStarter,
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Provider:    provider,
		APIProvider: apiProvider,
		RegionCode:  regionCode,
		RegionName:  "regions/" + apiProvider + "-" + regionCode,
	}, nil
}

func (r Resolver) ResolveIAM() (Endpoint, error) {
	baseURL := r.IAMBaseURL
	if baseURL == "" {
		baseURL = DefaultIAMBaseURL
	}
	if err := validateBaseURL(baseURL); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		Service: ServiceIAM,
		BaseURL: strings.TrimRight(baseURL, "/"),
	}, nil
}

// ResolveFSProfile selects the regional Filesystem endpoint for the placement
// recorded in profile. Control-plane commands use it: they need a region and may
// not have a File System credential yet, so the published Filesystem region
// catalog is authoritative.
func (r Resolver) ResolveFSProfile(profile *config.Profile) (Endpoint, error) {
	if profile == nil {
		return Endpoint{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	regionCode := profile.FSPlacementRegionCode
	if regionCode == "" && profile.FSCloudProvider != "" && profile.FSRegionCode != "" {
		code, err := region.CanonicalCode(profile.FSCloudProvider, profile.FSRegionCode)
		if err != nil {
			return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
		}
		regionCode = code
	}
	if regionCode == "" {
		regionCode = profile.PlacementRegionCode
	}
	return r.ResolveFSRegion(profile, regionCode)
}

// ResolveFSRegion selects the regional Filesystem endpoint for an explicit
// canonical region code using the published Filesystem region catalog.
func (r Resolver) ResolveFSRegion(profile *config.Profile, regionCode string) (Endpoint, error) {
	placement, err := region.ParseCanonicalCode(regionCode)
	if err != nil {
		return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	if profile == nil {
		return Endpoint{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required to resolve a ti fs region")
	}
	catalog, err := r.FetchFSRegions(context.Background(), profile)
	if err != nil {
		return Endpoint{}, err
	}
	entry, ok := catalog.Lookup(placement.Code)
	if !ok || !entry.Eligible() {
		return Endpoint{}, fsregion.UnpublishedRegionError(placement.Code, catalog)
	}
	if !entry.Routable() {
		// An unmapped cloud provider is listed but never routable: borrowing the
		// provider from the region-code prefix would route a request the catalog
		// did not authorize.
		return Endpoint{}, fsregion.UnsupportedProviderError(entry)
	}
	return fsEndpoint(entry.Provider, entry.RegionCode, entry.ServiceAddress)
}

// ResolveFSCredentialEndpoint selects the Filesystem endpoint recorded in the
// selected File System's local credentials. Data-plane, mount, Git, journal,
// vault, and Runtime execution use it: those commands must keep working without
// TiDB Cloud API credentials, and the recorded address is a stable property of
// the region.
//
// When the credential does not record an address (it was written before the
// region catalog existed) the conventional regional address is derived from the
// canonical region code. That fallback needs no network request and is what
// keeps token-only use working on a machine without TiDB Cloud API keys.
func (r Resolver) ResolveFSCredentialEndpoint(profile *config.Profile) (Endpoint, error) {
	if profile == nil {
		return Endpoint{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	regionCode, err := region.ParseCanonicalCode(profile.FSPlacementRegionCode)
	if err != nil {
		return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	address, addressErr := fsregion.NormalizeServiceAddress(profile.FSServiceAddress)
	if addressErr != nil || strings.TrimSpace(profile.FSServiceAddress) == "" {
		derived, deriveErr := fsregion.DeriveServiceAddress(regionCode.Code)
		if deriveErr != nil {
			return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, "cannot derive the ti fs service address", deriveErr)
		}
		address = derived
	}
	provider := profile.FSCloudProvider
	if provider == "" {
		provider = regionCode.Provider
	}
	return fsEndpoint(provider, regionCode.Code, address)
}

// FetchFSRegions reads the published Filesystem region catalog for profile. It
// always requests the API: there is no cached catalog.
func (r Resolver) FetchFSRegions(ctx context.Context, profile *config.Profile) (fsregion.Catalog, error) {
	if r.FSRegions != nil {
		catalog := *r.FSRegions
		catalog.Entries = append([]fsregion.Entry(nil), r.FSRegions.Entries...)
		return catalog, nil
	}
	credentials := fsregion.Credentials{}
	if profile != nil {
		credentials = fsregion.Credentials{
			PublicKey:  profile.TiDBCloudPublicKey,
			PrivateKey: profile.TiDBCloudPrivateKey,
		}
	}
	client := fsregion.Client{
		BaseURL:     r.FSRegionsBaseURL,
		HTTPClient:  r.FSRegionsHTTPClient,
		Credentials: credentials,
	}
	if r.Debug {
		client.Debug = r.DebugWriter
	}
	catalog, err := client.Fetch(ctx)
	if err != nil {
		if !credentials.Complete() {
			return fsregion.Catalog{}, fsregion.MissingCatalogError(err)
		}
		return fsregion.Catalog{}, err
	}
	return catalog, nil
}

func fsEndpoint(provider, regionCode, baseURL string) (Endpoint, error) {
	if err := validateBaseURL(baseURL); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		Service:     ServiceFS,
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Provider:    provider,
		APIProvider: APIProvider(provider),
		RegionCode:  regionCode,
		RegionName:  regionCode,
	}, nil
}

func APIProvider(provider string) string {
	switch provider {
	case region.ProviderAlibabaCloud:
		return "alicloud"
	default:
		return provider
	}
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return apperr.Wrap("api.invalid_endpoint", "config", 2, fmt.Sprintf("invalid internal endpoint %q", value), err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return apperr.New("api.invalid_endpoint", "config", 2, fmt.Sprintf("invalid internal endpoint %q: scheme must be https or http", value))
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return apperr.New("api.invalid_endpoint", "config", 2, fmt.Sprintf("invalid internal endpoint %q: plain http is allowed only for localhost", value))
	}
	return nil
}

// isLoopbackHost reports whether host is localhost or a loopback IP address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
