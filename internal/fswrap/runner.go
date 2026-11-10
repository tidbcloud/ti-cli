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
	"github.com/tidbcloud/ti-cli/internal/credentialenv"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/interrupt"
	"github.com/tidbcloud/ti-cli/internal/version"
)

const (
	defaultCompanionName = "ti-drive9"
	envCompanionBin      = "TI_DRIVE9_BIN"
)

// testControlsAllowed gates TI_DRIVE9_BIN. The companion it selects receives the
// TiDB Cloud key pair and FS credentials, so only local builds that opt in may
// use it. It is a variable so tests can turn the control on.
var testControlsAllowed = func() bool { return version.Current().TestControlsAllowed() }

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

// Probe runs a local capability check without credentials, stdin, or persistent
// companion state. The caller supplies a bounded context.
func (r Runner) Probe(ctx context.Context, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := r.locateCompanion()
	if err != nil {
		return err
	}
	home, err := os.MkdirTemp("", "ti-companion-probe-")
	if err != nil {
		return apperr.Wrap("runtime.probe_failed", "runtime", 1, "cannot prepare the local Runtime capability check", err)
	}
	defer os.RemoveAll(home)
	env, err := r.drive9Env(home, RunOptions{})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	isolateProcess(cmd)
	cmd.Env = env
	err = cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return apperr.Wrap("runtime.companion_incompatible", "runtime", 1, "the installed Runtime companion does not support `ti exec`; update or reinstall ti", err)
	}
	return apperr.Wrap("runtime.companion_failed", "runtime", 1, "cannot run the Runtime companion; update or reinstall ti", err)
}

// RunStreaming forwards output and signals without killing the companion on
// cancellation. The companion owns cancel-and-drain and second-signal detach.
func (r Runner) RunStreaming(ctx context.Context, opts RunOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	path, err := r.locateCompanion()
	if err != nil {
		return err
	}
	homeDir, err := r.homeDir(opts)
	if err != nil {
		return err
	}
	if err := prepareHome(homeDir); err != nil {
		return err
	}
	env, err := r.drive9Env(homeDir, opts)
	if err != nil {
		return err
	}

	cmd := exec.Command(path, opts.Args...)
	cmd.Env = env
	isolateProcess(cmd)
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if r.Debug && r.DebugWriter != nil {
		_, _ = fmt.Fprintln(r.DebugWriter, "ti [DEBUG]: starting Runtime companion")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return apperr.Wrap("runtime.companion_failed", "runtime", 1, "cannot start the Runtime companion; update or reinstall ti", err)
	}
	if err := waitStreaming(ctx, cmd); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return apperr.SilentExit("runtime.exec_exit", exitErr.ExitCode())
		}
		return apperr.New("runtime.outcome_unknown", "runtime", 255, "the Runtime companion ended without a confirmed result; the command may have run, so inspect the workspace before retrying")
	}
	return nil
}

func waitStreaming(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	signals, canceled := interrupt.Signals(ctx), ctx.Done()
	for {
		var sig os.Signal
		select {
		case err := <-done:
			return err
		case sig = <-signals:
		case <-canceled:
			canceled = nil
			if interrupt.FromSignal(ctx) {
				continue
			}
			sig = os.Interrupt
		}
		if err := interruptProcess(cmd.Process, sig); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				return <-done
			}
			// Losing local observation cannot establish that remote work stopped.
			_ = cmd.Process.Kill()
			<-done
			return err
		}
	}
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
		// Windows resolves these names case-insensitively too; the scoped
		// HOME/USERPROFILE values are appended below.
		switch strings.ToUpper(key) {
		case "HOME", "USERPROFILE":
			continue
		}
		if credentialenv.IsCredential(key) {
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

// locateCompanion resolves the ti-drive9 binary and refuses to run a candidate
// that could be planted by another user: the companion receives the TiDB Cloud
// private key, the filesystem API key and vault tokens, so a PATH hijack would
// hand those to an attacker. $PATH is never consulted; the only implicit
// candidates are ti's own directory and the installer-managed ~/.ti/bin, plus a
// validated explicit path. Every candidate must be a regular, executable file
// owned by the current user or root, must not be world writable, must not be
// writable by a group the caller is not in, and must live in a directory with
// the same properties (symlinks are resolved before that check).
func (r Runner) locateCompanion() (string, error) {
	// Explicit configuration is terminal: silently falling back to another
	// binary would hide a stale or tampered setting on a path that receives
	// credentials. Runner.CompanionPath is internal; TI_DRIVE9_BIN is a hidden
	// test control that only local builds which opted in may use.
	if strings.TrimSpace(r.CompanionPath) != "" {
		return resolveExplicitCompanion(r.CompanionPath)
	}
	if testControlsAllowed() {
		if env := strings.TrimSpace(os.Getenv(envCompanionBin)); env != "" {
			return resolveExplicitCompanion(env)
		}
	}

	var attempts []string
	reject := func(candidate string, err error) {
		attempts = append(attempts, fmt.Sprintf("%s: %v", candidate, err))
	}
	// ti's own directory and the installer-managed ~/.ti/bin are the only
	// implicit candidates. $PATH is deliberately not consulted: a directory on
	// PATH is outside ti's control, and this process receives the TiDB Cloud
	// private key, the FS API key and vault tokens.
	for _, dir := range r.trustedCompanionDirs() {
		candidate := filepath.Join(dir, companionBinaryName(defaultCompanionName))
		if err := validateCompanionBinary(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			reject(candidate, err)
		}
	}
	if len(attempts) > 0 {
		return "", apperr.New("fs.companion_untrusted", "runtime", 1, fmt.Sprintf("refusing to run the Drive9 companion from an untrusted location: %s; install ti with its companion or use an absolute path owned by you", strings.Join(attempts, "; ")))
	}
	return "", apperr.New("fs.companion_missing", "runtime", 1, "ti requires the Drive9 companion binary; update or reinstall ti")
}

// resolveExplicitCompanion validates a configured companion path. Credentials are
// handed to this process, so a missing or insecure explicit path is an error
// rather than a cue to fall back to another binary.
func resolveExplicitCompanion(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if !filepath.IsAbs(candidate) {
		return "", apperr.New("fs.companion_untrusted", "runtime", 1, fmt.Sprintf("ti fs companion path %q must be absolute", candidate))
	}
	if err := validateCompanionBinary(candidate); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", apperr.New("fs.companion_missing", "runtime", 1, fmt.Sprintf("ti requires the Drive9 companion binary: configured path %q does not exist; update or reinstall ti", candidate))
		}
		return "", apperr.New("fs.companion_untrusted", "runtime", 1, fmt.Sprintf("refusing to run the Drive9 companion %q: %v", candidate, err))
	}
	return candidate, nil
}

var errCompanionPathNotAbsolute = errors.New("path must be absolute")

// trustedCompanionDirs lists the directories ti owns and trusts implicitly.
func (r Runner) trustedCompanionDirs() []string {
	dirs := make([]string, 0, 2)
	if exe, err := os.Executable(); err == nil && exe != "" {
		dirs = append(dirs, filepath.Dir(exe))
	}
	home := strings.TrimSpace(r.HomeDir)
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".ti", "bin"))
	}
	return dirs
}

// validateCompanionBinary rejects companion binaries another account could
// replace: it must be an absolute, regular, executable file that is not world
// writable, not writable by an unrelated group, and owned by the current user or
// root. Group-writable files stay usable when the group is one of the caller's
// own groups, so a umask of 002 does not break every install. The directory that
// actually contains the file must pass the same treatment; symlinks are resolved
// first so a link cannot smuggle in a binary from a directory another account can
// rewrite.
func validateCompanionBinary(path string) error {
	if !filepath.IsAbs(path) {
		return errCompanionPathNotAbsolute
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if err := validateCompanionFileMode(info); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if err := validateCompanionDir(filepath.Dir(path)); err != nil {
		return err
	}
	if filepath.Dir(resolved) != filepath.Dir(path) {
		if err := validateCompanionDir(filepath.Dir(resolved)); err != nil {
			return err
		}
	}
	return nil
}

// validateCompanionDir rejects a parent directory another account can use to
// replace the binary, while allowing the group-writable installs a umask of 002
// produces inside the user's own directories.
func validateCompanionDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("parent %s is not a directory", dir)
	}
	return validateCompanionDirMode(info, dir)
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
