package mountlocator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const schema = "ti.fs.mount-locator/v1"

type Locator struct {
	Schema           string `json:"schema"`
	Profile          string `json:"profile"`
	FileSystemName   string `json:"file_system_name"`
	RegionCode       string `json:"region_code"`
	CompanionHome    string `json:"companion_home"`
	MountPath        string `json:"mount_path"`
	Kind             string `json:"kind,omitempty"`
	FileSystemID     string `json:"file_system_id,omitempty"`
	TokenID          string `json:"token_id,omitempty"`
	TokenFingerprint string `json:"token_fingerprint,omitempty"`
}

func New(profile, fileSystemName, regionCode, companionHome, mountPath, kind string) (Locator, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return Locator{}, err
	}
	if strings.TrimSpace(fileSystemName) == "" {
		return Locator{}, fmt.Errorf("file system name is required")
	}
	if strings.TrimSpace(regionCode) == "" {
		return Locator{}, fmt.Errorf("region code is required")
	}
	if strings.TrimSpace(companionHome) == "" {
		return Locator{}, fmt.Errorf("companion home is required")
	}
	return Locator{
		Schema:         schema,
		Profile:        strings.TrimSpace(profile),
		FileSystemName: strings.TrimSpace(fileSystemName),
		RegionCode:     strings.TrimSpace(regionCode),
		CompanionHome:  strings.TrimSpace(companionHome),
		MountPath:      canonical,
		Kind:           strings.TrimSpace(kind),
	}, nil
}

func (l Locator) WithTokenCorrelation(fileSystemID, tokenID, fingerprint string) Locator {
	l.FileSystemID = strings.TrimSpace(fileSystemID)
	l.TokenID = strings.TrimSpace(tokenID)
	l.TokenFingerprint = strings.TrimSpace(fingerprint)
	return l
}

func List(homeDir string) ([]Locator, error) {
	dir := filepath.Join(homeDir, ".ti", "mounts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Locator{}, nil
	}
	if err != nil {
		return nil, err
	}
	locators := make([]Locator, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".locator.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var locator Locator
		if err := json.Unmarshal(data, &locator); err != nil {
			return nil, err
		}
		if locator.Schema != schema || locator.FileSystemName == "" || locator.MountPath == "" {
			continue
		}
		locators = append(locators, locator)
	}
	return locators, nil
}

func CanonicalMountPath(mountPath string) (string, error) {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" {
		return "", fmt.Errorf("mount path is required")
	}
	return filepath.Abs(filepath.Clean(mountPath))
}

func Path(homeDir, mountPath string) (string, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join(homeDir, ".ti", "mounts", hex.EncodeToString(sum[:8])+".locator.json"), nil
}

func Write(homeDir string, locator Locator) (string, error) {
	path, err := Path(homeDir, locator.MountPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(locator, "", "  ")
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func Read(homeDir, mountPath string) (Locator, string, error) {
	path, err := Path(homeDir, mountPath)
	if err != nil {
		return Locator{}, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Locator{}, path, err
	}
	var locator Locator
	if err := json.Unmarshal(data, &locator); err != nil {
		return Locator{}, path, err
	}
	if locator.Schema != schema {
		return Locator{}, path, fmt.Errorf("unsupported mount locator schema %q", locator.Schema)
	}
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return Locator{}, path, err
	}
	if locator.MountPath != canonical {
		return Locator{}, path, fmt.Errorf("mount locator path mismatch")
	}
	if locator.FileSystemName == "" || locator.RegionCode == "" || locator.CompanionHome == "" {
		return Locator{}, path, fmt.Errorf("mount locator is incomplete")
	}
	return locator, path, nil
}

func Remove(homeDir, mountPath string) error {
	path, err := Path(homeDir, mountPath)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
