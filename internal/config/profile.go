package config

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config/region"
	"github.com/tidbcloud/tdc/internal/config/store"
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
	ProjectID             string
	TDCPublicKey          string
	TDCPrivateKey         string
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

	publicKey, privateKey, source, err := resolveTDCCredentials(opts.HomeDir, profileName, creds, hasCreds, opts.Env)
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
		ProjectID:             cfg.ProjectID,
		TDCPublicKey:          publicKey,
		TDCPrivateKey:         privateKey,
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
		ProjectID:             cfg.ProjectID,
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

func resolveOptionalPlacement(cfg store.ConfigProfile, regionOverride string, env map[string]string) (region.Placement, bool, error) {
	regionCode := strings.TrimSpace(regionOverride)
	if regionCode == "" {
		regionCode = strings.TrimSpace(envValue(env, "TDC_REGION_CODE"))
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
		regionCode = envValue(env, "TDC_REGION_CODE")
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
				fmt.Sprintf("profile %q not found in %s; run tdc configure --profile %s or write ~/.tdc/config", profileName, store.ConfigPath(homeDir), profileName),
			)
		}
		return region.Placement{}, missingConfig(profileName, store.ConfigPath(homeDir), "region_code")
	}
	return parsePlacement(regionCode)
}

func resolveTDCCredentials(homeDir, profileName string, creds store.CredentialsProfile, hasCreds bool, env map[string]string) (string, string, string, error) {
	envPublic := strings.TrimSpace(envValue(env, "TDC_PUBLIC_KEY"))
	envPrivate := strings.TrimSpace(envValue(env, "TDC_PRIVATE_KEY"))
	if envPublic != "" || envPrivate != "" {
		if envPublic == "" {
			return "", "", "", envMissing("TDC_PUBLIC_KEY")
		}
		if envPrivate == "" {
			return "", "", "", envMissing("TDC_PRIVATE_KEY")
		}
		return envPublic, envPrivate, "env", nil
	}

	if !hasCreds {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tdc_public_key")
	}
	if creds.TDCPublicKey == "" {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tdc_public_key")
	}
	if creds.TDCPrivateKey == "" {
		return "", "", "", missingCredential(profileName, store.CredentialsPath(homeDir), "tdc_private_key")
	}
	return creds.TDCPublicKey, creds.TDCPrivateKey, "profile", nil
}

func envMissing(key string) error {
	return apperr.New(
		"config.env_missing",
		"config",
		2,
		fmt.Sprintf("%s is required when using TDC_* environment credentials", key),
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
		fmt.Sprintf("%s missing for profile %q in %s; run tdc configure --profile %s or write ~/.tdc/config", key, profileName, path, profileName),
	)
}

func missingCredential(profileName, path, key string) error {
	return apperr.New(
		"config.missing_credentials",
		"config",
		2,
		fmt.Sprintf("%s missing for profile %q in %s; run tdc configure --profile %s or write ~/.tdc/credentials", key, profileName, path, profileName),
	)
}

func envValue(env map[string]string, key string) string {
	if env != nil {
		return env[key]
	}
	return os.Getenv(key)
}
