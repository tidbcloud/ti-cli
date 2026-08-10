package fswrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

const (
	defaultCompanionName = "ti-drive9"
	envCompanionBin      = "TI_DRIVE9_BIN"
	envAllowDrive9       = "TI_ALLOW_STANDALONE_DRIVE9"
)

type Runner struct {
	HomeDir       string
	CompanionPath string
	Resolver      endpoints.Resolver
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Debug         bool
	DebugWriter   io.Writer
}

type RunOptions struct {
	Profile         *config.Profile
	ResourceName    string
	Args            []string
	CaptureStdout   bool
	IncludeTIKeys   bool
	IncludeFSAPIKey bool
	VaultToken      string
}

type Result struct {
	CompanionPath string
	Stdout        []byte
	Stderr        []byte
}

type CompanionInfo struct {
	Path string `json:"path"`
}

func (r Runner) Run(ctx context.Context, opts RunOptions) (Result, error) {
	path, err := r.locateCompanion()
	if err != nil {
		return Result{}, err
	}
	homeDir, err := r.homeDir(opts)
	if err != nil {
		return Result{}, err
	}
	if err := prepareHome(homeDir); err != nil {
		return Result{}, err
	}

	env, err := r.drive9Env(homeDir, opts)
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, path, opts.Args...)
	cmd.Env = env
	if r.Stdin != nil {
		cmd.Stdin = r.Stdin
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if opts.CaptureStdout {
		cmd.Stdout = &stdout
	} else if r.Stdout != nil {
		cmd.Stdout = r.Stdout
	}
	cmd.Stderr = &stderr
	if !opts.CaptureStdout && r.Stderr != nil {
		cmd.Stderr = io.MultiWriter(r.Stderr, &stderr)
	}

	if r.Debug && r.DebugWriter != nil {
		_, _ = fmt.Fprintf(r.DebugWriter, "ti [DEBUG]: running %s %s\n", path, strings.Join(redactArgs(opts.Args), " "))
	}
	if err := cmd.Run(); err != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() > 0 {
			exitCode = exitErr.ExitCode()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = fmt.Sprintf("ti fs companion command failed: %s %s", path, strings.Join(redactArgs(opts.Args), " "))
		}
		return Result{CompanionPath: path, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, apperr.Wrap("fs.companion_failed", "runtime", exitCode, redactRunSecrets(message, opts), err)
	}
	return Result{CompanionPath: path, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func (r Runner) CompanionInfo(ctx context.Context, profile *config.Profile) (CompanionInfo, error) {
	result, err := r.Run(ctx, RunOptions{
		Profile:         profile,
		Args:            []string{"--help"},
		CaptureStdout:   true,
		IncludeFSAPIKey: false,
	})
	if err != nil && len(result.Stdout) == 0 {
		return CompanionInfo{}, err
	}
	path, err := r.locateCompanion()
	if err != nil {
		return CompanionInfo{}, err
	}
	return CompanionInfo{Path: path}, nil
}

func (r Runner) drive9Env(homeDir string, opts RunOptions) ([]string, error) {
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			env = append(env, entry)
			continue
		}
		if strings.HasPrefix(key, "DRIVE9_") {
			continue
		}
		switch key {
		case "HOME", "USERPROFILE",
			"TIDB_CLOUD_PUBLIC_KEY", "TIDB_CLOUD_PRIVATE_KEY", "TI_FS_TOKEN", "TI_VAULT_TOKEN", "TI_FS_API_KEY",
			"TDC_PUBLIC_KEY", "TDC_PRIVATE_KEY", "TDC_FS_TOKEN", "TDC_VAULT_TOKEN":
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+homeDir)
	if runtime.GOOS == "windows" {
		env = append(env, "USERPROFILE="+homeDir)
	}
	if profile := opts.Profile; profile != nil {
		provider, regionCode, placementCode := fsPlacement(profile)
		if provider == "" || regionCode == "" || placementCode == "" {
			return nil, apperr.New("fs.resource_credentials_incomplete", "config", 2, fmt.Sprintf("ti fs resource placement is incomplete for profile %q", profile.Name))
		}
		endpoint, err := r.resolver().ResolveFS(provider, regionCode)
		if err != nil {
			return nil, err
		}
		if endpoint.BaseURL == "" {
			return nil, apperr.New("api.fs_endpoint_missing", "config", 2, fmt.Sprintf("ti fs endpoint is unavailable for %s", placementCode))
		}
		env = append(env, "DRIVE9_SERVER="+endpoint.BaseURL)
		env = append(env, "DRIVE9_REGION_CODE="+placementCode)
		if opts.IncludeFSAPIKey && strings.TrimSpace(profile.FSAPIKey) != "" {
			env = append(env, "DRIVE9_API_KEY="+strings.TrimSpace(profile.FSAPIKey))
		}
		if opts.IncludeTIKeys {
			if strings.TrimSpace(profile.TiDBCloudPublicKey) != "" {
				env = append(env, "DRIVE9_PUBLIC_KEY="+strings.TrimSpace(profile.TiDBCloudPublicKey))
			}
			if strings.TrimSpace(profile.TiDBCloudPrivateKey) != "" {
				env = append(env, "DRIVE9_PRIVATE_KEY="+strings.TrimSpace(profile.TiDBCloudPrivateKey))
			}
		}
	}
	if strings.TrimSpace(opts.VaultToken) != "" {
		env = append(env, "DRIVE9_VAULT_TOKEN="+strings.TrimSpace(opts.VaultToken))
	}
	return env, nil
}

func (r Runner) locateCompanion() (string, error) {
	candidates := make([]string, 0, 5)
	if strings.TrimSpace(r.CompanionPath) != "" {
		candidates = append(candidates, strings.TrimSpace(r.CompanionPath))
	}
	if env := strings.TrimSpace(os.Getenv(envCompanionBin)); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), companionBinaryName(defaultCompanionName)))
	}
	candidates = append(candidates, companionBinaryName(defaultCompanionName))
	if os.Getenv(envAllowDrive9) == "1" {
		candidates = append(candidates, companionBinaryName("drive9"))
	}
	for _, candidate := range candidates {
		path := candidate
		if strings.ContainsRune(candidate, os.PathSeparator) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			continue
		}
		if resolved, err := exec.LookPath(path); err == nil {
			return resolved, nil
		}
	}
	return "", apperr.New("fs.companion_missing", "runtime", 1, "ti fs requires the Drive9 companion binary; reinstall ti or set TI_DRIVE9_BIN to a compatible drive9 binary")
}

func (r Runner) homeDir(opts RunOptions) (string, error) {
	home := strings.TrimSpace(r.HomeDir)
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", apperr.Wrap("fs.home_dir", "config", 1, "cannot determine home directory", err)
		}
	}
	resourceName := strings.TrimSpace(opts.ResourceName)
	profileName := config.DefaultProfile
	if opts.Profile != nil {
		profileName = opts.Profile.Name
		if resourceName == "" {
			resourceName = opts.Profile.FSResourceName
		}
	}
	return fscred.CompanionHome(home, profileName, resourceName)
}

func (r Runner) resolver() endpoints.Resolver {
	if r.Resolver.IsZero() {
		return endpoints.NewResolver()
	}
	return r.Resolver
}

func prepareHome(homeDir string) error {
	if err := os.MkdirAll(filepath.Join(homeDir, ".drive9"), 0o700); err != nil {
		return apperr.Wrap("fs.companion_home", "runtime", 1, "prepare ti fs companion home", err)
	}
	return nil
}

func fsPlacement(profile *config.Profile) (provider, regionCode, placementCode string) {
	if profile == nil {
		return "", "", ""
	}
	provider = strings.TrimSpace(profile.FSCloudProvider)
	regionCode = strings.TrimSpace(profile.FSRegionCode)
	placementCode = strings.TrimSpace(profile.FSPlacementRegionCode)
	if provider == "" {
		provider = strings.TrimSpace(profile.CloudProvider)
	}
	if regionCode == "" {
		regionCode = strings.TrimSpace(profile.RegionCode)
	}
	if placementCode == "" {
		placementCode = strings.TrimSpace(profile.PlacementRegionCode)
	}
	return provider, regionCode, placementCode
}

func companionBinaryName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		return name + ".exe"
	}
	return name
}

func redactArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		switch out[i] {
		case "--api-key", "--tidbcloud-public-key", "--tidbcloud-private-key", "--server":
			if i+1 < len(out) {
				out[i+1] = "<redacted>"
			}
		}
	}
	return out
}

func redactSecrets(message string) string {
	for _, key := range []string{"DRIVE9_API_KEY", "DRIVE9_PUBLIC_KEY", "DRIVE9_PRIVATE_KEY", "DRIVE9_VAULT_TOKEN"} {
		value := os.Getenv(key)
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "<redacted>")
	}
	return message
}

func redactRunSecrets(message string, opts RunOptions) string {
	message = redactSecrets(message)
	if opts.Profile != nil {
		for _, value := range []string{
			opts.Profile.FSAPIKey,
			opts.Profile.TiDBCloudPublicKey,
			opts.Profile.TiDBCloudPrivateKey,
		} {
			if strings.TrimSpace(value) == "" {
				continue
			}
			message = strings.ReplaceAll(message, value, "<redacted>")
		}
	}
	if strings.TrimSpace(opts.VaultToken) != "" {
		message = strings.ReplaceAll(message, opts.VaultToken, "<redacted>")
	}
	return message
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
