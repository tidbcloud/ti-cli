// Package fsregion owns the TiDB Cloud Filesystem region catalog: the
// authoritative list of published Filesystem regions, their cloud providers,
// and their regional service addresses.
//
// The catalog replaces the compiled-in Filesystem region mapping that ti used
// to ship. It is fetched from the TiDB Cloud Filesystem open API on demand, holds
// only public placement metadata, and is never cached locally: a command that
// resolves through the catalog always observes the published state.
package fsregion

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/region"
)

const (
	// DefaultBaseURL is the TiDB Cloud Filesystem control-plane host that
	// publishes the region catalog.
	DefaultBaseURL = "https://fs.tidbapi.com"

	// RegionsPath is the region catalog resource.
	RegionsPath = "/v1beta2/regions"

	// ActiveState is the only region state ti is allowed to route to.
	ActiveState = "ACTIVE"
)

// Entry is one published Filesystem region.
type Entry struct {
	RegionCode     string `json:"region_code"`
	CloudProvider  string `json:"cloud_provider"`
	DisplayName    string `json:"display_name"`
	ServiceAddress string `json:"service_address"`
	State          string `json:"state"`

	// Provider is the internal provider name derived from CloudProvider. It is
	// empty when the backend reports a cloud provider ti does not map yet.
	Provider string `json:"-"`
}

// Eligible reports whether ti may route a command to this region.
func (e Entry) Eligible() bool {
	return strings.EqualFold(strings.TrimSpace(e.State), ActiveState)
}

// Catalog is an immutable, validated snapshot of published Filesystem regions.
type Catalog struct {
	Entries []Entry
}

// Lookup returns the entry whose canonical region code matches regionCode.
func (c Catalog) Lookup(regionCode string) (Entry, bool) {
	code := strings.ToLower(strings.TrimSpace(regionCode))
	for _, entry := range c.Entries {
		if entry.RegionCode == code {
			return entry, true
		}
	}
	return Entry{}, false
}

// RoutableRegionCodes returns the sorted region codes ti may route to: eligible
// entries whose cloud provider ti can map. An eligible entry with an unmapped
// provider stays visible in list output but is never routable, because routing
// it would silently borrow a provider mapping from the region-code prefix.
func (c Catalog) RoutableRegionCodes() []string {
	codes := make([]string, 0, len(c.Entries))
	for _, entry := range c.Entries {
		if entry.Eligible() && entry.Provider != "" {
			codes = append(codes, entry.RegionCode)
		}
	}
	sort.Strings(codes)
	return codes
}

// Routable reports whether ti may route to this entry.
func (e Entry) Routable() bool {
	return e.Eligible() && e.Provider != ""
}

// Normalize validates and canonicalizes a catalog. Unknown cloud providers are
// preserved with an empty Provider so that list output stays faithful; every
// other contract violation is an error.
func Normalize(entries []Entry) (Catalog, error) {
	if len(entries) == 0 {
		return Catalog{}, invalid("the Filesystem region catalog is empty")
	}
	normalized := make([]Entry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		next, err := normalizeEntry(entry)
		if err != nil {
			return Catalog{}, err
		}
		if _, ok := seen[next.RegionCode]; ok {
			return Catalog{}, invalid(fmt.Sprintf("region catalog entry %d repeats region %q", i, next.RegionCode))
		}
		seen[next.RegionCode] = struct{}{}
		normalized = append(normalized, next)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].RegionCode < normalized[j].RegionCode })
	return Catalog{Entries: normalized}, nil
}

func normalizeEntry(entry Entry) (Entry, error) {
	regionCode := strings.ToLower(strings.TrimSpace(entry.RegionCode))
	placement, err := region.ParseCanonicalCode(regionCode)
	if err != nil {
		return Entry{}, invalid(err.Error())
	}
	if placement.Code != regionCode {
		return Entry{}, invalid(fmt.Sprintf("region catalog code %q is not canonical; want %q", regionCode, placement.Code))
	}
	cloudProvider := strings.ToLower(strings.TrimSpace(entry.CloudProvider))
	if cloudProvider == "" {
		return Entry{}, invalid(fmt.Sprintf("region catalog entry %q is missing cloud_provider", regionCode))
	}
	provider, known := region.ProviderForAPI(cloudProvider)
	if known && provider != placement.Provider {
		return Entry{}, invalid(fmt.Sprintf("region catalog entry %q declares cloud provider %q", regionCode, cloudProvider))
	}
	address, err := NormalizeServiceAddress(entry.ServiceAddress)
	if err != nil {
		return Entry{}, err
	}
	state := strings.TrimSpace(entry.State)
	if state == "" {
		return Entry{}, invalid(fmt.Sprintf("region catalog entry %q is missing state", regionCode))
	}
	return Entry{
		RegionCode:     regionCode,
		CloudProvider:  cloudProvider,
		DisplayName:    strings.TrimSpace(entry.DisplayName),
		ServiceAddress: address,
		State:          state,
		Provider:       provider,
	}, nil
}

// NormalizeServiceAddress validates a regional service address and returns it
// without a trailing slash. Service addresses are absolute https URLs: ti never
// contacts a plain-http Filesystem endpoint and never accepts a URL with a
// query, a fragment, or embedded credentials.
func NormalizeServiceAddress(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", invalid("the Filesystem region catalog entry is missing service_address")
	}
	parsed, err := parseSecureOrigin(trimmed)
	if err != nil {
		return "", invalid(describeRejectedOrigin("Filesystem service address", parsed, err))
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

// parseSecureOrigin parses a URL that ti will spend credentials on and enforces
// the origin rules shared by the catalog endpoint and published service
// addresses: an absolute https URL (plain http only for a loopback test server)
// with a host name and without embedded credentials, a query - including a bare
// trailing "?" - or a fragment.
//
// The returned URL is safe to describe in an error only through
// describeRejectedOrigin; the raw input may embed a password or token.
func parseSecureOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errOriginUnparsable
	}
	if parsed.Scheme == "" || parsed.Hostname() == "" {
		return parsed, errOriginNeedsHost
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return parsed, errOriginCarriesSecrets
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return parsed, errOriginInsecureScheme
	}
	return parsed, nil
}

var (
	errOriginUnparsable     = errors.New("it cannot be parsed as a URL")
	errOriginNeedsHost      = errors.New("it needs a scheme and a host name")
	errOriginCarriesSecrets = errors.New("credentials, query, and fragment are not allowed")
	errOriginInsecureScheme = errors.New("scheme must be https, and plain http is allowed only for localhost")
)

// describeRejectedOrigin phrases a rejected URL without echoing the parts that
// can carry secrets, so a rejected endpoint never prints a password or query
// token to stderr.
func describeRejectedOrigin(kind string, parsed *url.URL, reason error) string {
	// Only the scheme and host are echoed. Formatting the parsed URL, or any other
	// field of it, would echo secrets for inputs that do not parse as a hierarchical
	// URL: "https:user:secret@host" becomes Opaque, and a pasted token becomes Path.
	display := apitransport.Origin(parsed)
	if display == "" {
		return fmt.Sprintf("invalid %s: %v", kind, reason)
	}
	return fmt.Sprintf("invalid %s %q: %v", kind, display, reason)
}

// isLoopbackHost reports whether host is localhost or a loopback IP address.
// Only local fake Filesystem servers use plain http.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func invalid(message string) error {
	return apperr.New("api.fs_regions_invalid", "api", 1, "invalid Filesystem region catalog: "+message)
}

// MissingCatalogError reports that ti cannot learn which Filesystem regions
// exist: no catalog has been fetched and none can be fetched.
func MissingCatalogError(cause error) error {
	message := "ti cannot fetch the TiDB Cloud Filesystem region catalog; TiDB Cloud API credentials are required for that request"
	if cause != nil {
		return apperr.Wrap("fs.region_catalog_unavailable", "config", 2, message, cause)
	}
	return apperr.New("fs.region_catalog_unavailable", "config", 2, message)
}

// UnsupportedProviderError reports an active region whose cloud provider this ti
// release cannot map. It is listed by ti fs list-regions but cannot be routed.
func UnsupportedProviderError(entry Entry) error {
	return apperr.New(
		"api.fs_endpoint_unsupported",
		"config",
		2,
		fmt.Sprintf("ti fs region %q is published for cloud provider %q, which this ti release does not support", entry.RegionCode, entry.CloudProvider),
	)
}

// UnpublishedRegionError reports a canonical region code the catalog does not
// publish or does not currently activate.
func UnpublishedRegionError(regionCode string, catalog Catalog) error {
	message := "ti fs is not available for region " + regionCode
	if codes := catalog.RoutableRegionCodes(); len(codes) > 0 {
		message += "; available ti fs regions: " + strings.Join(codes, ", ")
	}
	if entry, ok := catalog.Lookup(regionCode); ok && !entry.Eligible() {
		message += "; region " + entry.RegionCode + " is " + strings.ToLower(entry.State) + ", not active"
	}
	return apperr.New("api.fs_endpoint_unsupported", "config", 2, message)
}

// ServiceAddressDomain is the Filesystem deployment domain. A published region's
// service address is normally derived from its canonical region code, and the
// catalog is authoritative whenever it is available.
const ServiceAddressDomain = ".drive9.ai"

// DeriveServiceAddress returns the conventional regional service address for a
// canonical region code, for example aws-us-east-1 ->
// https://aws-us-east-1.drive9.ai.
//
// This is the offline fallback that keeps token-only use working when local
// credentials predate the region catalog: it needs no network request and no
// TiDB Cloud API keys. A recorded catalog address always wins over it.
func DeriveServiceAddress(regionCode string) (string, error) {
	placement, err := region.ParseCanonicalCode(regionCode)
	if err != nil {
		return "", invalid(err.Error())
	}
	derived := "https://" + placement.Code + ServiceAddressDomain
	normalized, err := NormalizeServiceAddress(derived)
	if err != nil {
		return "", err
	}
	return normalized, nil
}
