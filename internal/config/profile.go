package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

const DefaultProfile = "default"

type LoadOptions struct {
	Profile         string
	ProfileExplicit bool
	RegionOverride  string
	HomeDir         string
	Env             map[string]string
}

type Profile struct {
	Name                  string
	HomeDir               string
	Source                string
	PlacementRegionCode   string
	CloudProvider         string
	RegionCode            string
	TiDBCloudPublicKey    string
	TiDBCloudPrivateKey   string
	FSResourceName        string
	FSTenantID            string
	FSPlacementRegionCode string
	FSCloudProvider       string
	FSRegionCode          string
	FSAPIKey              string
}

func Load(ctx context.Context, opts LoadOptions) (*Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, apperr.Wrap("config.home_dir", "config", 1, "cannot determine home directory", err)
		}
		opts.HomeDir = home
	}

	profileName := opts.Profile
	if profileName == "" {
		profileName = DefaultProfile
	}
	if err := ValidateProfileName(profileName); err != nil {
		return nil, err
	}

	configDoc, err := store.ReadConfig(opts.HomeDir)
	if err != nil {
		return nil, apperr.Wrap("config.read_config", "config", 1, err.Error(), err)
	}
	credentialsDoc, err := store.ReadCredentials(opts.HomeDir)
	if err != nil {
		return nil, apperr.Wrap("config.read_credentials", "config", 1, err.Error(), err)
	}

	cfg, hasConfig := configDoc[profileName]
	creds, hasCreds := credentialsDoc[profileName]

	placement, err := resolvePlacement(opts.HomeDir, profileName, cfg, hasConfig, opts.RegionOverride, opts.Env)
	if err != nil {
		return nil, err
	}

	publicKey, privateKey, source, err := resolveTiDBCloudCredentials(opts.HomeDir, profileName, creds, hasCreds, opts.Env)
	if err != nil {
		return nil, err
	}

	var fsPlacement region.Placement
	if cfg.FSRegionCode != "" {
		fsPlacement, err = parsePlacement(cfg.FSRegionCode)
		if err != nil {
			return nil, err
		}
	}

	return &Profile{
		Name:                  profileName,
		HomeDir:               opts.HomeDir,
		Source:                source,
		PlacementRegionCode:   placement.Code,
		CloudProvider:         placement.Provider,
		RegionCode:            placement.NativeCode,
		TiDBCloudPublicKey:    publicKey,
		TiDBCloudPrivateKey:   privateKey,
		FSResourceName:        cfg.FSResourceName,
		FSTenantID:            cfg.FSTenantID,
		FSPlacementRegionCode: fsPlacement.Code,
		FSCloudProvider:       fsPlacement.Provider,
		FSRegionCode:          fsPlacement.NativeCode,
		FSAPIKey:              creds.FSAPIKey,
	}, nil
}

// LoadLocal loads a profile namespace and any locally available placement and
// filesystem state without requiring TiDB Cloud API credentials.
func LoadLocal(ctx context.Context, opts LoadOptions) (*Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if opts.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, apperr.Wrap("config.home_dir", "config", 1, "cannot determine home directory", err)
		}
		opts.HomeDir = home
	}

	profileName := opts.Profile
	if profileName == "" {
		profileName = DefaultProfile
	}
	if err := ValidateProfileName(profileName); err != nil {
		return nil, err
	}
	configDoc, err := store.ReadConfig(opts.HomeDir)
	if err != nil {
		return nil, apperr.Wrap("config.read_config", "config", 1, err.Error(), err)
	}
	credentialsDoc, err := store.ReadCredentials(opts.HomeDir)
	if err != nil {
		return nil, apperr.Wrap("config.read_credentials", "config", 1, err.Error(), err)
	}
	cfg, hasConfig := configDoc[profileName]
	creds, hasCreds := credentialsDoc[profileName]

	placement, hasPlacement, err := resolveOptionalPlacement(cfg, opts.RegionOverride, opts.Env)
	if err != nil {
		return nil, err
	}
	var fsPlacement region.Placement
	if cfg.FSRegionCode != "" {
		fsPlacement, err = parsePlacement(cfg.FSRegionCode)
		if err != nil {
			return nil, err
		}
	}
	source := "local"
	if hasConfig || hasCreds {
		source = "profile"
	}
	profile := &Profile{
		Name:                  profileName,
		HomeDir:               opts.HomeDir,
		Source:                source,
		FSResourceName:        cfg.FSResourceName,
		FSTenantID:            cfg.FSTenantID,
		FSPlacementRegionCode: fsPlacement.Code,
		FSCloudProvider:       fsPlacement.Provider,
		FSRegionCode:          fsPlacement.NativeCode,
		FSAPIKey:              creds.FSAPIKey,
	}
	if hasPlacement {
		profile.PlacementRegionCode = placement.Code
		profile.CloudProvider = placement.Provider
		profile.RegionCode = placement.NativeCode
	}
	return profile, nil
}

func ValidateProfileName(name string) error {
	if !store.IsReservedProfileName(name) {
		return nil
	}
	return apperr.New(
		"config.reserved_profile",
		"usage",
		2,
		fmt.Sprintf("profile name %q is reserved; choose another profile name", name),
	)
}

func resolveOptionalPlacement(cfg store.ConfigProfile, regionOverride string, env map[string]string) (region.Placement, bool, error) {
	regionCode := strings.TrimSpace(regionOverride)
	if regionCode == "" {
		resolved, _, _, err := envcompat.ResolveNames(env, "TI_REGION_CODE", "TDC_REGION_CODE")
		if err != nil {
			return region.Placement{}, false, err
		}
		regionCode = strings.TrimSpace(resolved)
	}
	if regionCode == "" {
		regionCode = strings.TrimSpace(cfg.RegionCode)
	}
	if regionCode == "" {
		return region.Placement{}, false, nil
	}
	placement, err := parsePlacement(regionCode)
	if err != nil {
		return region.Placement{}, false, err
	}
	return placement, true, nil
}

func resolvePlacement(homeDir, profileName string, cfg store.ConfigProfile, hasConfig bool, regionOverride string, env map[string]string) (region.Placement, error) {
	regionCode := strings.TrimSpace(regionOverride)
	if regionCode == "" {
		resolved, _, _, err := envcompat.ResolveNames(env, "TI_REGION_CODE", "TDC_REGION_CODE")
		if err != nil {
			return region.Placement{}, err
		}
		regionCode = strings.TrimSpace(resolved)
	}
	if regionCode == "" {
		regionCode = cfg.RegionCode
	}
	if regionCode == "" {
		if !hasConfig {
			return region.Placement{}, apperr.New(
				"config.profile_not_found",
				"config",
				2,
				fmt.Sprintf("profile %q not found in %s; run ti configure --profile %s or write ~/.ti/config", profileName, store.ConfigPath(homeDir), profileName),
			)
		}
		return region.Placement{}, missingConfig(profileName, store.ConfigPath(homeDir), "region_code")
	}
	return parsePlacement(regionCode)
}

func resolveTiDBCloudCredentials(homeDir, profileName string, creds store.CredentialsProfile, hasCreds bool, env map[string]string) (string, string, string, error) {
	envPublicValue, _, _, err := envcompat.ResolveNames(env, "TIDB_CLOUD_PUBLIC_KEY", "TDC_PUBLIC_KEY")
	if err != nil {
		return "", "", "", err
	}
	envPrivateValue, _, _, err := envcompat.ResolveNames(env, "TIDB_CLOUD_PRIVATE_KEY", "TDC_PRIVATE_KEY")
	if err != nil {
		return "", "", "", err
	}
	envPublic := strings.TrimSpace(envPublicValue)
	envPrivate := strings.TrimSpace(envPrivateValue)
	if envPublic != "" || envPrivate != "" {
		if envPublic == "" {
			return "", "", "", envMissing("TIDB_CLOUD_PUBLIC_KEY")
		}
		if envPrivate == "" {
			return "", "", "", envMissing("TIDB_CLOUD_PRIVATE_KEY")
		}
		return envPublic, envPrivate, "env", nil
	}

	if !hasCreds {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tidb_cloud_public_key")
	}
	if creds.TiDBCloudPublicKey == "" {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tidb_cloud_public_key")
	}
	if creds.TiDBCloudPrivateKey == "" {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tidb_cloud_private_key")
	}
	return creds.TiDBCloudPublicKey, creds.TiDBCloudPrivateKey, "profile", nil
}

func envMissing(key string) error {
	return apperr.New(
		"config.env_missing",
		"config",
		2,
		fmt.Sprintf("%s is required when using TiDB Cloud environment credentials", key),
	)
}

func parsePlacement(regionCode string) (region.Placement, error) {
	placement, err := region.ParsePlacementCode(regionCode)
	if err != nil {
		return region.Placement{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	return placement, nil
}

func missingConfig(profileName, path, key string) error {
	return apperr.New(
		"config.missing_config",
		"config",
		2,
		fmt.Sprintf("%s missing for profile %q in %s; run ti configure --profile %s or write ~/.ti/config", key, profileName, path, profileName),
	)
}

func missingCredential(profileName, path, key string) error {
	return apperr.New(
		"config.missing_credentials",
		"config",
		2,
		fmt.Sprintf("%s missing for profile %q in %s; run ti configure --profile %s or write ~/.ti/credentials", key, profileName, path, profileName),
	)
}
