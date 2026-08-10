package settings

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

const (
	FileName                          = ".preferences"
	SchemaVersion                     = 1
	DefaultMaxFileMB                  = 10
	DefaultMaxFiles                   = 5
	settingsFileMode      os.FileMode = 0o600
	profileConfigFileMode os.FileMode = 0o644
	tiDirMode             os.FileMode = 0o700
	envLogging                        = "TI_LOGGING"
)

type Document struct {
	SchemaVersion int       `toml:"schema_version,omitempty"`
	Logging       Logging   `toml:"logging,omitempty"`
	Telemetry     Telemetry `toml:"telemetry,omitempty"`
}

type Logging struct {
	Enabled   *bool `toml:"enabled,omitempty"`
	MaxFileMB *int  `toml:"max_file_mb,omitempty"`
	MaxFiles  *int  `toml:"max_files,omitempty"`
}

type Telemetry struct {
	Enabled *bool `toml:"enabled,omitempty"`
}

type LoggingConfig struct {
	Enabled      bool
	MaxFileBytes int64
	MaxFiles     int
}

func Path(homeDir string) string {
	return filepath.Join(homeDir, store.TIDirName, FileName)
}

func Load(homeDir string) (Document, bool, error) {
	homeDir, err := resolveHomeDir(homeDir)
	if err != nil {
		return Document{}, false, err
	}
	if err := migrateLegacyLogging(homeDir); err != nil {
		return Document{}, false, err
	}
	return read(homeDir)
}

func ResolveLogging(homeDir string, env map[string]string) (LoggingConfig, error) {
	cfg := defaultLoggingConfig()
	value, hasOverride, _, resolveErr := envcompat.ResolveNames(env, envLogging, "TDC_LOGGING")
	if resolveErr != nil {
		cfg.Enabled = false
		return cfg, resolveErr
	}
	if hasOverride {
		enabled, valid := parseBool(value)
		if !valid {
			cfg.Enabled = false
			return cfg, fmt.Errorf("invalid %s value", envLogging)
		}
		if !enabled {
			cfg.Enabled = false
			return cfg, nil
		}
	}

	homeDir, err := resolveHomeDir(homeDir)
	if err != nil {
		cfg.Enabled = false
		return cfg, err
	}
	if err := migrateLegacyLogging(homeDir); err != nil {
		cfg.Enabled = false
		return cfg, err
	}

	if hasOverride {
		cfg.Enabled = true
		doc, exists, err := read(homeDir)
		if err != nil {
			cfg.Enabled = false
			return cfg, err
		}
		if exists {
			applyLoggingLimits(&cfg, doc.Logging)
		}
		return cfg, nil
	}

	doc, exists, err := read(homeDir)
	if err != nil {
		cfg.Enabled = false
		return cfg, err
	}
	if exists {
		if doc.Logging.Enabled != nil {
			cfg.Enabled = *doc.Logging.Enabled
		}
		applyLoggingLimits(&cfg, doc.Logging)
	}
	return cfg, nil
}

func defaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Enabled:      true,
		MaxFileBytes: int64(DefaultMaxFileMB) * 1024 * 1024,
		MaxFiles:     DefaultMaxFiles,
	}
}

func applyLoggingLimits(cfg *LoggingConfig, logging Logging) {
	if logging.MaxFileMB != nil {
		cfg.MaxFileBytes = int64(*logging.MaxFileMB) * 1024 * 1024
	}
	if logging.MaxFiles != nil {
		cfg.MaxFiles = *logging.MaxFiles
	}
}

func resolveHomeDir(homeDir string) (string, error) {
	if strings.TrimSpace(homeDir) != "" {
		return homeDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return home, nil
}

func read(homeDir string) (Document, bool, error) {
	path := Path(homeDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, fmt.Errorf("read settings %s: %w", path, err)
	}
	doc, err := decode(data, path)
	if err != nil {
		return Document{}, false, err
	}
	return doc, true, nil
}

func decode(data []byte, path string) (Document, error) {
	var doc Document
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return Document{}, fmt.Errorf("parse settings %s: %w", path, err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if doc.SchemaVersion != SchemaVersion {
		return Document{}, fmt.Errorf("unsupported settings schema_version %d in %s", doc.SchemaVersion, path)
	}
	if err := validateLogging(doc.Logging, path); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func validateLogging(logging Logging, path string) error {
	if logging.MaxFileMB != nil {
		if *logging.MaxFileMB <= 0 || int64(*logging.MaxFileMB) > math.MaxInt64/(1024*1024) {
			return fmt.Errorf("logging.max_file_mb must be a positive supported integer in %s", path)
		}
	}
	if logging.MaxFiles != nil && *logging.MaxFiles <= 0 {
		return fmt.Errorf("logging.max_files must be a positive integer in %s", path)
	}
	return nil
}

func migrateLegacyLogging(homeDir string) error {
	configPath := store.ConfigPath(homeDir)
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy logging config %s: %w", configPath, err)
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse legacy logging config %s: %w", configPath, err)
	}
	legacyValue, exists := raw["logging"]
	if !exists {
		return nil
	}
	legacyMap, ok := legacyValue.(map[string]any)
	if !ok {
		return fmt.Errorf("legacy logging config in %s must be a TOML table", configPath)
	}
	legacy, err := decodeLegacyLogging(legacyMap, configPath)
	if err != nil {
		return err
	}

	_, settingsExists, err := read(homeDir)
	if err != nil {
		return err
	}
	if !settingsExists {
		doc := Document{SchemaVersion: SchemaVersion, Logging: legacy}
		if err := createSettings(homeDir, doc); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			if _, _, readErr := read(homeDir); readErr != nil {
				return readErr
			}
		}
	}

	delete(raw, "logging")
	mode := profileConfigFileMode
	if info, statErr := os.Stat(configPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := writeTOMLAtomic(configPath, raw, mode); err != nil {
		return fmt.Errorf("remove legacy logging config from %s: %w", configPath, err)
	}
	return nil
}

func decodeLegacyLogging(raw map[string]any, path string) (Logging, error) {
	data, err := toml.Marshal(map[string]any{"logging": raw})
	if err != nil {
		return Logging{}, fmt.Errorf("marshal legacy logging config %s: %w", path, err)
	}
	var doc struct {
		Logging Logging `toml:"logging"`
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return Logging{}, fmt.Errorf("parse legacy logging config %s: %w", path, err)
	}
	if err := validateLogging(doc.Logging, path); err != nil {
		return Logging{}, err
	}
	return doc.Logging, nil
}

func createSettings(homeDir string, doc Document) error {
	data, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	dir := filepath.Join(homeDir, store.TIDirName)
	if err := os.MkdirAll(dir, tiDirMode); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".preferences.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(settingsFileMode); err != nil {
		temp.Close()
		return fmt.Errorf("set temporary settings permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary settings: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary settings: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary settings: %w", err)
	}
	if err := os.Link(tempPath, Path(homeDir)); err != nil {
		return fmt.Errorf("publish settings: %w", err)
	}
	return nil
}

func writeTOMLAtomic(path string, value any, mode os.FileMode) error {
	data, err := toml.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "1", "yes":
		return true, true
	case "off", "false", "0", "no":
		return false, true
	default:
		return false, false
	}
}
