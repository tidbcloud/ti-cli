package fsregion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
)

const (
	catalogPageSize    = 100
	catalogMaxPages    = 20
	catalogTimeout     = 15 * time.Second
	catalogReadMaxByte = 8 << 20
)

// Credentials are the TiDB Cloud API keys used for the catalog request. The
// profile name is only used to phrase authentication and authorization failures.
type Credentials struct {
	ProfileName string
	PublicKey   string
	PrivateKey  string
}

// Complete reports whether both TiDB Cloud API keys are available.
func (c Credentials) Complete() bool {
	return strings.TrimSpace(c.PublicKey) != "" && strings.TrimSpace(c.PrivateKey) != ""
}

// Client fetches the published Filesystem region catalog. It owns no local
// state: every call observes the published state, and nothing is cached.
type Client struct {
	BaseURL     string
	HTTPClient  *http.Client
	Credentials Credentials
	Debug       io.Writer
}

type catalogPage struct {
	Regions       []wireRegion `json:"regions"`
	TotalSize     int          `json:"totalSize"`
	NextPageToken string       `json:"nextPageToken"`
}

type wireRegion struct {
	Name           string `json:"name"`
	RegionID       string `json:"regionId"`
	CloudProvider  string `json:"cloudProvider"`
	DisplayName    string `json:"displayName"`
	ServiceAddress string `json:"serviceAddress"`
	State          string `json:"state"`
}

// Fetch reads every published region from the Filesystem open API.
func (c Client) Fetch(ctx context.Context) (Catalog, error) {
	if !c.Credentials.Complete() {
		return Catalog{}, apperr.New(
			"fs.region_catalog_unavailable",
			"config",
			2,
			"the Filesystem region catalog requires TiDB Cloud API credentials",
		)
	}
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	// This request carries the organization API key pair, so the endpoint itself
	// must be a secure origin, and a rejected endpoint is described without the
	// parts that could carry secrets.
	parsedBase, err := parseSecureOrigin(baseURL)
	if err != nil {
		return Catalog{}, apperr.New("api.fs_regions_request", "config", 2, describeRejectedOrigin("Filesystem region catalog endpoint", parsedBase, err))
	}
	endpoint := strings.TrimRight(parsedBase.String(), "/") + RegionsPath

	client := c.httpClient()
	entries := make([]Entry, 0, catalogPageSize)
	seenTokens := map[string]struct{}{}
	pageToken := ""
	page := 0
	for {
		page++
		if page > catalogMaxPages {
			return Catalog{}, apperr.New("api.fs_regions_invalid", "api", 1, fmt.Sprintf("Filesystem region catalog returned more than %d pages", catalogMaxPages))
		}
		requestURL := endpoint + "?" + catalogQuery(pageToken).Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return Catalog{}, apperr.Wrap("api.fs_regions_request", "api", 1, "build the Filesystem region catalog request", err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "ti fs region catalog")
		response, err := client.Do(request)
		if err != nil {
			return Catalog{}, apperr.Wrap("api.fs_regions_unavailable", "api", 1, "the Filesystem region catalog is unavailable", err)
		}
		pageData, readErr := c.readCatalogPage(request, response)
		response.Body.Close()
		if readErr != nil {
			return Catalog{}, readErr
		}
		for _, wire := range pageData.Regions {
			entry, entryErr := entryFromWire(wire)
			if entryErr != nil {
				return Catalog{}, entryErr
			}
			entries = append(entries, entry)
		}
		next := strings.TrimSpace(pageData.NextPageToken)
		if next == "" {
			break
		}
		if _, ok := seenTokens[next]; ok {
			return Catalog{}, apperr.New("api.fs_regions_invalid", "api", 1, "Filesystem region catalog repeated a page token")
		}
		seenTokens[next] = struct{}{}
		pageToken = next
	}
	return Normalize(entries)
}

func (c Client) httpClient() *http.Client {
	var base http.RoundTripper = http.DefaultTransport
	if c.HTTPClient != nil {
		if c.HTTPClient.Transport != nil {
			base = c.HTTPClient.Transport
		}
	}
	redactor := apitransport.Redactor{Secrets: []string{c.Credentials.PublicKey, c.Credentials.PrivateKey}}
	transport := apitransport.NewDebugRoundTripper(base, c.Debug, redactor)
	transport = apitransport.NewDigest(c.Credentials.PublicKey, c.Credentials.PrivateKey, transport)

	// The catalog request is Digest-authenticated, and the digest transport answers
	// any 401 challenge it receives, so a redirect to another origin would hand the
	// organization API key pair to that host. The same-origin rule is composed under
	// any caller-supplied policy: a caller can narrow redirects further, never widen
	// them past this origin check.
	client := &http.Client{Transport: transport, Timeout: catalogTimeout}
	if c.HTTPClient != nil {
		caller := *c.HTTPClient
		if caller.Timeout == 0 {
			caller.Timeout = catalogTimeout
		}
		client = &caller
		client.Transport = transport
	}
	client.CheckRedirect = apitransport.SameOriginThen(client.CheckRedirect)
	return client
}

func catalogQuery(pageToken string) url.Values {
	query := url.Values{}
	query.Set("pageSize", fmt.Sprintf("%d", catalogPageSize))
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	return query
}

func (c Client) readCatalogPage(request *http.Request, response *http.Response) (catalogPage, error) {
	// A refused cross-origin redirect was never followed: report the origin change
	// instead of a generic catalog failure so the hardening is visible. A same-origin
	// redirect that a caller policy refused keeps its normal status handling.
	if fromOrigin, toOrigin, crossOrigin := apitransport.CrossOriginRedirect(response, request); crossOrigin {
		return catalogPage{}, apperr.New(
			"api.fs_regions_redirect_refused",
			"api",
			1,
			fmt.Sprintf("refused a Filesystem region catalog redirect from %s to %s: ti follows redirects only within the same scheme and host", fromOrigin, toOrigin),
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The catalog is a Digest-authenticated TiDB Cloud control-plane
		// endpoint, so credential, authorization, and not-found failures keep the
		// documented exit-code contract instead of collapsing into exit 1.
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return catalogPage{}, auth.InvalidCredentials(c.Credentials.ProfileName).AsError()
		case http.StatusForbidden:
			return catalogPage{}, auth.PermissionDenied(c.Credentials.ProfileName, authz.FSRegionRead, "read the Filesystem region catalog").AsError()
		case http.StatusNotFound:
			return catalogPage{}, auth.NotFound(http.MethodGet, RegionsPath).AsError()
		}
		return catalogPage{}, apperr.New(
			"api.fs_regions_unavailable",
			"api",
			1,
			fmt.Sprintf("the Filesystem region catalog returned HTTP %d", response.StatusCode),
		)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, catalogReadMaxByte))
	if err != nil {
		return catalogPage{}, apperr.Wrap("api.fs_regions_unavailable", "api", 1, "read the Filesystem region catalog response", err)
	}
	var pageData catalogPage
	if err := json.Unmarshal(body, &pageData); err != nil {
		return catalogPage{}, apperr.Wrap("api.fs_regions_decode", "api", 1, "decode the Filesystem region catalog response", err)
	}
	return pageData, nil
}

func entryFromWire(wire wireRegion) (Entry, error) {
	return normalizeEntry(Entry{
		RegionCode:     wire.RegionID,
		CloudProvider:  wire.CloudProvider,
		DisplayName:    wire.DisplayName,
		ServiceAddress: wire.ServiceAddress,
		State:          wire.State,
	})
}
