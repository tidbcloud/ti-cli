package sqlcred

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

const (
	dirMode         os.FileMode = 0o700
	credentialsMode os.FileMode = 0o600
)

var safeClusterIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type AccessMode string

const (
	ReadOnly  AccessMode = "read_only"
	ReadWrite AccessMode = "read_write"
	Admin     AccessMode = "admin"
)

type Credential struct {
	Username string `toml:"username" json:"username"`
	Password string `toml:"password" json:"-"`
}

type Document struct {
	ReadOnly  Credential `toml:"read_only"`
	ReadWrite Credential `toml:"read_write"`
	Admin     Credential `toml:"admin"`
}

func CredentialsPath(homeDir, clusterID string) (string, error) {
	if homeDir == "" {
		return "", apperr.New("db.sql_credentials_missing_home", "config", 2, "home directory is required")
	}
	safeClusterID, err := SafeClusterID(clusterID)
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, store.TIDirName, "db_users", safeClusterID, store.CredsFileName), nil
}

func SafeClusterID(clusterID string) (string, error) {
	trimmed := strings.TrimSpace(clusterID)
	if strings.HasPrefix(trimmed, "clusters/") {
		trimmed = strings.TrimPrefix(trimmed, "clusters/")
	}
	if trimmed == "" ||
		trimmed == "." ||
		trimmed == ".." ||
		strings.Contains(trimmed, "..") ||
		strings.ContainsAny(trimmed, `/\`) ||
		!safeClusterIDPattern.MatchString(trimmed) ||
		hasWindowsInvalidPathCharacter(trimmed) {
		return "", apperr.New(
			"db.invalid_sql_credentials_cluster_id",
			"usage",
			2,
			"--db-cluster-id must be a single safe TiDB Cloud cluster id path segment",
		)
	}
	return trimmed, nil
}

func Read(homeDir, clusterID string) (Document, error) {
	path, err := CredentialsPath(homeDir, clusterID)
	if err != nil {
		return Document{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Document{}, nil
	}
	if err != nil {
		return Document{}, apperr.Wrap("db.sql_credentials_read", "config", 1, fmt.Sprintf("read DB SQL credentials %s", path), err)
	}
	if err := ensureOwnerOnly(path); err != nil {
		return Document{}, err
	}
	var doc Document
	if err := toml.Unmarshal(data, &doc); err != nil {
		return Document{}, apperr.Wrap("db.sql_credentials_parse", "config", 1, fmt.Sprintf("parse DB SQL credentials %s", path), err)
	}
	return doc, nil
}

func Write(homeDir, clusterID string, doc Document) error {
	path, err := CredentialsPath(homeDir, clusterID)
	if err != nil {
		return err
	}
	data, err := toml.Marshal(doc)
	if err != nil {
		return apperr.Wrap("db.sql_credentials_marshal", "runtime", 1, "marshal DB SQL credentials", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return apperr.Wrap("db.sql_credentials_mkdir", "config", 1, fmt.Sprintf("create DB SQL credentials directory %s", dir), err)
	}
	if err := os.Chmod(filepath.Join(filepath.Dir(filepath.Dir(dir)), "db_users"), dirMode); err == nil {
		// Best-effort chmod for the shared db_users parent. Ignore failures on
		// platforms/filesystems that do not expose POSIX mode bits.
	}
	if err := os.Chmod(dir, dirMode); err != nil && runtime.GOOS != "windows" {
		return apperr.Wrap("db.sql_credentials_chmod_dir", "config", 1, fmt.Sprintf("restrict DB SQL credentials directory %s", dir), err)
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return apperr.Wrap("db.sql_credentials_temp", "config", 1, fmt.Sprintf("create temp DB SQL credentials file for %s", path), err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(credentialsMode); err != nil {
		_ = temp.Close()
		return apperr.Wrap("db.sql_credentials_chmod_temp", "config", 1, fmt.Sprintf("restrict temp DB SQL credentials file %s", tempPath), err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return apperr.Wrap("db.sql_credentials_write_temp", "config", 1, fmt.Sprintf("write temp DB SQL credentials file %s", tempPath), err)
	}
	if err := temp.Close(); err != nil {
		return apperr.Wrap("db.sql_credentials_close_temp", "config", 1, fmt.Sprintf("close temp DB SQL credentials file %s", tempPath), err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return apperr.Wrap("db.sql_credentials_replace", "config", 1, fmt.Sprintf("replace DB SQL credentials file %s", path), err)
	}
	if err := os.Chmod(path, credentialsMode); err != nil && runtime.GOOS != "windows" {
		return apperr.Wrap("db.sql_credentials_chmod", "config", 1, fmt.Sprintf("restrict DB SQL credentials file %s", path), err)
	}
	return nil
}

func (d Document) Credential(mode AccessMode) (Credential, bool) {
	switch mode {
	case ReadOnly:
		return d.ReadOnly, d.ReadOnly.Username != "" && d.ReadOnly.Password != ""
	case ReadWrite:
		return d.ReadWrite, d.ReadWrite.Username != "" && d.ReadWrite.Password != ""
	case Admin:
		return d.Admin, d.Admin.Username != "" && d.Admin.Password != ""
	default:
		return Credential{}, false
	}
}

func (d *Document) SetCredential(mode AccessMode, credential Credential) {
	switch mode {
	case ReadOnly:
		d.ReadOnly = credential
	case ReadWrite:
		d.ReadWrite = credential
	case Admin:
		d.Admin = credential
	}
}

func ensureOwnerOnly(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return apperr.Wrap("db.sql_credentials_stat", "config", 1, fmt.Sprintf("stat DB SQL credentials file %s", path), err)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, credentialsMode); err != nil {
		return apperr.Wrap("db.sql_credentials_chmod", "config", 1, fmt.Sprintf("restrict DB SQL credentials file %s", path), err)
	}
	return nil
}

func hasWindowsInvalidPathCharacter(value string) bool {
	return strings.ContainsAny(value, `<>:"|?*`)
}
