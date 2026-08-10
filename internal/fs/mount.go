package fs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/mountcontrol"
	"github.com/tidbcloud/ti-cli/internal/fs/mountdriver"
	"github.com/tidbcloud/ti-cli/internal/fs/mountprocess"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
	"golang.org/x/net/webdav"
)

const (
	defaultFuseReadCacheSizeBytes    = 128 << 20
	defaultFuseReadCacheMaxFileBytes = 4 << 20
	defaultFuseReadCacheTTL          = 30 * time.Second
)

type MountFileSystemOptions struct {
	Profile           *config.Profile
	FileSystemName    string
	MountPath         string
	RemotePath        string
	Driver            string
	Foreground        bool
	ReadOnly          bool
	ReadyTimeout      time.Duration
	CacheDir          string
	ReadCacheMB       int64
	ReadCacheFileMB   int64
	ReadCacheTTL      time.Duration
	WriteBackCache    bool
	MountProfile      string
	LocalRoot         string
	PackPaths         []string
	UnpackArchivePath string
	NoAutoUnpack      bool
}

type UnmountFileSystemOptions struct {
	Profile         *config.Profile
	MountPath       string
	Timeout         time.Duration
	Force           bool
	IgnoreAbsent    bool
	PackArchivePath string
	NoAutoPack      bool
}

type DrainFileSystemOptions struct {
	Profile   *config.Profile
	MountPath string
	Timeout   time.Duration
}

type MountResult struct {
	Status         string               `json:"status"`
	Profile        string               `json:"profile"`
	FileSystemName string               `json:"file_system_id"`
	MountPath      string               `json:"mount_path"`
	RemotePath     string               `json:"remote_path"`
	Driver         string               `json:"driver"`
	PID            int                  `json:"pid,omitempty"`
	StateFile      string               `json:"state_file,omitempty"`
	LogFile        string               `json:"log_file,omitempty"`
	ControlSocket  string               `json:"control_socket,omitempty"`
	Endpoint       *endpoints.Endpoint  `json:"endpoint,omitempty"`
	Checks         []MountRuntimeCheck  `json:"checks,omitempty"`
	Remote         *MountRemoteSnapshot `json:"remote,omitempty"`
	CacheDir       string               `json:"cache_dir,omitempty"`
	WriteBackCache bool                 `json:"write_back_cache"`
	MountProfile   string               `json:"mount_profile,omitempty"`
	LocalRoot      string               `json:"local_root,omitempty"`
	PackPaths      []string             `json:"pack_paths,omitempty"`
}

type MountRemoteSnapshot struct {
	Status   string `json:"status,omitempty"`
	TenantID string `json:"tenant_id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Version  string `json:"version,omitempty"`
}

type MountCacheIdentity struct {
	Profile           string `json:"profile"`
	FileSystemName    string `json:"file_system_name"`
	TenantID          string `json:"tenant_id,omitempty"`
	Endpoint          string `json:"endpoint"`
	RemotePath        string `json:"remote_path"`
	MountPath         string `json:"mount_path"`
	APIKeyFingerprint string `json:"api_key_fingerprint,omitempty"`
}

type UnmountResult struct {
	Status    string              `json:"status"`
	MountPath string              `json:"mount_path"`
	Driver    string              `json:"driver,omitempty"`
	PID       int                 `json:"pid,omitempty"`
	StateFile string              `json:"state_file,omitempty"`
	Checks    []MountRuntimeCheck `json:"checks,omitempty"`
}

type DrainResult struct {
	Status        string                      `json:"status"`
	MountPath     string                      `json:"mount_path"`
	Driver        string                      `json:"driver,omitempty"`
	PID           int                         `json:"pid,omitempty"`
	StateFile     string                      `json:"state_file,omitempty"`
	ControlSocket string                      `json:"control_socket,omitempty"`
	Response      *mountcontrol.DrainResponse `json:"response,omitempty"`
	Checks        []MountRuntimeCheck         `json:"checks,omitempty"`
}

type MountRuntimeCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type backgroundMountRequest struct {
	Executable string
	Args       []string
	Env        []string
	LogFile    string
	StateFile  string
	MountPath  string
	Timeout    time.Duration
}

type backgroundMountStarter func(context.Context, backgroundMountRequest) (int, error)

func (s Service) MountFileSystem(ctx context.Context, opts MountFileSystemOptions) (MountResult, error) {
	return s.drive9MountFileSystem(ctx, opts)
}

func (s Service) DryRunMountFileSystem(ctx context.Context, commandPath string, opts MountFileSystemOptions) (dryrun.Result, error) {
	inputs, err := s.mountInputs(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", inputs.profile.Name)},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSMount)},
		{Name: "fs_resource_credentials", Status: "passed", Message: inputs.fileSystemName},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", inputs.endpoint.Provider, inputs.endpoint.RegionCode)},
	}
	if err := inputs.driver.CheckPrerequisites(); err != nil {
		checks = append(checks, dryrun.Check{Name: "mount_driver", Status: "failed", Message: err.Error()})
	} else {
		checks = append(checks, dryrun.Check{Name: "mount_driver", Status: "passed", Message: inputs.driver.Name()})
	}
	description := "normal execution starts a local ti fs FUSE runtime and mounts it at the requested path"
	if inputs.driver.Name() == "webdav" {
		description = "normal execution starts a local WebDAV bridge and mounts it at the requested path"
	}
	return dryrun.New(
		commandPath,
		"mount_file_system",
		dryrun.RequestSummary{
			Description: description,
			Method:      "GET",
			Path:        "/v1/status",
		},
		checks...,
	), nil
}

func (s Service) UnmountFileSystem(ctx context.Context, opts UnmountFileSystemOptions) (UnmountResult, error) {
	return s.drive9UnmountFileSystem(ctx, opts)
}

func (s Service) autoPackAfterUnmount(ctx context.Context, opts UnmountFileSystemOptions, state mountstate.State, mountPath string) (string, error) {
	if opts.NoAutoPack && strings.TrimSpace(opts.PackArchivePath) == "" {
		return "", nil
	}
	if strings.TrimSpace(state.LocalRoot) == "" {
		return "", nil
	}
	if len(state.PackPaths) == 0 && strings.TrimSpace(opts.PackArchivePath) == "" {
		return "", nil
	}
	profile := opts.Profile
	if profile == nil {
		return "", nil
	}
	result, err := s.PackFileSystem(ctx, PackFileSystemOptions{
		Profile:      profile,
		MountPath:    mountPath,
		ArchivePath:  opts.PackArchivePath,
		LocalRoot:    state.LocalRoot,
		RemoteRoot:   state.RemotePath,
		MountProfile: state.MountProfile,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("packed %d entries to %s", result.Entries, result.ArchivePath), nil
}

func (s Service) DrainFileSystem(ctx context.Context, opts DrainFileSystemOptions) (DrainResult, error) {
	return s.drive9DrainFileSystem(ctx, opts)
}

func (s Service) DryRunDrainFileSystem(ctx context.Context, commandPath string, opts DrainFileSystemOptions) (dryrun.Result, error) {
	profile, err := s.drive9MountLocatorProfile(opts.Profile, opts.MountPath)
	if err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"drain_file_system",
		dryrun.RequestSummary{
			Description: "normal execution delegates mount drain to the resource-scoped ti-drive9 companion runtime",
			Method:      "LOCAL",
			Path:        opts.MountPath,
		},
		dryrun.Check{Name: "mount_locator", Status: "passed", Message: opts.MountPath},
		dryrun.Check{Name: "file_system_id", Status: "passed", Message: profile.FSTenantID},
		dryrun.Check{Name: "region", Status: "passed", Message: profile.FSPlacementRegionCode},
	), nil
}

func (s Service) DryRunUnmountFileSystem(ctx context.Context, commandPath string, opts UnmountFileSystemOptions) (dryrun.Result, error) {
	profile, err := s.drive9MountLocatorProfile(opts.Profile, opts.MountPath)
	if err != nil {
		if opts.IgnoreAbsent && apperr.CodeFor(err) == "fs.mount_locator_not_found" {
			return dryrun.New(
				commandPath,
				"unmount_file_system",
				dryrun.RequestSummary{Description: "no mount locator exists; normal execution would return absent"},
				dryrun.Check{Name: "mount_locator", Status: "warning", Message: opts.MountPath},
			), nil
		}
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"unmount_file_system",
		dryrun.RequestSummary{
			Description: "normal execution delegates unmount to the resource-scoped ti-drive9 companion runtime and removes the locator after success",
			Method:      "LOCAL",
			Path:        opts.MountPath,
		},
		dryrun.Check{Name: "mount_locator", Status: "passed", Message: opts.MountPath},
		dryrun.Check{Name: "file_system_id", Status: "passed", Message: profile.FSTenantID},
		dryrun.Check{Name: "region", Status: "passed", Message: profile.FSPlacementRegionCode},
	), nil
}

func (s Service) readDrainMountState(mountPathInput string) (mountstate.State, string, string, []MountRuntimeCheck, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return mountstate.State{}, "", "", nil, err
	}
	mountPath, err := mountstate.CanonicalMountPath(mountPathInput)
	if err != nil {
		return mountstate.State{}, "", "", nil, apperr.New("fs.missing_mount_path", "usage", 2, "--mount-path is required")
	}
	state, stateFile, err := mountstate.Read(homeDir, mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return mountstate.State{}, "", "", nil, apperr.New("fs.mount_state_not_found", "runtime", 1, fmt.Sprintf("no ti fs mount state found for %q", mountPath))
		}
		return mountstate.State{}, "", "", nil, apperr.Wrap("fs.read_mount_state", "runtime", 1, fmt.Sprintf("read mount state for %q", mountPath), err)
	}
	checks := []MountRuntimeCheck{{Name: "mount_state", Status: "passed", Message: stateFile}}
	return state, stateFile, mountPath, checks, nil
}

func drainTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return mountcontrol.DefaultDrainTimeout
	}
	return timeout
}

type mountInputs struct {
	profile            *config.Profile
	fileSystemName     string
	mountPath          string
	remotePath         string
	driver             mountdriver.Driver
	endpoint           endpoints.Endpoint
	client             *apifs.Client
	readOnly           bool
	stateFile          string
	logFile            string
	homeDir            string
	timeout            time.Duration
	cacheDir           string
	cacheIdentity      MountCacheIdentity
	metadataStore      *fsMetadataStore
	readCacheBytes     int64
	readCacheFileBytes int64
	readCacheTTL       time.Duration
	writeBackCache     bool
	mountProfile       string
	localRoot          string
	packPaths          []string
	unpackArchivePath  string
	noAutoUnpack       bool
}

func (s Service) mountInputs(opts MountFileSystemOptions) (mountInputs, error) {
	if opts.Profile == nil {
		return mountInputs{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	fileSystemName := strings.TrimSpace(opts.FileSystemName)
	if fileSystemName == "" {
		fileSystemName = opts.Profile.FSResourceName
	}
	if fileSystemName == "" {
		return mountInputs{}, apperr.New("fs.missing_file_system_id", "usage", 2, "--file-system-id is required unless an FS token identifies the file system")
	}
	if opts.Profile.FSResourceName != "" && opts.Profile.FSResourceName != fileSystemName {
		return mountInputs{}, apperr.New("fs.file_system_id_mismatch", "usage", 2, fmt.Sprintf("resolved file system ID %q does not match %q", opts.Profile.FSResourceName, fileSystemName))
	}
	mountPath, err := mountstate.CanonicalMountPath(opts.MountPath)
	if err != nil {
		return mountInputs{}, apperr.New("fs.missing_mount_path", "usage", 2, "--mount-path is required")
	}
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.RemotePath))
	if err != nil {
		return mountInputs{}, err
	}
	driver, err := mountdriver.Resolve(strings.TrimSpace(opts.Driver))
	if err != nil {
		return mountInputs{}, apperr.New("fs.invalid_mount_driver", "usage", 2, err.Error())
	}
	endpoint, err := s.resolveFS(opts.Profile)
	if err != nil {
		return mountInputs{}, err
	}
	client, err := s.bearerClient(opts.Profile, endpoint, authz.FSMount, "mount ti fs resource")
	if err != nil {
		return mountInputs{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return mountInputs{}, err
	}
	stateFile, err := mountstate.Path(homeDir, mountPath)
	if err != nil {
		return mountInputs{}, err
	}
	logFile, err := mountstate.LogPath(homeDir, mountPath)
	if err != nil {
		return mountInputs{}, err
	}
	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	identity := mountCacheIdentity(opts.Profile, fileSystemName, mountPath, remotePath, endpoint)
	cacheDir, err := mountCacheDir(homeDir, identity, opts.CacheDir)
	if err != nil {
		return mountInputs{}, err
	}
	metadataStore, err := newFSMetadataStore(homeDir, opts.Profile)
	if err != nil {
		return mountInputs{}, err
	}
	readCacheBytes, err := mountCacheBytes(opts.ReadCacheMB, defaultFuseReadCacheSizeBytes, "--read-cache-size-mb")
	if err != nil {
		return mountInputs{}, err
	}
	readCacheFileBytes, err := mountCacheBytes(opts.ReadCacheFileMB, defaultFuseReadCacheMaxFileBytes, "--read-cache-max-file-mb")
	if err != nil {
		return mountInputs{}, err
	}
	readCacheTTL := opts.ReadCacheTTL
	if readCacheTTL == 0 {
		readCacheTTL = defaultFuseReadCacheTTL
	}
	if readCacheTTL < 0 {
		return mountInputs{}, apperr.New("fs.invalid_read_cache_ttl", "usage", 2, "--read-cache-ttl must be non-negative")
	}
	mountProfile, localRoot, packPaths, err := s.mountOverlayInputs(opts, homeDir, identity)
	if err != nil {
		return mountInputs{}, err
	}
	return mountInputs{
		profile:            opts.Profile,
		fileSystemName:     fileSystemName,
		mountPath:          mountPath,
		remotePath:         remotePath,
		driver:             driver,
		endpoint:           endpoint,
		client:             client,
		readOnly:           opts.ReadOnly,
		stateFile:          stateFile,
		logFile:            logFile,
		homeDir:            homeDir,
		timeout:            timeout,
		cacheDir:           cacheDir,
		cacheIdentity:      identity,
		metadataStore:      metadataStore,
		readCacheBytes:     readCacheBytes,
		readCacheFileBytes: readCacheFileBytes,
		readCacheTTL:       readCacheTTL,
		writeBackCache:     opts.WriteBackCache,
		mountProfile:       mountProfile,
		localRoot:          localRoot,
		packPaths:          packPaths,
		unpackArchivePath:  strings.TrimSpace(opts.UnpackArchivePath),
		noAutoUnpack:       opts.NoAutoUnpack,
	}, nil
}

func (s Service) mountForeground(ctx context.Context, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck) (MountResult, error) {
	switch inputs.driver.Name() {
	case "fuse":
		return s.mountFUSEForeground(ctx, inputs, remote, checks)
	case "webdav":
		return s.mountWebDAVForeground(ctx, inputs, remote, checks)
	default:
		return MountResult{}, apperr.New("fs.invalid_mount_driver", "usage", 2, fmt.Sprintf("unsupported ti fs mount driver %q", inputs.driver.Name()))
	}
}

func (s Service) autoUnpackMount(ctx context.Context, inputs mountInputs) (string, error) {
	if inputs.noAutoUnpack && strings.TrimSpace(inputs.unpackArchivePath) == "" {
		return "", nil
	}
	if strings.TrimSpace(inputs.localRoot) == "" {
		return "", nil
	}
	if len(inputs.packPaths) == 0 && strings.TrimSpace(inputs.unpackArchivePath) == "" {
		return "", nil
	}
	archivePath := strings.TrimSpace(inputs.unpackArchivePath)
	var err error
	if archivePath == "" {
		archivePath, err = defaultPackArchivePath(inputs.remotePath, inputs.mountProfile)
		if err != nil {
			return "", err
		}
	} else {
		archivePath, err = normalizeRemotePath(archivePath)
		if err != nil {
			return "", err
		}
	}
	data, err := inputs.client.ReadFile(ctx, archivePath)
	if err != nil {
		if strings.TrimSpace(inputs.unpackArchivePath) == "" && isNotFound(err) {
			return "default archive not found: " + archivePath, nil
		}
		return "", err
	}
	manifest, err := extractPackArchive(ctx, bytes.NewReader(data), unpackArchiveOptions{
		LocalRoot: inputs.localRoot,
		Replace:   true,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("restored %d entries from %s", len(manifest.Entries), archivePath), nil
}

func (s Service) mountWebDAVForeground(ctx context.Context, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck) (MountResult, error) {
	prefix, err := randomWebDAVPrefix()
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.mount_nonce", "runtime", 1, "create local WebDAV mount prefix", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.mount_listen", "runtime", 1, "start local ti fs WebDAV bridge", err)
	}
	defer listener.Close()

	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: newRemoteWebDAVFS(inputs.client, inputs.remotePath, inputs.readOnly),
		LockSystem: webdav.NewMemLS(),
	}
	server := &http.Server{Handler: handler}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()

	addr := listener.Addr().(*net.TCPAddr)
	serverURL := fmt.Sprintf("http://127.0.0.1:%d%s/", addr.Port, prefix)
	if err := os.MkdirAll(inputs.mountPath, 0o755); err != nil {
		_ = server.Close()
		return MountResult{}, apperr.Wrap("fs.create_mount_path", "runtime", 1, fmt.Sprintf("create mount path %q", inputs.mountPath), err)
	}
	mountCtx, cancelMount := context.WithTimeout(ctx, inputs.timeout)
	if err := inputs.driver.Mount(mountCtx, serverURL, inputs.mountPath); err != nil {
		cancelMount()
		_ = server.Close()
		return MountResult{}, apperr.Wrap("fs.mount_driver", "runtime", 1, err.Error(), err)
	}
	cancelMount()

	state, err := mountstate.New(inputs.profile.Name, inputs.fileSystemName, inputs.mountPath, inputs.remotePath, inputs.driver.Name(), inputs.endpoint.BaseURL, os.Getpid(), inputs.readOnly, time.Now().UTC())
	if err != nil {
		_ = inputs.driver.Unmount(context.Background(), inputs.mountPath)
		_ = server.Close()
		return MountResult{}, apperr.Wrap("fs.mount_state", "runtime", 1, fmt.Sprintf("create mount state for %q", inputs.mountPath), err)
	}
	state.MountProfile = inputs.mountProfile
	state.LocalRoot = inputs.localRoot
	state.PackPaths = append([]string(nil), inputs.packPaths...)
	stateFile, err := mountstate.Write(inputs.homeDir, state)
	if err != nil {
		_ = inputs.driver.Unmount(context.Background(), inputs.mountPath)
		_ = server.Close()
		return MountResult{}, apperr.Wrap("fs.write_mount_state", "runtime", 1, fmt.Sprintf("write mount state for %q", inputs.mountPath), err)
	}
	defer func() {
		_ = mountstate.Remove(inputs.homeDir, inputs.mountPath)
	}()

	signalCh := make(chan os.Signal, 2)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signalCh)
	go func() {
		select {
		case <-ctx.Done():
		case <-signalCh:
		}
		_ = inputs.driver.Unmount(context.Background(), inputs.mountPath)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err = <-serverErr
	if err != nil && err != http.ErrServerClosed {
		_ = inputs.driver.Unmount(context.Background(), inputs.mountPath)
		return MountResult{}, apperr.Wrap("fs.mount_server", "runtime", 1, "ti fs WebDAV bridge stopped unexpectedly", err)
	}
	checks = append(checks, MountRuntimeCheck{Name: "mount_state", Status: "passed", Message: stateFile})
	return mountResult("unmounted", inputs, remote, checks, os.Getpid(), stateFile, ""), nil
}

func (s Service) mountBackground(ctx context.Context, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck) (MountResult, error) {
	executable, err := os.Executable()
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.executable_path", "runtime", 1, "determine ti executable path for background mount", err)
	}
	if err := os.MkdirAll(filepath.Dir(inputs.logFile), 0o700); err != nil {
		return MountResult{}, apperr.Wrap("fs.mount_log_dir", "runtime", 1, fmt.Sprintf("create mount log directory %q", filepath.Dir(inputs.logFile)), err)
	}
	args := []string{
		"--profile", inputs.profile.Name,
		"fs", "mount-file-system",
		"--file-system-id", inputs.fileSystemName,
		"--mount-path", inputs.mountPath,
		"--remote-path", inputs.remotePath,
		"--driver", inputs.driver.Name(),
		"--cache-dir", inputs.cacheDir,
		"--read-cache-size-mb", fmt.Sprintf("%d", inputs.readCacheBytes/(1<<20)),
		"--read-cache-max-file-mb", fmt.Sprintf("%d", inputs.readCacheFileBytes/(1<<20)),
		"--read-cache-ttl", inputs.readCacheTTL.String(),
		"--write-back-cache=" + fmt.Sprintf("%t", inputs.writeBackCache),
		"--mount-profile", inputs.mountProfile,
		"--foreground",
	}
	if inputs.localRoot != "" {
		args = append(args, "--local-root", inputs.localRoot)
	}
	if inputs.unpackArchivePath != "" {
		args = append(args, "--unpack-archive-path", inputs.unpackArchivePath)
	}
	if inputs.noAutoUnpack {
		args = append(args, "--no-auto-unpack")
	}
	for _, packPath := range inputs.packPaths {
		args = append(args, "--pack-path", packPath)
	}
	if inputs.readOnly {
		args = append(args, "--read-only")
	}
	pid, err := startBackgroundMount(ctx, backgroundMountRequest{
		Executable: executable,
		Args:       args,
		LogFile:    inputs.logFile,
		StateFile:  inputs.stateFile,
		MountPath:  inputs.mountPath,
		Timeout:    inputs.timeout,
	})
	if err != nil {
		return MountResult{}, err
	}
	checks = append(checks, MountRuntimeCheck{Name: "background_process", Status: "passed", Message: fmt.Sprintf("pid %d", pid)})
	checks = append(checks, MountRuntimeCheck{Name: "mount_state", Status: "passed", Message: inputs.stateFile})
	return mountResult("mounted", inputs, remote, checks, pid, inputs.stateFile, inputs.logFile), nil
}

func startBackgroundMount(ctx context.Context, request backgroundMountRequest) (int, error) {
	logFile, err := os.OpenFile(request.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, apperr.Wrap("fs.mount_log", "runtime", 1, fmt.Sprintf("open mount log %q", request.LogFile), err)
	}
	defer logFile.Close()
	cmd := exec.CommandContext(ctx, request.Executable, request.Args...)
	if len(request.Env) > 0 {
		cmd.Env = append(os.Environ(), request.Env...)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return 0, apperr.Wrap("fs.start_mount_process", "runtime", 1, "start background ti fs mount process", err)
	}
	deadline := time.Now().Add(request.Timeout)
	for {
		if _, err := os.Stat(request.StateFile); err == nil {
			_ = cmd.Process.Release()
			return cmd.Process.Pid, nil
		}
		if !mountprocess.Alive(cmd.Process.Pid) {
			return 0, apperr.New("fs.mount_process_exited", "runtime", 1, fmt.Sprintf("background mount process exited before %q became ready; inspect %s", request.MountPath, request.LogFile))
		}
		if time.Now().After(deadline) {
			_ = mountprocess.Terminate(cmd.Process.Pid)
			return 0, apperr.New("fs.mount_ready_timeout", "runtime", 1, fmt.Sprintf("ti fs mount at %q did not become ready within %s; inspect %s", request.MountPath, request.Timeout, request.LogFile))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mountResult(status string, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck, pid int, stateFile, logFile string) MountResult {
	controlSocket := ""
	if state, _, err := mountstate.Read(inputs.homeDir, inputs.mountPath); err == nil {
		controlSocket = state.ControlSocket
	}
	snapshot := MountRemoteSnapshot{Status: remote.Status, TenantID: remote.TenantID, Kind: remote.Kind, Version: remote.Version}
	return MountResult{
		Status:         status,
		Profile:        inputs.profile.Name,
		FileSystemName: inputs.fileSystemName,
		MountPath:      inputs.mountPath,
		RemotePath:     inputs.remotePath,
		Driver:         inputs.driver.Name(),
		PID:            pid,
		StateFile:      stateFile,
		LogFile:        logFile,
		ControlSocket:  controlSocket,
		Endpoint:       &inputs.endpoint,
		Checks:         checks,
		Remote:         &snapshot,
		CacheDir:       inputs.cacheDir,
		WriteBackCache: inputs.writeBackCache,
		MountProfile:   inputs.mountProfile,
		LocalRoot:      inputs.localRoot,
		PackPaths:      append([]string(nil), inputs.packPaths...),
	}
}

func mountCacheDir(homeDir string, identity MountCacheIdentity, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(strings.TrimSpace(explicit))
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		identity.Profile,
		identity.FileSystemName,
		identity.TenantID,
		identity.Endpoint,
		identity.RemotePath,
		identity.MountPath,
		identity.APIKeyFingerprint,
	}, "\x00")))
	return filepath.Join(homeDir, ".ti", "cache", "mounts", hex.EncodeToString(sum[:8])), nil
}

func mountCacheIdentity(profile *config.Profile, fileSystemName, mountPath, remotePath string, endpoint endpoints.Endpoint) MountCacheIdentity {
	identity := MountCacheIdentity{
		FileSystemName: fileSystemName,
		Endpoint:       endpoint.BaseURL,
		RemotePath:     remotePath,
		MountPath:      mountPath,
	}
	if profile != nil {
		identity.Profile = profile.Name
		identity.TenantID = profile.FSTenantID
		if strings.TrimSpace(profile.FSAPIKey) != "" {
			sum := sha256.Sum256([]byte(profile.FSAPIKey))
			identity.APIKeyFingerprint = hex.EncodeToString(sum[:8])
		}
	}
	return identity
}

func (s Service) mountOverlayInputs(opts MountFileSystemOptions, homeDir string, identity MountCacheIdentity) (string, string, []string, error) {
	profileConfig, err := loadPackProfileConfig(opts.MountProfile)
	if err != nil {
		return "", "", nil, err
	}
	packPaths := mergeMountPackPaths(profileConfig.PackPaths, opts.PackPaths)
	localRoot := strings.TrimSpace(opts.LocalRoot)
	if profileConfig.Name != noneMountProfile {
		if localRoot == "" {
			localRoot = defaultMountLocalRoot(homeDir, identity)
		}
		var err error
		localRoot, err = validatePackLocalRoot(localRoot, "--local-root")
		if err != nil {
			return "", "", nil, err
		}
		if err := os.MkdirAll(filepath.Join(localRoot, "overlay"), 0o755); err != nil {
			return "", "", nil, apperr.Wrap("fs.mount_prepare_local_root", "runtime", 1, fmt.Sprintf("prepare local overlay root %q", localRoot), err)
		}
	} else if localRoot != "" || len(packPaths) > 0 {
		return "", "", nil, apperr.New("fs.invalid_mount_profile", "usage", 2, "--local-root and --pack-path require --mount-profile coding-agent or portable")
	}
	return profileConfig.Name, localRoot, packPaths, nil
}

func defaultMountLocalRoot(homeDir string, identity MountCacheIdentity) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		identity.Profile,
		identity.FileSystemName,
		identity.TenantID,
		identity.Endpoint,
		identity.RemotePath,
		identity.APIKeyFingerprint,
	}, "\x00")))
	return filepath.Join(homeDir, ".ti", "local", "fs", hex.EncodeToString(sum[:8]))
}

func mergeMountPackPaths(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func mountCacheBytes(value, fallback int64, flagName string) (int64, error) {
	if value < 0 {
		return 0, apperr.New("fs.invalid_mount_cache_size", "usage", 2, flagName+" must be non-negative")
	}
	if value == 0 {
		return fallback, nil
	}
	return value << 20, nil
}

func randomWebDAVPrefix() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "/ti-" + hex.EncodeToString(buf[:]), nil
}

func statusMessage(value string) string {
	if strings.TrimSpace(value) == "" {
		return "reachable"
	}
	return value
}

func (r MountResult) Human() string {
	lines := []string{
		"Status: " + r.Status,
		"Mount path: " + r.MountPath,
		"Remote path: " + r.RemotePath,
		"Driver: " + r.Driver,
	}
	if r.PID > 0 {
		lines = append(lines, fmt.Sprintf("PID: %d", r.PID))
	}
	if r.StateFile != "" {
		lines = append(lines, "State file: "+r.StateFile)
	}
	if r.LogFile != "" {
		lines = append(lines, "Log file: "+r.LogFile)
	}
	if r.ControlSocket != "" {
		lines = append(lines, "Control socket: "+r.ControlSocket)
	}
	return strings.Join(lines, "\n")
}

func (r UnmountResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "STATUS\tMOUNT_PATH\tDRIVER\tPID")
	_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%d\n", r.Status, r.MountPath, r.Driver, r.PID)
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r DrainResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "STATUS\tMOUNT_PATH\tDRIVER\tPID\tDURATION_MS")
	duration := int64(0)
	if r.Response != nil {
		duration = r.Response.DurationMS
	}
	_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\n", r.Status, r.MountPath, r.Driver, r.PID, duration)
	_ = writer.Flush()
	if r.Response != nil {
		p := r.Response.Pending
		_, _ = fmt.Fprintf(&out, "pending: open_handles=%d dirty_handles=%d commit_queue=%d commit_bytes=%d commit_in_flight=%d commit_delayed=%d commit_conflicts=%d uploader_queued=%d uploader_in_flight=%d uploader_cached=%d\n",
			p.OpenHandles,
			p.DirtyHandles,
			p.CommitQueuePending,
			p.CommitQueueBytes,
			p.CommitQueueInFlight,
			p.CommitQueueDelayed,
			p.CommitQueueConflicts,
			p.UploaderQueued,
			p.UploaderInFlight,
			p.UploaderCached,
		)
	}
	return strings.TrimRight(out.String(), "\n")
}
