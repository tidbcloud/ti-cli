package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
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
		return "", apperr.New("fs.missing_profile", "config", 2, "active profile is required")
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
	// drive9 normalizes the remote root before hashing and drops a trailing
	// slash (verified: /workspace/repo and /workspace/repo/ derive the same
	// directory), while ti's normalizer preserves it.
	if trimmed := strings.TrimSuffix(remoteRoot, "/"); trimmed != "" {
		remoteRoot = trimmed
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

// companionCacheRoot mirrors os.UserCacheDir exactly as the companion resolves
// it. The companion runs with HOME (and USERPROFILE) set to the resource-scoped
// companion home, so the Unix and darwin branches resolve under that home;
// Windows reads LOCALAPPDATA, which ti does not change. XDG_CACHE_HOME applies
// only to the default Unix branch and is used verbatim: a relative value must
// flow through so scanOverlayEntries rejects the non-absolute root loudly instead
// of auditing a plausible but wrong directory.
func companionCacheRoot(companionHome string) string {
	switch runtime.GOOS {
	case "windows":
		if dir, err := os.UserCacheDir(); err == nil && dir != "" {
			return dir
		}
		return filepath.Join(companionHome, ".cache")
	case "darwin":
		return filepath.Join(companionHome, "Library", "Caches")
	case "plan9":
		return filepath.Join(companionHome, "lib", "cache")
	case "js":
		return "/cache"
	default:
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			return xdg
		}
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
