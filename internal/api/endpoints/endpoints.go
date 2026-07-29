package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config/region"
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
	DefaultFSManifestURL  = "https://drive9.ai/manifest/regions/drive9-regions.json"
	DefaultFSMode         = "tidb_cloud_native"
)

var errFSManifestUnavailable = errors.New("tdc fs region manifest unavailable")

const (
	fsManifestFetchAttempts = 3
	fsManifestRetryDelay    = 200 * time.Millisecond
	fsManifestCacheMaxAge   = 24 * time.Hour
)

type cachedFSRegionManifest struct {
	ManifestURL string           `json:"manifest_url"`
	FetchedAt   time.Time        `json:"fetched_at"`
	Manifest    FSRegionManifest `json:"manifest"`
}

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
	StarterBaseURL       string
	IAMBaseURL           string
	FSBaseURLs           map[ProviderRegion]string
	FSManifestURL        string
	FSMode               string
	FSManifest           *FSRegionManifest
	FSManifestHTTPClient *http.Client
}

func NewResolver() Resolver {
	resolver := Resolver{
		StarterBaseURL: DefaultStarterBaseURL,
		IAMBaseURL:     DefaultIAMBaseURL,
		FSManifestURL:  DefaultFSManifestURL,
		FSMode:         DefaultFSMode,
	}
	if os.Getenv("TDC_ALLOW_TEST_ENDPOINTS") == "1" {
		if baseURL := strings.TrimSpace(os.Getenv("TDC_TEST_STARTER_BASE_URL")); baseURL != "" {
			resolver.StarterBaseURL = baseURL
		}
		if baseURL := strings.TrimSpace(os.Getenv("TDC_TEST_IAM_BASE_URL")); baseURL != "" {
			resolver.IAMBaseURL = baseURL
		}
		if manifestURL := strings.TrimSpace(os.Getenv("TDC_TEST_FS_MANIFEST_URL")); manifestURL != "" {
			resolver.FSManifestURL = manifestURL
		}
	}
	return resolver
}

func (r Resolver) IsZero() bool {
	return r.StarterBaseURL == "" &&
		r.IAMBaseURL == "" &&
		r.FSBaseURLs == nil &&
		r.FSManifestURL == "" &&
		r.FSMode == "" &&
		r.FSManifest == nil &&
		r.FSManifestHTTPClient == nil
}

func (r Resolver) Resolve(service Service, provider, regionCode string) (Endpoint, error) {
	switch service {
	case ServiceStarter:
		return r.ResolveStarter(provider, regionCode)
	case ServiceIAM:
		return r.ResolveIAM()
	case ServiceFS:
		return r.ResolveFS(provider, regionCode)
	default:
		return Endpoint{}, apperr.New("api.unknown_service", "usage", 2, fmt.Sprintf("unknown API service %q", service))
	}
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

func (r Resolver) ResolveFS(provider, regionCode string) (Endpoint, error) {
	if err := region.Validate(provider, regionCode); err != nil {
		return Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}

	if r.FSBaseURLs != nil {
		baseURL := r.FSBaseURLs[ProviderRegion{Provider: provider, Region: regionCode}]
		if baseURL != "" {
			return fsEndpoint(provider, regionCode, "", baseURL)
		}
	}

	mode := strings.TrimSpace(r.FSMode)
	if mode == "" {
		mode = DefaultFSMode
	}
	manifest, err := r.fsManifest()
	if err != nil {
		return Endpoint{}, err
	}
	entry, err := selectFSManifestEntry(manifest.Regions, provider, regionCode, mode)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint, err := fsEndpoint(provider, regionCode, entry.RegionCode, entry.ServerURL)
	if err != nil {
		return Endpoint{}, err
	}
	return endpoint, nil
}

type FSRegionManifest struct {
	Service string                   `json:"service"`
	Default *FSRegionManifestDefault `json:"default,omitempty"`
	Regions []FSRegionManifestEntry  `json:"regions"`
}

type FSRegionManifestDefault struct {
	RegionCode string `json:"region_code"`
	Mode       string `json:"mode"`
}

type FSRegionManifestEntry struct {
	RegionCode    string            `json:"region_code"`
	Mode          string            `json:"mode"`
	ServerURL     string            `json:"server_url"`
	CloudProvider string            `json:"cloud_provider,omitempty"`
	TiDBRegion    string            `json:"tidb_region,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func (r Resolver) fsManifest() (*FSRegionManifest, error) {
	if r.FSManifest != nil {
		manifest := *r.FSManifest
		manifest.Regions = append([]FSRegionManifestEntry(nil), r.FSManifest.Regions...)
		if r.FSManifest.Default != nil {
			defaultEntry := *r.FSManifest.Default
			manifest.Default = &defaultEntry
		}
		if err := validateFSManifest(&manifest); err != nil {
			return nil, err
		}
		return &manifest, nil
	}
	manifestURL := strings.TrimSpace(r.FSManifestURL)
	if manifestURL == "" {
		manifestURL = DefaultFSManifestURL
	}
	return fetchFSManifest(context.Background(), manifestURL, r.FSManifestHTTPClient)
}

func fetchFSManifest(ctx context.Context, manifestURL string, client *http.Client) (*FSRegionManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}

	var lastErr error
	for attempt := 0; attempt < fsManifestFetchAttempts; attempt++ {
		manifest, retry, err := fetchFSManifestOnce(ctx, manifestURL, client)
		if err == nil {
			storeCachedFSManifest(manifestURL, manifest)
			return manifest, nil
		}
		lastErr = err
		if !retry || attempt == fsManifestFetchAttempts-1 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * fsManifestRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			lastErr = apperr.Wrap("api.fs_manifest_unavailable", "api", 1, fmt.Sprintf("%s: fetch %s", errFSManifestUnavailable, manifestURL), ctx.Err())
			if cached, cacheErr := loadCachedFSManifest(manifestURL); cacheErr == nil {
				return cached, nil
			}
			return nil, lastErr
		case <-timer.C:
		}
	}
	if cached, cacheErr := loadCachedFSManifest(manifestURL); cacheErr == nil {
		return cached, nil
	}
	return nil, lastErr
}

func fetchFSManifestOnce(ctx context.Context, manifestURL string, client *http.Client) (*FSRegionManifest, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, false, apperr.Wrap("api.fs_manifest_request", "api", 1, "build tdc fs region manifest request", err)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, true, apperr.Wrap("api.fs_manifest_unavailable", "api", 1, fmt.Sprintf("%s: fetch %s", errFSManifestUnavailable, manifestURL), err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, retryableFSManifestStatus(res.StatusCode), apperr.New("api.fs_manifest_unavailable", "api", 1, fmt.Sprintf("%s: fetch %s returned HTTP %d", errFSManifestUnavailable, manifestURL, res.StatusCode))
	}
	var manifest FSRegionManifest
	if err := json.NewDecoder(res.Body).Decode(&manifest); err != nil {
		return nil, true, apperr.Wrap("api.fs_manifest_decode", "api", 1, "decode tdc fs region manifest", err)
	}
	if err := validateFSManifest(&manifest); err != nil {
		return nil, false, err
	}
	return &manifest, false, nil
}

func retryableFSManifestStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func loadCachedFSManifest(manifestURL string) (*FSRegionManifest, error) {
	cachePath, err := fsManifestCachePath(manifestURL)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}
	var cached cachedFSRegionManifest
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}
	if cached.ManifestURL != manifestURL {
		return nil, fmt.Errorf("cached tdc fs manifest belongs to a different URL")
	}
	if cached.FetchedAt.IsZero() || time.Since(cached.FetchedAt) > fsManifestCacheMaxAge {
		return nil, fmt.Errorf("cached tdc fs manifest is expired")
	}
	manifest := cached.Manifest
	if err := validateFSManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func storeCachedFSManifest(manifestURL string, manifest *FSRegionManifest) {
	if manifest == nil {
		return
	}
	cachePath, err := fsManifestCachePath(manifestURL)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return
	}
	cached := cachedFSRegionManifest{
		ManifestURL: manifestURL,
		FetchedAt:   time.Now().UTC(),
		Manifest:    *manifest,
	}
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(cachePath, data, 0o644)
}

func fsManifestCachePath(manifestURL string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve tdc fs manifest cache home: %w", err)
	}
	sum := sha256.Sum256([]byte(manifestURL))
	name := fmt.Sprintf("fs-region-manifest-%x.json", sum[:8])
	return filepath.Join(home, ".tdc", "cache", name), nil
}

func validateFSManifest(manifest *FSRegionManifest) error {
	if manifest == nil {
		return apperr.New("api.fs_manifest_invalid", "api", 1, "tdc fs region manifest is required")
	}
	if len(manifest.Regions) == 0 {
		return apperr.New("api.fs_manifest_invalid", "api", 1, "tdc fs region manifest has no regions")
	}
	seen := map[string]int{}
	for i := range manifest.Regions {
		entry := &manifest.Regions[i]
		entry.RegionCode = strings.TrimSpace(entry.RegionCode)
		entry.Mode = strings.TrimSpace(entry.Mode)
		entry.ServerURL = strings.TrimSpace(entry.ServerURL)
		entry.CloudProvider = strings.TrimSpace(entry.CloudProvider)
		entry.TiDBRegion = strings.TrimSpace(entry.TiDBRegion)
		if entry.RegionCode == "" {
			return apperr.New("api.fs_manifest_invalid", "api", 1, fmt.Sprintf("tdc fs region manifest entry %d missing region_code", i))
		}
		if entry.Mode == "" {
			return apperr.New("api.fs_manifest_invalid", "api", 1, fmt.Sprintf("tdc fs region manifest entry %d missing mode", i))
		}
		if entry.ServerURL == "" {
			return apperr.New("api.fs_manifest_invalid", "api", 1, fmt.Sprintf("tdc fs region manifest entry %d missing server_url", i))
		}
		key := entry.RegionCode + "\x00" + entry.Mode
		if first, ok := seen[key]; ok {
			return apperr.New("api.fs_manifest_invalid", "api", 1, fmt.Sprintf("tdc fs region manifest entries %d and %d duplicate region_code %q mode %q", first, i, entry.RegionCode, entry.Mode))
		}
		seen[key] = i
	}
	if manifest.Default != nil {
		manifest.Default.RegionCode = strings.TrimSpace(manifest.Default.RegionCode)
		manifest.Default.Mode = strings.TrimSpace(manifest.Default.Mode)
		if manifest.Default.RegionCode == "" || manifest.Default.Mode == "" {
			return apperr.New("api.fs_manifest_invalid", "api", 1, "tdc fs region manifest default must include region_code and mode")
		}
		if _, ok := seen[manifest.Default.RegionCode+"\x00"+manifest.Default.Mode]; !ok {
			return apperr.New("api.fs_manifest_invalid", "api", 1, fmt.Sprintf("tdc fs region manifest default %q/%q not found in regions", manifest.Default.RegionCode, manifest.Default.Mode))
		}
	}
	return nil
}

func selectFSManifestEntry(entries []FSRegionManifestEntry, provider, regionCode, mode string) (FSRegionManifestEntry, error) {
	apiProvider := APIProvider(provider)
	mode = strings.TrimSpace(mode)
	matches := make([]FSRegionManifestEntry, 0, 1)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Mode) != mode {
			continue
		}
		if fsManifestEntryMatches(entry, provider, apiProvider, regionCode) {
			matches = append(matches, entry)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return FSRegionManifestEntry{}, apperr.New(
			"api.fs_endpoint_unsupported",
			"config",
			2,
			fmt.Sprintf("tdc fs is not available for %s/%s in mode %s; supported tdc fs regions: %s", provider, regionCode, mode, supportedFSRegions(entries, mode)),
		)
	default:
		return FSRegionManifestEntry{}, apperr.New(
			"api.fs_endpoint_ambiguous",
			"api",
			1,
			fmt.Sprintf("tdc fs region manifest has multiple endpoints for %s/%s in mode %s", provider, regionCode, mode),
		)
	}
}

func fsManifestEntryMatches(entry FSRegionManifestEntry, provider, apiProvider, regionCode string) bool {
	if entry.CloudProvider != "" || entry.TiDBRegion != "" {
		return entry.CloudProvider == apiProvider && entry.TiDBRegion == regionCode
	}
	return entry.RegionCode == fsRegionCode(provider, regionCode)
}

func fsRegionCode(provider, regionCode string) string {
	prefix := APIProvider(provider)
	if provider == region.ProviderAlibabaCloud {
		prefix = "ali"
	}
	return prefix + "-" + regionCode
}

func supportedFSRegions(entries []FSRegionManifestEntry, mode string) string {
	values := make([]string, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Mode) != mode {
			continue
		}
		provider := entry.CloudProvider
		if provider == "" {
			provider = strings.SplitN(entry.RegionCode, "-", 2)[0]
		}
		regionCode := entry.TiDBRegion
		if regionCode == "" {
			regionCode = strings.TrimPrefix(entry.RegionCode, provider+"-")
		}
		value := provider + "/" + regionCode
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	sort.Strings(values)
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func fsEndpoint(provider, regionCode, regionName, baseURL string) (Endpoint, error) {
	if err := validateBaseURL(baseURL); err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		Service:     ServiceFS,
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Provider:    provider,
		APIProvider: APIProvider(provider),
		RegionCode:  regionCode,
		RegionName:  regionName,
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
	return nil
}
