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
	Schema         string `json:"schema"`
	Profile        string `json:"profile"`
	FileSystemName string `json:"file_system_name"`
	RegionCode     string `json:"region_code"`
	// ServiceAddress is the regional Filesystem endpoint resolved when the mount
	// was created. It is a stable, non-secret placement property that lets drain
	// and unmount rebuild the companion environment without fetching the region
	// catalog. Older locators may omit it.
	ServiceAddress string `json:"service_address,omitempty"`
	CompanionHome  string `json:"companion_home"`
	MountPath      string `json:"mount_path"`
	Kind           string `json:"kind,omitempty"`
	// Driver is the mount driver the companion reported when the mount was
	// created. Unmount uses it to decide whether the mount supports drain:
	// WebDAV mounts have no control socket and no durability tiers. Older
	// locators may omit it, and an omitted driver means "unknown, do not gate".
	Driver           string `json:"driver,omitempty"`
	FileSystemID     string `json:"file_system_id,omitempty"`
	TokenID          string `json:"token_id,omitempty"`
	TokenFingerprint string `json:"token_fingerprint,omitempty"`
	RemotePath       string `json:"remote_path,omitempty"`
	LayerRef         string `json:"layer_ref,omitempty"`
	CheckpointID     string `json:"checkpoint_id,omitempty"`
	ReadOnly         bool   `json:"read_only,omitempty"`
	// PackPaths are the extra local-overlay paths given to
	// `ti fs mount-file-system --pack-path`. The companion's mount has no flag
	// for them, so they are recorded here and replayed as `drive9 umount
	// --pack-path` so the automatic pack includes them. Older locators may omit
	// the field; an omitted field means "none".
	PackPaths []string `json:"pack_paths,omitempty"`
}

func (l Locator) WithLayerView(remotePath, layerRef, checkpointID string, readOnly bool) Locator {
	l.RemotePath = strings.TrimSpace(remotePath)
	l.LayerRef = strings.TrimSpace(layerRef)
	l.CheckpointID = strings.TrimSpace(checkpointID)
	l.ReadOnly = readOnly
	return l
}

// WithPackPaths records the extra pack paths supplied at mount time so unmount
// can hand them back to the companion. Blank entries are dropped and duplicates
// collapse onto their first occurrence, because the companion appends one
// --pack-path flag per entry.
func (l Locator) WithPackPaths(paths []string) Locator {
	l.PackPaths = NormalizePackPaths(paths)
	return l
}

// NormalizePackPaths trims, drops blank entries and removes duplicates while
// preserving the caller's order.
func NormalizePackPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// WithDriver records the mount driver reported by the companion.
func (l Locator) WithDriver(driver string) Locator {
	l.Driver = strings.TrimSpace(driver)
	return l
}

// WithServiceAddress records the resolved regional service address.
func (l Locator) WithServiceAddress(address string) Locator {
	l.ServiceAddress = strings.TrimSpace(address)
	return l
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
