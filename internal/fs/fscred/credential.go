package fscred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/config/region"
	"github.com/tidbcloud/tdc/internal/config/store"
)

const (
	credentialsDirName     = "fs_credentials"
	migrationStateFileName = ".legacy-name-registry-migration"
	migrationStateSchema   = 1
)

var migrationMu sync.Mutex

type Credential struct {
	FileSystemID  string `json:"file_system_id" toml:"file_system_id"`
	RegionCode    string `json:"region_code" toml:"region_code"`
	HasLocalToken bool   `json:"has_local_token" toml:"-"`
	APIKey        string `json:"-" toml:"api_key"`
}

type CredentialPaths struct {
	Credentials string `json:"credentials"`
}

type migrationState struct {
	SchemaVersion int      `toml:"schema_version"`
	FileSystemIDs []string `toml:"file_system_ids"`
}

type ResolveCredentialOptions struct {
	FileSystemID         string
	FileSystemIDExplicit bool
	Token                string
	TokenExplicit        bool
	RegionOverride       string
	TokenRequired        bool
	Env                  map[string]string
	DryRun               bool
}

func StoreCredential(homeDir string, profile *config.Profile, fileSystemID, regionCode, apiKey string, replace bool) (Credential, error) {
	if profile == nil {
		return Credential{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	fileSystemID, err := ValidateFileSystemID(fileSystemID)
	if err != nil {
		return Credential{}, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Credential{}, apperr.New("fs.missing_token", "authentication", 3, "authentication required: missing FS token")
	}
	placementCode := strings.TrimSpace(regionCode)
	if placementCode == "" {
		placementCode = strings.TrimSpace(profile.PlacementRegionCode)
	}
	placement, err := region.ParsePlacementCode(placementCode)
	if err != nil {
		return Credential{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	credential := Credential{FileSystemID: fileSystemID, RegionCode: placement.Code, HasLocalToken: true, APIKey: apiKey}
	if existing, getErr := GetCredential(homeDir, profile.Name, fileSystemID); getErr == nil {
		if existing.RegionCode == credential.RegionCode && existing.APIKey == credential.APIKey {
			return existing, nil
		}
		if !replace {
			return Credential{}, credentialError("fs.credential_import_conflict", profile.Name, fileSystemID, "a different local token or region is already stored")
		}
	} else if apperr.CodeFor(getErr) != "fs.credential_not_found" {
		return Credential{}, getErr
	}
	dir, err := credentialDir(homeDir, profile.Name, fileSystemID)
	if err != nil {
		return Credential{}, err
	}
	if err := ensureCredentialDirs(homeDir, profile.Name, fileSystemID); err != nil {
		return Credential{}, fmt.Errorf("create tdc fs credential directory: %w", err)
	}
	if err := writeTOML(filepath.Join(dir, credsFileName), credential, 0o600); err != nil {
		return Credential{}, err
	}
	stored, err := GetCredential(homeDir, profile.Name, fileSystemID)
	if err != nil {
		return Credential{}, err
	}
	if stored.RegionCode != credential.RegionCode || stored.APIKey != credential.APIKey {
		return Credential{}, credentialError("fs.credential_store_failed", profile.Name, fileSystemID, "stored credential verification failed")
	}
	return stored, nil
}

func GetCredential(homeDir, profileName, fileSystemID string) (Credential, error) {
	fileSystemID, err := ValidateFileSystemID(fileSystemID)
	if err != nil {
		return Credential{}, err
	}
	dir, err := credentialDir(homeDir, profileName, fileSystemID)
	if err != nil {
		return Credential{}, err
	}
	path := filepath.Join(dir, credsFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, credentialError("fs.credential_not_found", profileName, fileSystemID, "local FS credentials are not configured")
	}
	if err != nil {
		return Credential{}, err
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm()&0o077 != 0 {
		if statErr == nil {
			statErr = os.Chmod(path, 0o600)
		}
		if statErr != nil {
			return Credential{}, credentialError("fs.credential_incomplete", profileName, fileSystemID, "cannot restrict credential permissions")
		}
	}
	var credential Credential
	if err := toml.Unmarshal(data, &credential); err != nil {
		return Credential{}, credentialError("fs.credential_incomplete", profileName, fileSystemID, "cannot parse local credentials")
	}
	if credential.FileSystemID != fileSystemID || strings.TrimSpace(credential.APIKey) == "" {
		return Credential{}, credentialError("fs.credential_incomplete", profileName, fileSystemID, "local credentials are incomplete")
	}
	placement, err := region.ParsePlacementCode(credential.RegionCode)
	if err != nil {
		return Credential{}, credentialError("fs.credential_incomplete", profileName, fileSystemID, "stored region_code is invalid")
	}
	credential.RegionCode = placement.Code
	credential.APIKey = strings.TrimSpace(credential.APIKey)
	credential.HasLocalToken = true
	return credential, nil
}

func ListCredentials(homeDir, profileName string) ([]Credential, error) {
	dir := credentialProfileDir(homeDir, profileName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Credential{}, nil
	}
	if err != nil {
		return nil, err
	}
	credentials := make([]Credential, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := decodeKey(entry.Name())
		if err != nil {
			return nil, credentialError("fs.credential_incomplete", profileName, entry.Name(), "invalid credential directory")
		}
		credential, err := GetCredential(homeDir, profileName, id)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return credentials, nil
}

func DeleteCredential(homeDir, profileName, fileSystemID string) (bool, error) {
	if _, err := GetCredential(homeDir, profileName, fileSystemID); err != nil {
		if apperr.CodeFor(err) == "fs.credential_not_found" {
			return false, nil
		}
		return false, err
	}
	dir, err := credentialDir(homeDir, profileName, fileSystemID)
	if err != nil {
		return false, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

func CredentialPath(homeDir, profileName, fileSystemID string) (CredentialPaths, error) {
	dir, err := credentialDir(homeDir, profileName, fileSystemID)
	if err != nil {
		return CredentialPaths{}, err
	}
	return CredentialPaths{Credentials: filepath.Join(dir, credsFileName)}, nil
}

func PrepareCredentialStore(homeDir, profileName string) error {
	root := filepath.Join(homeDir, store.TDCDirName, credentialsDirName)
	profilePath := credentialProfileDir(homeDir, profileName)
	for _, path := range []string{root, profilePath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("prepare tdc fs credential directory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("restrict tdc fs credential directory: %w", err)
		}
	}
	probe, err := os.CreateTemp(profilePath, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("verify tdc fs credential directory is writable: %w", err)
	}
	probePath := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probePath)
		return fmt.Errorf("verify tdc fs credential directory is writable: %w", closeErr)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("remove tdc fs credential write probe: %w", err)
	}
	return nil
}

func ResolveCredential(homeDir string, profile *config.Profile, opts ResolveCredentialOptions) (*config.Profile, Credential, error) {
	if profile == nil {
		return nil, Credential{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	id := strings.TrimSpace(opts.FileSystemID)
	if opts.FileSystemIDExplicit && id == "" {
		return nil, Credential{}, apperr.New("fs.empty_file_system_id", "usage", 2, "--file-system-id cannot be empty")
	}
	if id == "" {
		id = strings.TrimSpace(fsEnvValue(opts.Env, "TDC_FS_FILE_SYSTEM_ID"))
	}
	token := strings.TrimSpace(opts.Token)
	if opts.TokenExplicit && token == "" {
		return nil, Credential{}, apperr.New("fs.empty_token", "usage", 2, "--fs-token cannot be empty")
	}
	if token == "" {
		token = strings.TrimSpace(fsEnvValue(opts.Env, "TDC_FS_TOKEN"))
	}
	explicitToken := token != ""
	if explicitToken {
		tokenID, err := FileSystemIDFromToken(token)
		if err != nil {
			return nil, Credential{}, err
		}
		if id == "" {
			id = tokenID
		} else if id != tokenID {
			return nil, Credential{}, apperr.New("fs.token_file_system_mismatch", "authentication", 3, fmt.Sprintf("FS token belongs to file system %q, not %q", tokenID, id))
		}
	}
	if id == "" {
		return nil, Credential{}, missingFileSystemID()
	}
	var credential Credential
	found := false
	if !opts.DryRun || !explicitToken {
		stored, err := GetCredential(homeDir, profile.Name, id)
		if err == nil {
			credential = stored
			found = true
		} else if apperr.CodeFor(err) != "fs.credential_not_found" {
			return nil, Credential{}, err
		}
	}
	if opts.DryRun && !found {
		legacy := legacyResource(profile)
		if legacy.TenantID == id && legacy.APIKey != "" {
			credential = Credential{FileSystemID: id, RegionCode: legacy.RegionCode, HasLocalToken: true, APIKey: legacy.APIKey}
			found = true
		} else if resources, err := List(homeDir, profile.Name); err == nil {
			for _, resource := range resources {
				if resource.TenantID == id {
					credential = Credential{FileSystemID: id, RegionCode: resource.RegionCode, HasLocalToken: true, APIKey: resource.APIKey}
					found = true
					break
				}
			}
		}
	}
	if token == "" && found {
		token = credential.APIKey
	}
	if opts.TokenRequired && token == "" {
		return nil, Credential{}, apperr.New(
			"auth.missing_fs_api_key",
			"authentication",
			3,
			fmt.Sprintf("authentication required: no local FS token is stored for file system %q; pass --fs-token, set TDC_FS_TOKEN, or import a known token with `tdc fs import-file-system-token`. Token regeneration is not available yet", id),
		)
	}
	placementCode := strings.TrimSpace(opts.RegionOverride)
	if placementCode == "" && found {
		placementCode = credential.RegionCode
	}
	if placementCode == "" {
		placementCode = strings.TrimSpace(profile.PlacementRegionCode)
	}
	if placementCode == "" {
		return nil, Credential{}, apperr.New("fs.missing_region", "config", 2, "tdc fs region is required; pass --region, set TDC_REGION_CODE, or use locally stored credentials with region_code")
	}
	placement, err := region.ParsePlacementCode(placementCode)
	if err != nil {
		return nil, Credential{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	if found && credential.RegionCode != placement.Code {
		return nil, Credential{}, apperr.New("fs.credential_region_mismatch", "config", 2, fmt.Sprintf("file system %q credentials are for %s, not %s", id, credential.RegionCode, placement.Code))
	}
	credential.FileSystemID = id
	credential.RegionCode = placement.Code
	credential.APIKey = token
	credential.HasLocalToken = token != ""
	selected := *profile
	selected.FSResourceName = id
	selected.FSTenantID = id
	selected.FSPlacementRegionCode = placement.Code
	selected.FSCloudProvider = placement.Provider
	selected.FSRegionCode = placement.NativeCode
	selected.FSAPIKey = token
	return &selected, credential, nil
}

func FileSystemIDFromToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	var wrapped string
	switch {
	case strings.HasPrefix(raw, "drive9_"):
		wrapped = strings.TrimPrefix(raw, "drive9_")
	case strings.HasPrefix(raw, "dat9_"):
		wrapped = strings.TrimPrefix(raw, "dat9_")
	default:
		return "", apperr.New("fs.invalid_token", "authentication", 3, "invalid FS token format")
	}
	jwtBytes, err := base64.RawURLEncoding.DecodeString(wrapped)
	if err != nil {
		return "", apperr.Wrap("fs.invalid_token", "authentication", 3, "invalid FS token wrapper", err)
	}
	parts := strings.Split(string(jwtBytes), ".")
	if len(parts) != 3 {
		return "", apperr.New("fs.invalid_token", "authentication", 3, "invalid FS token JWT structure")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", apperr.Wrap("fs.invalid_token", "authentication", 3, "invalid FS token JWT payload", err)
	}
	var claims struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", apperr.Wrap("fs.invalid_token", "authentication", 3, "invalid FS token JWT claims", err)
	}
	if strings.TrimSpace(claims.TenantID) == "" {
		return "", apperr.New("fs.invalid_token", "authentication", 3, "invalid FS token JWT claims: tenant_id is missing")
	}
	id, err := ValidateFileSystemID(claims.TenantID)
	if err != nil {
		return "", apperr.Wrap("fs.invalid_token", "authentication", 3, "invalid FS token JWT tenant_id", err)
	}
	return id, nil
}

func ValidateFileSystemID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", apperr.New("fs.missing_file_system_id", "usage", 2, "--file-system-id is required unless an FS token is supplied")
	}
	if len(id) > 128 || strings.ContainsAny(id, "/\\") {
		return "", apperr.New("fs.invalid_file_system_id", "usage", 2, "file system ID must be 1-128 characters and must not contain path separators")
	}
	for _, r := range id {
		if r < 0x21 || r == 0x7f {
			return "", apperr.New("fs.invalid_file_system_id", "usage", 2, "file system ID must not contain whitespace or control characters")
		}
	}
	return id, nil
}

func MigrateNameRegistry(homeDir string, profile *config.Profile) error {
	if profile == nil {
		return nil
	}
	migrationMu.Lock()
	defer migrationMu.Unlock()
	if err := MigrateLegacy(homeDir, profile); err != nil {
		return err
	}
	resources, err := List(homeDir, profile.Name)
	if err != nil {
		return err
	}
	candidates := make(map[string]Credential, len(resources))
	for _, resource := range resources {
		candidate := Credential{FileSystemID: resource.TenantID, RegionCode: resource.RegionCode, APIKey: resource.APIKey}
		if previous, ok := candidates[resource.TenantID]; ok && (previous.RegionCode != candidate.RegionCode || previous.APIKey != candidate.APIKey) {
			return credentialError("fs.credential_migration_conflict", profile.Name, resource.TenantID, "legacy names contain conflicting credentials for the same file system ID")
		}
		candidates[resource.TenantID] = candidate
	}
	migrated, err := loadMigrationState(homeDir, profile.Name)
	if err != nil {
		return err
	}
	pending := make(map[string]Credential, len(candidates))
	for id, candidate := range candidates {
		if migrated[id] {
			continue
		}
		pending[id] = candidate
	}
	for id, candidate := range pending {
		if existing, err := GetCredential(homeDir, profile.Name, id); err == nil {
			if existing.RegionCode != candidate.RegionCode || existing.APIKey != candidate.APIKey {
				return credentialError("fs.credential_migration_conflict", profile.Name, id, "legacy credentials conflict with the ID-keyed credential")
			}
		} else if apperr.CodeFor(err) != "fs.credential_not_found" {
			return err
		}
	}
	for id, candidate := range pending {
		if _, err := StoreCredential(homeDir, profile, id, candidate.RegionCode, candidate.APIKey, false); err != nil {
			if apperr.CodeFor(err) == "fs.credential_import_conflict" {
				return credentialError("fs.credential_migration_conflict", profile.Name, id, "legacy credentials conflict with the ID-keyed credential")
			}
			return err
		}
	}
	if len(pending) > 0 {
		for id := range pending {
			migrated[id] = true
		}
		if err := writeMigrationState(homeDir, profile.Name, migrated); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrationState(homeDir, profileName string) (map[string]bool, error) {
	path := filepath.Join(credentialProfileDir(homeDir, profileName), migrationStateFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var state migrationState
	if err := toml.Unmarshal(data, &state); err != nil || state.SchemaVersion != migrationStateSchema {
		return nil, credentialError("fs.credential_migration_state_invalid", profileName, "", "cannot parse the legacy registry migration state")
	}
	ids := make(map[string]bool, len(state.FileSystemIDs))
	for _, value := range state.FileSystemIDs {
		id, err := ValidateFileSystemID(value)
		if err != nil {
			return nil, credentialError("fs.credential_migration_state_invalid", profileName, "", "legacy registry migration state contains an invalid file system ID")
		}
		ids[id] = true
	}
	return ids, nil
}

func writeMigrationState(homeDir, profileName string, migrated map[string]bool) error {
	ids := make([]string, 0, len(migrated))
	for id, done := range migrated {
		if done {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	path := filepath.Join(credentialProfileDir(homeDir, profileName), migrationStateFileName)
	if err := writeTOML(path, migrationState{SchemaVersion: migrationStateSchema, FileSystemIDs: ids}, 0o600); err != nil {
		return credentialError("fs.credential_migration_failed", profileName, "", "cannot write the legacy registry migration state")
	}
	return nil
}

func missingFileSystemID() error {
	return apperr.New("fs.missing_file_system_id", "usage", 2, "file system ID is required; pass --file-system-id, set TDC_FS_FILE_SYSTEM_ID, or supply an FS token")
}

func credentialDir(homeDir, profileName, fileSystemID string) (string, error) {
	id, err := ValidateFileSystemID(fileSystemID)
	if err != nil {
		return "", err
	}
	return filepath.Join(credentialProfileDir(homeDir, profileName), encodeKey(id)), nil
}

func credentialProfileDir(homeDir, profileName string) string {
	return filepath.Join(homeDir, store.TDCDirName, credentialsDirName, encodeKey(normalizedProfile(profileName)))
}

func ensureCredentialDirs(homeDir, profileName, fileSystemID string) error {
	root := filepath.Join(homeDir, store.TDCDirName, credentialsDirName)
	profilePath := credentialProfileDir(homeDir, profileName)
	credentialPath, err := credentialDir(homeDir, profileName, fileSystemID)
	if err != nil {
		return err
	}
	for _, path := range []string{root, profilePath, credentialPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func credentialError(code, profileName, fileSystemID, detail string) error {
	message := fmt.Sprintf("%s for profile %q", detail, normalizedProfile(profileName))
	if strings.TrimSpace(fileSystemID) != "" {
		message += fmt.Sprintf(" and file system ID %q", strings.TrimSpace(fileSystemID))
	}
	return apperr.New(code, "config", 2, message)
}
