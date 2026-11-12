package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

// companionMountLocalRoot returns the local overlay root the ti-drive9 companion
// derives when the caller does not pass --local-root. The formula mirrors
// drive9's defaultMountLocalRoot (cmd/drive9/cli/mount.go): the mount cache
// directory as the companion sees it (its HOME is the resource-scoped companion
// home) plus drive9/mounts/<label>-<hash>, where the hash covers the server, the
// normalized remote root, and the credential fingerprint.
//
// ti must not pass --local-root for this case: doing so would relocate existing
// overlays, because ti's own default (defaultMountLocalRoot) is a different path.
// Verified against drive9 v0.10.0: server https://drive9.example, remote "/", and
// credential "sk-test" produce root-26d2b529bbcb6733 under the companion home.
func (s Service) companionMountLocalRoot(profile *config.Profile, remotePath string) (string, error) {
	if profile == nil {
		return "", nil
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return "", err
	}
	companionHome, err := fscred.CompanionHome(homeDir, profile.Name, profile.FSResourceName)
	if err != nil {
		return "", err
	}
	endpoint, err := s.resolveFS(profile)
	if err != nil {
		return "", err
	}
	return companionMountLocalRootAt(companionHome, endpoint.BaseURL, remotePath, profile.FSAPIKey)
}

func companionMountLocalRootAt(companionHome, server, remotePath, credential string) (string, error) {
	remoteRoot, err := normalizeRemotePath(remotePath)
	if err != nil {
		return "", err
	}
	fingerprint := ""
	if strings.TrimSpace(credential) != "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(credential)))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(server) + "\n" + remoteRoot + "\n" + fingerprint))
	hash := hex.EncodeToString(sum[:8])
	return filepath.Join(companionCacheRoot(companionHome), "drive9", "mounts", companionMountRootLabel(remoteRoot)+"-"+hash), nil
}

// companionCacheRoot is os.UserCacheDir as the companion sees it. On Unix the
// companion runs with HOME set to the resource-scoped companion home, so its
// cache lives under that home; XDG_CACHE_HOME is inherited. On Windows the cache
// still comes from LOCALAPPDATA, which the companion does not change.
func companionCacheRoot(companionHome string) string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" && filepath.IsAbs(xdg) {
		return xdg
	}
	switch runtime.GOOS {
	case "windows":
		if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
			return dir
		}
		return filepath.Join(companionHome, ".cache")
	case "darwin":
		return filepath.Join(companionHome, "Library", "Caches")
	default:
		return filepath.Join(companionHome, ".cache")
	}
}

// companionMountRootLabel mirrors drive9's pathBase plus safePackArchiveLabel.
func companionMountRootLabel(remoteRoot string) string {
	value := strings.TrimSuffix(remoteRoot, "/")
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), ".-")
	if label == "" {
		return "root"
	}
	return label
}
