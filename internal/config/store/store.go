package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const (
	TIDirName      = ".ti"
	ConfigFileName = "config"
	CredsFileName  = "credentials"
	configFileMode = 0o644
	credsFileMode  = 0o600
	tiDirFileMode  = 0o700
)

type ConfigDocument map[string]ConfigProfile

type ConfigProfile struct {
	CloudProvider   string `toml:"cloud_provider,omitempty"`
	RegionCode      string `toml:"region_code,omitempty"`
	ProjectID       string `toml:"project_id,omitempty"`
	FSResourceName  string `toml:"fs_resource_name,omitempty"`
	FSTenantID      string `toml:"fs_tenant_id,omitempty"`
	FSCloudProvider string `toml:"fs_cloud_provider,omitempty"`
	FSRegionCode    string `toml:"fs_region_code,omitempty"`
}

type CredentialsDocument map[string]CredentialsProfile

type CredentialsProfile struct {
	TiDBCloudPublicKey  string `toml:"tidb_cloud_public_key,omitempty"`
	TiDBCloudPrivateKey string `toml:"tidb_cloud_private_key,omitempty"`
	FSAPIKey            string `toml:"fs_api_key,omitempty"`
}

type credentialsProfileWire struct {
	TiDBCloudPublicKey  string `toml:"tidb_cloud_public_key,omitempty"`
	TiDBCloudPrivateKey string `toml:"tidb_cloud_private_key,omitempty"`
	LegacyTDCPublicKey  string `toml:"tdc_public_key,omitempty"`
	LegacyTDCPrivateKey string `toml:"tdc_private_key,omitempty"`
	FSAPIKey            string `toml:"fs_api_key,omitempty"`
}

func ConfigPath(homeDir string) string {
	return filepath.Join(homeDir, TIDirName, ConfigFileName)
}

func CredentialsPath(homeDir string) string {
	return filepath.Join(homeDir, TIDirName, CredsFileName)
}

func ReadConfig(homeDir string) (ConfigDocument, error) {
	path := ConfigPath(homeDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ConfigDocument{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := rejectDisallowedKeys(data, path); err != nil {
		return nil, err
	}

	var doc ConfigDocument
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if doc == nil {
		doc = ConfigDocument{}
	}
	return doc, nil
}

func ReadCredentials(homeDir string) (CredentialsDocument, error) {
	path := CredentialsPath(homeDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return CredentialsDocument{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", path, err)
	}
	if err := EnsureCredentialsPermissions(path); err != nil {
		return nil, err
	}
	if err := rejectDisallowedKeys(data, path); err != nil {
		return nil, err
	}
	if err := rejectDisallowedCredentialKeys(data, path); err != nil {
		return nil, err
	}

	return DecodeCredentials(data, path)
}

func DecodeCredentials(data []byte, path string) (CredentialsDocument, error) {
	var wire map[string]credentialsProfileWire
	if err := toml.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	doc := make(CredentialsDocument, len(wire))
	for profileName, profile := range wire {
		publicKey, err := resolveCredentialField(path, profileName, "tidb_cloud_public_key", profile.TiDBCloudPublicKey, "tdc_public_key", profile.LegacyTDCPublicKey)
		if err != nil {
			return nil, err
		}
		privateKey, err := resolveCredentialField(path, profileName, "tidb_cloud_private_key", profile.TiDBCloudPrivateKey, "tdc_private_key", profile.LegacyTDCPrivateKey)
		if err != nil {
			return nil, err
		}
		doc[profileName] = CredentialsProfile{
			TiDBCloudPublicKey:  publicKey,
			TiDBCloudPrivateKey: privateKey,
			FSAPIKey:            profile.FSAPIKey,
		}
	}
	return doc, nil
}

func NormalizeCredentials(data []byte, path string) ([]byte, error) {
	doc, err := DecodeCredentials(data, path)
	if err != nil {
		return nil, err
	}
	if err := rejectDisallowedKeys(data, path); err != nil {
		return nil, err
	}
	if err := rejectDisallowedCredentialKeys(data, path); err != nil {
		return nil, err
	}
	var raw map[string]map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	for profileName, profile := range raw {
		resolved := doc[profileName]
		if resolved.TiDBCloudPublicKey != "" {
			profile["tidb_cloud_public_key"] = resolved.TiDBCloudPublicKey
		}
		if resolved.TiDBCloudPrivateKey != "" {
			profile["tidb_cloud_private_key"] = resolved.TiDBCloudPrivateKey
		}
		delete(profile, "tdc_public_key")
		delete(profile, "tdc_private_key")
		raw[profileName] = profile
	}
	normalized, err := toml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal credentials %s: %w", path, err)
	}
	return normalized, nil
}

func resolveCredentialField(path, profileName, canonicalName, canonicalValue, legacyName, legacyValue string) (string, error) {
	if canonicalValue != "" && legacyValue != "" && canonicalValue != legacyValue {
		return "", apperr.New(
			"config.environment_conflict",
			"config",
			2,
			fmt.Sprintf("credential fields %s and %s contain different values for profile %q in %s; remove one or make them equal", canonicalName, legacyName, profileName, path),
		)
	}
	if canonicalValue != "" {
		return canonicalValue, nil
	}
	return legacyValue, nil
}

func WriteProfile(homeDir, profileName string, cfg ConfigProfile, creds CredentialsProfile) error {
	if profileName == "" {
		profileName = "default"
	}
	if IsReservedProfileName(profileName) {
		return fmt.Errorf("profile name %q is reserved", profileName)
	}
	if err := ensureDir(homeDir); err != nil {
		return err
	}

	configDoc, err := ReadConfig(homeDir)
	if err != nil {
		return err
	}
	existingConfig := configDoc[profileName]
	if cfg.CloudProvider != "" {
		existingConfig.CloudProvider = cfg.CloudProvider
	}
	if cfg.RegionCode != "" {
		existingConfig.RegionCode = cfg.RegionCode
		existingConfig.CloudProvider = ""
	}
	if cfg.ProjectID != "" {
		existingConfig.ProjectID = cfg.ProjectID
	}
	if cfg.FSResourceName != "" {
		existingConfig.FSResourceName = cfg.FSResourceName
	}
	if cfg.FSTenantID != "" {
		existingConfig.FSTenantID = cfg.FSTenantID
	}
	if cfg.FSCloudProvider != "" {
		existingConfig.FSCloudProvider = cfg.FSCloudProvider
	}
	if cfg.FSRegionCode != "" {
		existingConfig.FSRegionCode = cfg.FSRegionCode
	}
	configDoc[profileName] = existingConfig

	credentialsDoc, err := ReadCredentials(homeDir)
	if err != nil {
		return err
	}
	existingCreds := credentialsDoc[profileName]
	if creds.TiDBCloudPublicKey != "" {
		existingCreds.TiDBCloudPublicKey = creds.TiDBCloudPublicKey
	}
	if creds.TiDBCloudPrivateKey != "" {
		existingCreds.TiDBCloudPrivateKey = creds.TiDBCloudPrivateKey
	}
	if creds.FSAPIKey != "" {
		existingCreds.FSAPIKey = creds.FSAPIKey
	}
	credentialsDoc[profileName] = existingCreds

	if err := writeTOML(ConfigPath(homeDir), configDoc, configFileMode); err != nil {
		return err
	}
	if err := writeTOML(CredentialsPath(homeDir), credentialsDoc, credsFileMode); err != nil {
		return err
	}
	return nil
}

func RemoveLegacyFSDefaultFileSystem(homeDir, profileName string) (bool, error) {
	if profileName == "" {
		profileName = "default"
	}
	path := ConfigPath(homeDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := rejectDisallowedKeys(data, path); err != nil {
		return false, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse config %s: %w", path, err)
	}
	profile, ok := doc[profileName].(map[string]any)
	if !ok {
		return false, nil
	}
	if _, ok := profile["fs_default_file_system_name"]; !ok {
		return false, nil
	}
	delete(profile, "fs_default_file_system_name")
	doc[profileName] = profile
	if err := writeTOML(path, doc, configFileMode); err != nil {
		return false, err
	}
	return true, nil
}

func ClearFSResource(homeDir, profileName string) error {
	if profileName == "" {
		profileName = "default"
	}
	if err := ensureDir(homeDir); err != nil {
		return err
	}

	configDoc, err := ReadConfig(homeDir)
	if err != nil {
		return err
	}
	existingConfig := configDoc[profileName]
	existingConfig.FSResourceName = ""
	existingConfig.FSTenantID = ""
	existingConfig.FSCloudProvider = ""
	existingConfig.FSRegionCode = ""
	configDoc[profileName] = existingConfig

	credentialsDoc, err := ReadCredentials(homeDir)
	if err != nil {
		return err
	}
	existingCreds := credentialsDoc[profileName]
	existingCreds.FSAPIKey = ""
	credentialsDoc[profileName] = existingCreds

	if err := writeTOML(ConfigPath(homeDir), configDoc, configFileMode); err != nil {
		return err
	}
	if err := writeTOML(CredentialsPath(homeDir), credentialsDoc, credsFileMode); err != nil {
		return err
	}
	return nil
}

func EnsureCredentialsPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, credsFileMode); err != nil {
		return fmt.Errorf("restrict credentials permissions %s: %w", path, err)
	}
	return nil
}

func ensureDir(homeDir string) error {
	if homeDir == "" {
		return errors.New("home directory is required")
	}
	return os.MkdirAll(filepath.Join(homeDir, TIDirName), tiDirFileMode)
}

func writeTOML(path string, value any, mode os.FileMode) error {
	data, err := toml.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, tiDirFileMode); err != nil {
		return err
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return os.Chmod(path, mode)
}

func rejectDisallowedKeys(data []byte, path string) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	var walk func(prefix string, value any) error
	walk = func(prefix string, value any) error {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				next := key
				if prefix != "" {
					next = prefix + "." + key
				}
				if IsDisallowedKey(key) {
					return fmt.Errorf("unsupported URL-like config key %q in %s; configure region_code instead", next, path)
				}
				if err := walk(next, nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("", raw)
}

func IsReservedProfileName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "logging")
}

func IsDisallowedKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	return strings.Contains(normalized, "url") ||
		strings.Contains(normalized, "endpoint") ||
		strings.Contains(normalized, "databaseurl")
}

func rejectDisallowedCredentialKeys(data []byte, path string) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	var walk func(prefix string, value any) error
	walk = func(prefix string, value any) error {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				next := key
				if prefix != "" {
					next = prefix + "." + key
				}
				if strings.EqualFold(key, "db_users") {
					return fmt.Errorf("unsupported DB user credentials key %q in %s; store DB SQL users under ~/.ti/db_users/<cluster-id>/credentials", next, path)
				}
				if err := walk(next, nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("", raw)
}
