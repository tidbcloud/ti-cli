package fscred

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/config/region"
	"github.com/tidbcloud/tdc/internal/config/store"
)

const (
	resourcesDirName = "fs_resources"
	configFileName   = "config"
	credsFileName    = "credentials"
)

type Resource struct {
	Name          string `json:"file_system_name" toml:"file_system_name"`
	TenantID      string `json:"tenant_id,omitempty" toml:"tenant_id"`
	CloudProvider string `json:"cloud_provider,omitempty" toml:"cloud_provider"`
	RegionCode    string `json:"region_code,omitempty" toml:"region_code"`
	CreatedAt     string `json:"created_at,omitempty" toml:"created_at,omitempty"`
	HasAPIKey     bool   `json:"has_api_key" toml:"-"`
	APIKey        string `json:"-" toml:"-"`
}

type credentials struct {
	APIKey string `toml:"api_key"`
}

type ListResult struct {
	Profile     string     `json:"profile"`
	FileSystems []Resource `json:"file_systems"`
}

type RegistryPaths struct {
	Config      string `json:"config"`
	Credentials string `json:"credentials"`
}

type ResolveAuthOptions struct {
	Selector         string
	SelectorExplicit bool
	Token            string
	TokenExplicit    bool
	RegionOverride   string
	TokenRequired    bool
	Env              map[string]string
	DryRun           bool
}

func FromProfile(profile *config.Profile) Resource {
	if profile == nil {
		return Resource{}
	}
	return Resource{
		Name:          profile.FSResourceName,
		TenantID:      profile.FSTenantID,
		CloudProvider: profile.FSCloudProvider,
		RegionCode:    profile.FSPlacementRegionCode,
		HasAPIKey:     strings.TrimSpace(profile.FSAPIKey) != "",
		APIKey:        profile.FSAPIKey,
	}
}

func Store(homeDir string, profile *config.Profile, resourceName, tenantID, cloudProvider, regionCode, apiKey string) error {
	if profile == nil {
		return apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	resourceName = strings.TrimSpace(resourceName)
	if resourceName == "" {
		return apperr.New("fs.missing_file_system_name", "usage", 2, "--file-system-name is required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return apperr.New("fs.missing_api_key", "api", 1, "tdc fs provision response did not include an api_key")
	}
	if strings.TrimSpace(tenantID) == "" {
		return apperr.New("fs.missing_tenant_id", "api", 1, "tdc fs provision response did not include a tenant_id")
	}
	canonicalCode, resolvedProvider, err := canonicalPlacement(profile, cloudProvider, regionCode)
	if err != nil {
		return err
	}
	if existing, err := Get(homeDir, profile.Name, resourceName); err == nil {
		if existing.TenantID != strings.TrimSpace(tenantID) || existing.APIKey != strings.TrimSpace(apiKey) {
			return resourceError("fs.resource_name_conflict", profile.Name, resourceName, "a different local tdc fs resource already uses this name")
		}
		return nil
	} else if apperr.CodeFor(err) != "fs.resource_not_found" {
		return err
	}
	resource := Resource{
		Name:          resourceName,
		TenantID:      strings.TrimSpace(tenantID),
		CloudProvider: resolvedProvider,
		RegionCode:    canonicalCode,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	dir, err := resourceDir(homeDir, profile.Name, resourceName)
	if err != nil {
		return err
	}
	if err := ensureRegistryDirs(homeDir, profile.Name, resourceName); err != nil {
		return fmt.Errorf("create tdc fs resource directory: %w", err)
	}
	if err := writeTOML(filepath.Join(dir, configFileName), resource, 0o644); err != nil {
		return err
	}
	if err := writeTOML(filepath.Join(dir, credsFileName), credentials{APIKey: strings.TrimSpace(apiKey)}, 0o600); err != nil {
		_ = os.Remove(filepath.Join(dir, configFileName))
		return err
	}
	return nil
}

func List(homeDir, profileName string) ([]Resource, error) {
	dir := profileDir(homeDir, profileName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Resource{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list tdc fs resources for profile %q: %w", normalizedProfile(profileName), err)
	}
	resources := make([]Resource, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name, err := decodeKey(entry.Name())
		if err != nil {
			return nil, resourceError("fs.resource_credentials_incomplete", profileName, entry.Name(), "invalid local resource directory")
		}
		resource, err := readResourceDir(filepath.Join(dir, entry.Name()), profileName, name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, resourceError("fs.resource_credentials_incomplete", profileName, name, "resource config and credentials must both exist")
			}
			return nil, err
		}
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Name < resources[j].Name })
	return resources, nil
}

func Get(homeDir, profileName, resourceName string) (Resource, error) {
	resourceName = strings.TrimSpace(resourceName)
	dir, err := resourceDir(homeDir, profileName, resourceName)
	if err != nil {
		return Resource{}, err
	}
	resource, err := readResourceDir(dir, profileName, resourceName)
	if errors.Is(err, os.ErrNotExist) {
		return Resource{}, resourceError("fs.resource_not_found", profileName, resourceName, "tdc fs resource is not configured")
	}
	return resource, err
}

func Resolve(homeDir string, profile *config.Profile, selector string, selectorExplicit bool, env map[string]string) (*config.Profile, Resource, error) {
	return resolve(homeDir, profile, selector, selectorExplicit, env, true)
}

func ResolveDryRun(homeDir string, profile *config.Profile, selector string, selectorExplicit bool, env map[string]string) (*config.Profile, Resource, error) {
	return resolve(homeDir, profile, selector, selectorExplicit, env, false)
}

func ResolveAuthenticated(homeDir string, profile *config.Profile, opts ResolveAuthOptions) (*config.Profile, Resource, error) {
	if profile == nil {
		return nil, Resource{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	name := strings.TrimSpace(opts.Selector)
	if opts.SelectorExplicit && name == "" {
		return nil, Resource{}, apperr.New("fs.empty_file_system_name", "usage", 2, "--file-system-name cannot be empty")
	}
	if name == "" {
		name = strings.TrimSpace(fsEnvValue(opts.Env, "TDC_FS_FILE_SYSTEM_NAME"))
	}
	if name == "" {
		return nil, Resource{}, missingFileSystemName()
	}

	if opts.DryRun {
		if err := validateLegacy(profile); err != nil {
			return nil, Resource{}, err
		}
	} else if err := MigrateLegacy(homeDir, profile); err != nil {
		return nil, Resource{}, err
	}

	token := strings.TrimSpace(opts.Token)
	if opts.TokenExplicit && token == "" {
		return nil, Resource{}, apperr.New("fs.empty_token", "usage", 2, "--fs-token cannot be empty")
	}
	if token == "" {
		token = strings.TrimSpace(fsEnvValue(opts.Env, "TDC_FS_TOKEN"))
	}

	resource, resourceErr := Get(homeDir, profile.Name, name)
	found := resourceErr == nil
	if resourceErr != nil && opts.DryRun {
		legacy := legacyResource(profile)
		if legacy.Name == name {
			resource = legacy
			resourceErr = nil
			found = true
		}
	}
	if resourceErr != nil {
		if apperr.CodeFor(resourceErr) != "fs.resource_not_found" {
			return nil, Resource{}, resourceErr
		}
		if token == "" && opts.TokenRequired {
			return nil, Resource{}, resourceErr
		}
		resource = Resource{Name: name}
	}
	if token == "" && found {
		token = strings.TrimSpace(resource.APIKey)
	}
	if opts.TokenRequired && token == "" {
		return nil, Resource{}, apperr.New("fs.missing_token", "authentication", 3, "authentication required: missing FS token; pass --fs-token, set TDC_FS_TOKEN, or select a locally registered file system with credentials")
	}

	placementCode := strings.TrimSpace(opts.RegionOverride)
	if placementCode == "" && found {
		placementCode = strings.TrimSpace(resource.RegionCode)
	}
	if placementCode == "" {
		placementCode = strings.TrimSpace(profile.PlacementRegionCode)
	}
	if placementCode == "" {
		return nil, Resource{}, apperr.New("fs.missing_region", "config", 2, "tdc fs region is required; pass --region, set TDC_REGION_CODE, or configure region_code for the selected file system or profile")
	}
	placement, err := region.ParsePlacementCode(placementCode)
	if err != nil {
		return nil, Resource{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}

	resource.RegionCode = placement.Code
	resource.CloudProvider = placement.Provider
	resource.APIKey = token
	resource.HasAPIKey = token != ""
	selected := *profile
	selected.FSResourceName = resource.Name
	selected.FSTenantID = resource.TenantID
	selected.FSPlacementRegionCode = placement.Code
	selected.FSCloudProvider = placement.Provider
	selected.FSRegionCode = placement.NativeCode
	selected.FSAPIKey = token
	return &selected, resource, nil
}

func resolve(homeDir string, profile *config.Profile, selector string, selectorExplicit bool, env map[string]string, migrate bool) (*config.Profile, Resource, error) {
	if profile == nil {
		return nil, Resource{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	name := strings.TrimSpace(selector)
	if selectorExplicit && name == "" {
		return nil, Resource{}, apperr.New("fs.empty_file_system_name", "usage", 2, "--file-system-name cannot be empty")
	}
	if name == "" {
		if env != nil {
			name = strings.TrimSpace(env["TDC_FS_FILE_SYSTEM_NAME"])
		} else {
			name = strings.TrimSpace(os.Getenv("TDC_FS_FILE_SYSTEM_NAME"))
		}
	}
	if name == "" {
		return nil, Resource{}, missingFileSystemName()
	}
	if migrate {
		if err := MigrateLegacy(homeDir, profile); err != nil {
			return nil, Resource{}, err
		}
	} else if err := validateLegacy(profile); err != nil {
		return nil, Resource{}, err
	}
	resource, err := Get(homeDir, profile.Name, name)
	if err != nil && !migrate {
		legacy := legacyResource(profile)
		if legacy.Name == name {
			resource = legacy
			err = nil
		}
	}
	if err != nil {
		return nil, Resource{}, err
	}
	selected := *profile
	selected.FSResourceName = resource.Name
	selected.FSTenantID = resource.TenantID
	selected.FSCloudProvider = resource.CloudProvider
	selected.FSPlacementRegionCode = resource.RegionCode
	placement, err := region.ParsePlacementCode(resource.RegionCode)
	if err != nil {
		return nil, Resource{}, resourceError("fs.resource_credentials_incomplete", profile.Name, resource.Name, "stored region_code is invalid")
	}
	selected.FSRegionCode = placement.NativeCode
	selected.FSAPIKey = resource.APIKey
	return &selected, resource, nil
}

func missingFileSystemName() error {
	return apperr.New("fs.missing_file_system_name", "usage", 2, "file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME")
}

func fsEnvValue(env map[string]string, key string) string {
	if env != nil {
		return env[key]
	}
	return os.Getenv(key)
}

func Delete(homeDir string, profile *config.Profile, resourceName string) error {
	if profile == nil {
		return apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	dir, err := resourceDir(homeDir, profile.Name, resourceName)
	if err != nil {
		return err
	}
	if _, err := Get(homeDir, profile.Name, resourceName); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete local tdc fs resource %q: %w", resourceName, err)
	}
	return nil
}

func MigrateLegacy(homeDir string, profile *config.Profile) error {
	if profile == nil {
		return nil
	}
	if _, err := store.RemoveLegacyFSDefaultFileSystem(homeDir, profile.Name); err != nil {
		return apperr.Wrap("fs.resource_migration_failed", "config", 1, "remove legacy default file system", err)
	}
	hasConfig := strings.TrimSpace(profile.FSResourceName) != "" || strings.TrimSpace(profile.FSTenantID) != "" || strings.TrimSpace(profile.FSPlacementRegionCode) != ""
	hasCreds := strings.TrimSpace(profile.FSAPIKey) != ""
	if !hasConfig && !hasCreds {
		return nil
	}
	if !legacyComplete(profile) {
		return resourceError("fs.resource_credentials_incomplete", profile.Name, profile.FSResourceName, "legacy flat tdc fs credentials are incomplete; recreate the resource")
	}
	existing, err := Get(homeDir, profile.Name, profile.FSResourceName)
	if err == nil {
		if existing.TenantID != strings.TrimSpace(profile.FSTenantID) || existing.APIKey != strings.TrimSpace(profile.FSAPIKey) {
			return resourceError("fs.resource_migration_failed", profile.Name, profile.FSResourceName, "legacy flat resource disagrees with the existing registry resource")
		}
	} else {
		if apperr.CodeFor(err) != "fs.resource_not_found" {
			return apperr.Wrap("fs.resource_migration_failed", "config", 1, err.Error(), err)
		}
		if err := Store(homeDir, profile, profile.FSResourceName, profile.FSTenantID, profile.FSCloudProvider, profile.FSPlacementRegionCode, profile.FSAPIKey); err != nil {
			return apperr.Wrap("fs.resource_migration_failed", "config", 1, err.Error(), err)
		}
	}
	if err := store.ClearFSResource(homeDir, profile.Name); err != nil {
		return apperr.Wrap("fs.resource_migration_failed", "config", 1, "clear migrated flat tdc fs credentials", err)
	}
	profile.FSResourceName = ""
	profile.FSTenantID = ""
	profile.FSCloudProvider = ""
	profile.FSPlacementRegionCode = ""
	profile.FSRegionCode = ""
	profile.FSAPIKey = ""
	return nil
}

func validateLegacy(profile *config.Profile) error {
	if profile == nil {
		return nil
	}
	hasConfig := strings.TrimSpace(profile.FSResourceName) != "" || strings.TrimSpace(profile.FSTenantID) != "" || strings.TrimSpace(profile.FSPlacementRegionCode) != ""
	hasCreds := strings.TrimSpace(profile.FSAPIKey) != ""
	if !hasConfig && !hasCreds {
		return nil
	}
	if !legacyComplete(profile) {
		return resourceError("fs.resource_credentials_incomplete", profile.Name, profile.FSResourceName, "legacy flat tdc fs credentials are incomplete; recreate the resource")
	}
	return nil
}

func legacyComplete(profile *config.Profile) bool {
	return profile != nil &&
		strings.TrimSpace(profile.FSResourceName) != "" &&
		strings.TrimSpace(profile.FSTenantID) != "" &&
		strings.TrimSpace(profile.FSPlacementRegionCode) != "" &&
		strings.TrimSpace(profile.FSAPIKey) != ""
}

func legacyResource(profile *config.Profile) Resource {
	if profile == nil || strings.TrimSpace(profile.FSResourceName) == "" {
		return Resource{}
	}
	return Resource{
		Name:          strings.TrimSpace(profile.FSResourceName),
		TenantID:      strings.TrimSpace(profile.FSTenantID),
		CloudProvider: strings.TrimSpace(profile.FSCloudProvider),
		RegionCode:    strings.TrimSpace(profile.FSPlacementRegionCode),
		HasAPIKey:     strings.TrimSpace(profile.FSAPIKey) != "",
		APIKey:        strings.TrimSpace(profile.FSAPIKey),
	}
}

func CompanionHome(homeDir, profileName, resourceName string) (string, error) {
	profileKey := encodeKey(normalizedProfile(profileName))
	resourceKey := encodeKey(strings.TrimSpace(resourceName))
	if resourceKey == "" {
		return "", resourceError("fs.resource_not_configured", profileName, resourceName, "tdc fs resource is required for the companion runtime")
	}
	return filepath.Join(homeDir, store.TDCDirName, "drive9-home", profileKey, resourceKey), nil
}

func Paths(homeDir, profileName, resourceName string) (RegistryPaths, error) {
	dir, err := resourceDir(homeDir, profileName, resourceName)
	if err != nil {
		return RegistryPaths{}, err
	}
	return RegistryPaths{
		Config:      filepath.Join(dir, configFileName),
		Credentials: filepath.Join(dir, credsFileName),
	}, nil
}

func canonicalPlacement(profile *config.Profile, cloudProvider, regionCode string) (string, string, error) {
	cloudProvider = normalizeProvider(cloudProvider)
	regionCode = strings.TrimSpace(regionCode)
	if regionCode == "" && profile != nil {
		regionCode = profile.PlacementRegionCode
	}
	if parsed, err := region.ParsePlacementCode(regionCode); err == nil {
		return parsed.Code, parsed.Provider, nil
	}
	if cloudProvider == "" && profile != nil {
		cloudProvider = profile.CloudProvider
	}
	if regionCode == "" && profile != nil {
		regionCode = profile.RegionCode
	}
	canonicalCode, err := region.CanonicalCode(cloudProvider, regionCode)
	if err != nil {
		return "", "", apperr.Wrap("fs.invalid_resource_region", "config", 2, err.Error(), err)
	}
	return canonicalCode, cloudProvider, nil
}

func normalizeProvider(provider string) string {
	switch strings.TrimSpace(provider) {
	case "alicloud", "ali", region.ProviderAlibabaCloud:
		return region.ProviderAlibabaCloud
	case region.ProviderAWS:
		return region.ProviderAWS
	default:
		return strings.TrimSpace(provider)
	}
}

func resourceDir(homeDir, profileName, resourceName string) (string, error) {
	resourceName = strings.TrimSpace(resourceName)
	if resourceName == "" {
		return "", resourceError("fs.resource_not_found", profileName, resourceName, "file system name is required")
	}
	return filepath.Join(profileDir(homeDir, profileName), encodeKey(resourceName)), nil
}

func profileDir(homeDir, profileName string) string {
	return filepath.Join(homeDir, store.TDCDirName, resourcesDirName, encodeKey(normalizedProfile(profileName)))
}

func ensureRegistryDirs(homeDir, profileName, resourceName string) error {
	root := filepath.Join(homeDir, store.TDCDirName, resourcesDirName)
	profilePath := profileDir(homeDir, profileName)
	resourcePath, err := resourceDir(homeDir, profileName, resourceName)
	if err != nil {
		return err
	}
	for _, path := range []string{root, profilePath, resourcePath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func encodeKey(value string) string {
	if value == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeKey(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || encodeKey(string(decoded)) != value {
		return "", errors.New("invalid resource key")
	}
	return string(decoded), nil
}

func normalizedProfile(profileName string) string {
	if strings.TrimSpace(profileName) == "" {
		return config.DefaultProfile
	}
	return strings.TrimSpace(profileName)
}

func readResourceDir(dir, profileName, expectedName string) (Resource, error) {
	configPath := filepath.Join(dir, configFileName)
	credsPath := filepath.Join(dir, credsFileName)
	configData, configErr := os.ReadFile(configPath)
	credsData, credsErr := os.ReadFile(credsPath)
	if errors.Is(configErr, os.ErrNotExist) && errors.Is(credsErr, os.ErrNotExist) {
		return Resource{}, os.ErrNotExist
	}
	if configErr != nil || credsErr != nil {
		return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "resource config and credentials must both exist")
	}
	if info, err := os.Stat(credsPath); err != nil || info.Mode().Perm()&0o077 != 0 {
		if err == nil {
			err = os.Chmod(credsPath, 0o600)
		}
		if err != nil {
			return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "cannot restrict resource credentials permissions")
		}
	}
	var resource Resource
	if err := toml.Unmarshal(configData, &resource); err != nil {
		return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "cannot parse resource config")
	}
	var creds credentials
	if err := toml.Unmarshal(credsData, &creds); err != nil {
		return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "cannot parse resource credentials")
	}
	if resource.Name != expectedName || strings.TrimSpace(resource.TenantID) == "" || strings.TrimSpace(resource.CloudProvider) == "" || strings.TrimSpace(resource.RegionCode) == "" || strings.TrimSpace(creds.APIKey) == "" {
		return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "resource config or credentials are incomplete")
	}
	placement, err := region.ParsePlacementCode(resource.RegionCode)
	if err != nil || normalizeProvider(resource.CloudProvider) != placement.Provider {
		return Resource{}, resourceError("fs.resource_credentials_incomplete", profileName, expectedName, "resource cloud_provider and region_code are invalid or inconsistent")
	}
	resource.CloudProvider = placement.Provider
	resource.RegionCode = placement.Code
	resource.APIKey = strings.TrimSpace(creds.APIKey)
	resource.HasAPIKey = true
	return resource, nil
}

func writeTOML(path string, value any, mode os.FileMode) error {
	data, err := toml.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func resourceError(code, profileName, resourceName, detail string) error {
	message := fmt.Sprintf("%s for profile %q", detail, normalizedProfile(profileName))
	if strings.TrimSpace(resourceName) != "" {
		message += fmt.Sprintf(" and file system %q", strings.TrimSpace(resourceName))
	}
	return apperr.New(code, "config", 2, message)
}
