package homemigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

const LegacyDirName = ".tdc"

const migrationMarkerName = ".migrated-from-tdc"

var durableNames = []string{
	"config",
	"credentials",
	".preferences",
	".telemetry-installation-id",
	"db_users",
	"fs_resources",
	"fs_credentials",
}

var installationIDPattern = regexp.MustCompile(`^(?:tdc|ti)_[A-Za-z0-9_-]{22}$`)

type Result struct {
	Status      string   `json:"status"`
	Source      string   `json:"source,omitempty"`
	Destination string   `json:"destination"`
	Copied      []string `json:"copied,omitempty"`
}

type migrationMarker struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
}

type options struct {
	copyRegular   func(string, string, os.FileMode) error
	beforePublish func(string) error
}

func Ensure(homeDir string) (Result, error) {
	return ensure(homeDir, options{})
}

func ensure(homeDir string, opts options) (Result, error) {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return Result{}, migrationError("config.home_migration_failed", "home directory is required", nil)
	}
	legacyRoot := filepath.Join(homeDir, LegacyDirName)
	destination := filepath.Join(homeDir, store.TIDirName)
	legacyInfo, legacyExists, err := lstatOptional(legacyRoot)
	if err != nil {
		return Result{}, migrationError("config.home_migration_failed", fmt.Sprintf("inspect legacy state %s", legacyRoot), err)
	}
	destinationInfo, destinationExists, err := lstatOptional(destination)
	if err != nil {
		return Result{}, migrationError("config.home_migration_failed", fmt.Sprintf("inspect state %s", destination), err)
	}
	result := Result{Status: "not_needed", Destination: destination}
	if legacyExists && destinationExists {
		if validMigrationMarker(destination, legacyRoot) {
			return result, nil
		}
		return Result{}, apperr.New(
			"config.home_migration_conflict",
			"config",
			2,
			fmt.Sprintf("both %s and %s exist; keep one source of truth and remove or move the other before running ti", legacyRoot, destination),
		)
	}
	if destinationExists {
		if !destinationInfo.IsDir() || destinationInfo.Mode()&os.ModeSymlink != 0 {
			return Result{}, unsafeSourceError(destination, "new state path is not a regular directory")
		}
		return result, nil
	}
	if !legacyExists {
		return result, nil
	}
	if !legacyInfo.IsDir() || legacyInfo.Mode()&os.ModeSymlink != 0 {
		return Result{}, unsafeSourceError(legacyRoot, "legacy state path is not a regular directory")
	}
	if activePath, active, err := activeLegacyMount(legacyRoot); err != nil {
		return Result{}, migrationError("config.home_migration_failed", "inspect legacy mount state", err)
	} else if active {
		return Result{}, apperr.New(
			"config.home_migration_active_mount",
			"config",
			2,
			fmt.Sprintf("legacy mount at %q is still active; run `tdc fs drain-file-system --mount-path %s` and `tdc fs unmount-file-system --mount-path %s` with the old executable before retrying", activePath, shellArgument(activePath), shellArgument(activePath)),
		)
	}

	temporary, err := os.MkdirTemp(homeDir, ".ti.migrate-*")
	if err != nil {
		return Result{}, migrationError("config.home_migration_failed", fmt.Sprintf("create migration staging directory beside %s", destination), err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil && runtime.GOOS != "windows" {
		return Result{}, migrationError("config.home_migration_failed", "restrict migration staging directory", err)
	}
	copyRegular := opts.copyRegular
	if copyRegular == nil {
		copyRegular = copyRegularFile
	}
	copied := make([]string, 0, len(durableNames))
	for _, name := range durableNames {
		source := filepath.Join(legacyRoot, name)
		if _, exists, err := lstatOptional(source); err != nil {
			return Result{}, migrationError("config.home_migration_failed", fmt.Sprintf("inspect %s", source), err)
		} else if !exists {
			continue
		}
		if err := copyEntry(source, filepath.Join(temporary, name), name, copyRegular); err != nil {
			return Result{}, err
		}
		copied = append(copied, name)
	}
	markerData, err := json.Marshal(migrationMarker{SchemaVersion: 1, Source: legacyRoot})
	if err != nil {
		return Result{}, migrationError("config.home_migration_failed", "encode migration marker", err)
	}
	markerData = append(markerData, '\n')
	if err := replaceFile(filepath.Join(temporary, migrationMarkerName), markerData, 0o600); err != nil {
		return Result{}, migrationError("config.home_migration_failed", "write migration marker", err)
	}
	if err := validateStaged(temporary); err != nil {
		return Result{}, err
	}
	if opts.beforePublish != nil {
		if err := opts.beforePublish(temporary); err != nil {
			return Result{}, migrationError("config.home_migration_failed", "publish migrated state", err)
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		return Result{}, migrationError("config.home_migration_failed", fmt.Sprintf("publish migrated state at %s", destination), err)
	}
	published = true
	sort.Strings(copied)
	return Result{Status: "migrated", Source: legacyRoot, Destination: destination, Copied: copied}, nil
}

func validMigrationMarker(destination, legacyRoot string) bool {
	path := filepath.Join(destination, migrationMarkerName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var marker migrationMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	return marker.SchemaVersion == 1 && filepath.Clean(marker.Source) == filepath.Clean(legacyRoot)
}

func copyEntry(source, destination, relative string, copyRegular func(string, string, os.FileMode) error) error {
	info, err := os.Lstat(source)
	if err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("inspect %s", source), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return unsafeSourceError(source, "symbolic links are not allowed in migrated state")
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, 0o700); err != nil {
			return migrationError("config.home_migration_failed", fmt.Sprintf("create migration directory %s", destination), err)
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return migrationError("config.home_migration_failed", fmt.Sprintf("read migration directory %s", source), err)
		}
		for _, entry := range entries {
			childRelative := filepath.Join(relative, entry.Name())
			if err := copyEntry(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()), childRelative, copyRegular); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return unsafeSourceError(source, "special files are not allowed in migrated state")
	}
	if secretMigrationPath(relative) && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return unsafeSourceError(source, "credential file permissions allow group or other access")
	}
	mode := migratedMode(relative, info.Mode().Perm())
	if err := copyRegular(source, destination, mode); err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("copy %s", source), err)
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(destination, mode); err != nil && runtime.GOOS != "windows" {
		return err
	}
	ok = true
	return nil
}

func validateStaged(root string) error {
	configPath := filepath.Join(root, "config")
	if data, exists, err := readOptional(configPath); err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("read migrated config %s", configPath), err)
	} else if exists {
		var value map[string]any
		if err := toml.Unmarshal(data, &value); err != nil {
			return migrationError("config.home_migration_failed", fmt.Sprintf("parse migrated config %s", configPath), err)
		}
	}
	credentialsPath := filepath.Join(root, "credentials")
	if data, exists, err := readOptional(credentialsPath); err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("read migrated credentials %s", credentialsPath), err)
	} else if exists {
		normalized, err := store.NormalizeCredentials(data, credentialsPath)
		if err != nil {
			return err
		}
		if err := replaceFile(credentialsPath, normalized, 0o600); err != nil {
			return migrationError("config.home_migration_failed", fmt.Sprintf("rewrite migrated credentials %s", credentialsPath), err)
		}
	}
	preferencesPath := filepath.Join(root, ".preferences")
	if data, exists, err := readOptional(preferencesPath); err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("read migrated preferences %s", preferencesPath), err)
	} else if exists {
		var value map[string]any
		if err := toml.Unmarshal(data, &value); err != nil {
			return migrationError("config.home_migration_failed", fmt.Sprintf("parse migrated preferences %s", preferencesPath), err)
		}
	}
	idPath := filepath.Join(root, ".telemetry-installation-id")
	if data, exists, err := readOptional(idPath); err != nil {
		return migrationError("config.home_migration_failed", fmt.Sprintf("read migrated telemetry identity %s", idPath), err)
	} else if exists && !installationIDPattern.MatchString(strings.TrimSpace(string(data))) {
		return unsafeSourceError(idPath, "telemetry installation identity is invalid")
	}
	for _, tree := range []string{"db_users", "fs_resources", "fs_credentials"} {
		rootPath := filepath.Join(root, tree)
		if err := filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return filepath.SkipDir
			}
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			if filepath.Base(path) != "config" && filepath.Base(path) != "credentials" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var value map[string]any
			return toml.Unmarshal(data, &value)
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return migrationError("config.home_migration_failed", fmt.Sprintf("validate migrated %s", tree), err)
		}
	}
	return nil
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".credentials.normalize-*")
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

func activeLegacyMount(legacyRoot string) (string, bool, error) {
	mountsDir := filepath.Join(legacyRoot, "mounts")
	entries, err := os.ReadDir(mountsDir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(mountsDir, entry.Name()))
		if err != nil {
			return "", false, err
		}
		var state struct {
			MountPath string `json:"mount_path"`
			PID       int    `json:"pid"`
		}
		if json.Unmarshal(data, &state) != nil || strings.TrimSpace(state.MountPath) == "" {
			continue
		}
		if (state.PID > 0 && processAlive(state.PID)) || mountPointActive(state.MountPath) || (state.PID == 0 && locatorWithoutPIDBlocksMigration()) {
			return state.MountPath, true, nil
		}
	}
	return "", false, nil
}

func secretMigrationPath(relative string) bool {
	relative = filepath.Clean(relative)
	if relative == "credentials" || strings.HasPrefix(relative, "db_users"+string(os.PathSeparator)) || strings.HasPrefix(relative, "fs_credentials"+string(os.PathSeparator)) {
		return true
	}
	return strings.HasPrefix(relative, "fs_resources"+string(os.PathSeparator)) && filepath.Base(relative) == "credentials"
}

func migratedMode(relative string, source os.FileMode) os.FileMode {
	if secretMigrationPath(relative) || relative == ".preferences" || relative == ".telemetry-installation-id" {
		return 0o600
	}
	if relative == "config" || filepath.Base(relative) == "config" {
		return 0o644
	}
	if source == 0 {
		return 0o600
	}
	return source & 0o666
}

func lstatOptional(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return info, err == nil, err
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func unsafeSourceError(path, detail string) error {
	return apperr.New("config.home_migration_unsafe_source", "config", 2, fmt.Sprintf("unsafe legacy state at %s: %s", path, detail))
}

func migrationError(code, action string, err error) error {
	if err == nil {
		return apperr.New(code, "config", 1, action)
	}
	return apperr.Wrap(code, "config", 1, action, err)
}

func shellArgument(value string) string {
	return fmt.Sprintf("%q", value)
}
