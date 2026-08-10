package mountstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State struct {
	Schema         string    `json:"schema"`
	Profile        string    `json:"profile"`
	FileSystemName string    `json:"file_system_name"`
	MountPath      string    `json:"mount_path"`
	RemotePath     string    `json:"remote_path"`
	Driver         string    `json:"driver"`
	PID            int       `json:"pid"`
	Endpoint       string    `json:"endpoint"`
	LocalRoot      string    `json:"local_root,omitempty"`
	MountProfile   string    `json:"mount_profile,omitempty"`
	PackPaths      []string  `json:"pack_paths,omitempty"`
	ReadOnly       bool      `json:"read_only,omitempty"`
	ControlSocket  string    `json:"control_socket,omitempty"`
	StartedAt      time.Time `json:"started_at"`
}

const schema = "ti.fs.mount/v1"

func New(profile, fileSystemName, mountPath, remotePath, driver, endpoint string, pid int, readOnly bool, startedAt time.Time) (State, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return State{}, err
	}
	if pid <= 0 {
		return State{}, fmt.Errorf("invalid pid %d", pid)
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return State{
		Schema:         schema,
		Profile:        profile,
		FileSystemName: fileSystemName,
		MountPath:      canonical,
		RemotePath:     remotePath,
		Driver:         driver,
		PID:            pid,
		Endpoint:       endpoint,
		ReadOnly:       readOnly,
		StartedAt:      startedAt.UTC(),
	}, nil
}

func CanonicalMountPath(mountPath string) (string, error) {
	mountPath = strings.TrimSpace(mountPath)
	if mountPath == "" {
		return "", fmt.Errorf("mount path is required")
	}
	cleaned := filepath.Clean(mountPath)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

func Path(homeDir, mountPath string) (string, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join(homeDir, ".ti", "mounts", hex.EncodeToString(sum[:8])+".json"), nil
}

func LogPath(homeDir, mountPath string) (string, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return filepath.Join(homeDir, ".ti", "mounts", hex.EncodeToString(sum[:8])+".log"), nil
}

func ControlSocketPath(mountPath string) (string, error) {
	canonical, err := CanonicalMountPath(mountPath)
	if err != nil {
		return "", err
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	sum := sha256.Sum256([]byte(uid + "\x00" + canonical))
	dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if dir == "" || !filepath.IsAbs(dir) {
		dir = filepath.Join(os.TempDir(), "ti-"+uid)
	}
	return filepath.Join(dir, "ti-mount-"+hex.EncodeToString(sum[:8])+".sock"), nil
}

func Write(homeDir string, state State) (string, error) {
	path, err := Path(homeDir, state.MountPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func Read(homeDir, mountPath string) (State, string, error) {
	path, err := Path(homeDir, mountPath)
	if err != nil {
		return State{}, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, path, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, path, err
	}
	if state.Schema != schema {
		return State{}, path, fmt.Errorf("unsupported mount state schema %q", state.Schema)
	}
	if state.PID <= 0 {
		return State{}, path, fmt.Errorf("invalid mount process pid %d", state.PID)
	}
	return state, path, nil
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
