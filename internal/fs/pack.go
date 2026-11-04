package fs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

const (
	defaultMountProfile     = "coding-agent"
	noneMountProfile        = "none"
	portableMountProfile    = "portable"
	packArchiveFormat       = "ti.pack.v1"
	drive9PackArchiveFormat = "drive9.pack.v1"
	packManifestEntryName   = ".ti-pack-manifest.json"
	drive9ManifestEntryName = ".drive9-pack-manifest.json"
	packArchiveEntryPrefix  = "entries/"
	defaultPackRoot         = "/.ti/packs"
)

type PackFileSystemOptions struct {
	Profile      *config.Profile
	LocalRoot    string
	RemoteRoot   string
	MountPath    string
	MountProfile string
	ArchivePath  string
	Paths        []string
}

type UnpackFileSystemOptions struct {
	Profile      *config.Profile
	LocalRoot    string
	RemoteRoot   string
	MountPath    string
	MountProfile string
	ArchivePath  string
	NoReplace    bool
}

type PackFileSystemResult struct {
	Status           string    `json:"status"`
	ArchivePath      string    `json:"archive_path"`
	LocalRoot        string    `json:"local_root"`
	RemoteRoot       string    `json:"remote_root"`
	MountProfile     string    `json:"mount_profile"`
	Paths            []string  `json:"paths"`
	ReplacePaths     []string  `json:"replace_paths,omitempty"`
	Entries          int       `json:"entries"`
	ArchiveSizeBytes int64     `json:"archive_size_bytes"`
	UploadedBytes    int64     `json:"uploaded_bytes"`
	UploadMode       string    `json:"upload_mode,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type UnpackFileSystemResult struct {
	Status       string    `json:"status"`
	ArchivePath  string    `json:"archive_path"`
	LocalRoot    string    `json:"local_root"`
	RemoteRoot   string    `json:"remote_root,omitempty"`
	MountProfile string    `json:"mount_profile,omitempty"`
	Paths        []string  `json:"paths"`
	ReplacePaths []string  `json:"replace_paths,omitempty"`
	Entries      int       `json:"entries"`
	Replaced     bool      `json:"replaced"`
	CreatedAt    time.Time `json:"created_at"`
}

func (r PackFileSystemResult) Human() string {
	return strings.Join([]string{
		"Status: " + r.Status,
		"Archive path: " + r.ArchivePath,
		"Local root: " + r.LocalRoot,
		"Remote root: " + r.RemoteRoot,
		fmt.Sprintf("Entries: %d", r.Entries),
		fmt.Sprintf("Archive bytes: %d", r.ArchiveSizeBytes),
		fmt.Sprintf("Uploaded bytes: %d", r.UploadedBytes),
	}, "\n")
}

func (r UnpackFileSystemResult) Human() string {
	return strings.Join([]string{
		"Status: " + r.Status,
		"Archive path: " + r.ArchivePath,
		"Local root: " + r.LocalRoot,
		"Remote root: " + r.RemoteRoot,
		fmt.Sprintf("Entries: %d", r.Entries),
		fmt.Sprintf("Replaced: %t", r.Replaced),
	}, "\n")
}

type packProfileConfig struct {
	Name      string
	PackPaths []string
}

type packManifest struct {
	Format       string              `json:"format"`
	Version      int                 `json:"version"`
	CreatedAt    time.Time           `json:"created_at"`
	Profile      string              `json:"profile,omitempty"`
	RemoteRoot   string              `json:"remote_root,omitempty"`
	Paths        []string            `json:"paths"`
	ReplacePaths []string            `json:"replace_paths,omitempty"`
	Entries      []packManifestEntry `json:"entries"`
}

type packManifestEntry struct {
	Path       string `json:"path"`
	RemotePath string `json:"remote_path,omitempty"`
	Type       string `json:"type"`
	Mode       uint32 `json:"mode,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Linkname   string `json:"linkname,omitempty"`
	ModTime    int64  `json:"mtime,omitempty"`
}

type packArchiveOptions struct {
	LocalRoot            string
	RemoteRoot           string
	LocalPrefix          string
	MountProfile         string
	Paths                []string
	ProfilePackPaths     []string
	PreviousReplacePaths []string
}

type unpackArchiveOptions struct {
	LocalRoot string
	Replace   bool
}

type packSource struct {
	ArchivePath string
	RemotePath  string
	LocalPath   string
}

type packItem struct {
	ArchivePath string
	RemotePath  string
	LocalPath   string
	Info        iofs.FileInfo
	Linkname    string
	Type        string
}

type packDirTime struct {
	Path    string
	ModTime time.Time
}

func (s Service) PackFileSystem(ctx context.Context, opts PackFileSystemOptions) (PackFileSystemResult, error) {
	return s.drive9PackFileSystem(ctx, opts)
}

func (s Service) UnpackFileSystem(ctx context.Context, opts UnpackFileSystemOptions) (UnpackFileSystemResult, error) {
	return s.drive9UnpackFileSystem(ctx, opts)
}

func (s Service) DryRunPackFileSystem(ctx context.Context, commandPath string, opts PackFileSystemOptions) (dryrun.Result, error) {
	request, err := s.resolvePackFileSystemRequest(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	if _, err := s.dataClient(opts.Profile, authz.FSFileWrite, "pack ti fs local overlay"); err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"pack_file_system",
		dryrun.RequestSummary{
			Description: "normal execution creates a tar.gz archive from local-root/overlay and uploads it as a ti fs file",
			Method:      "PUT",
			Path:        request.ArchivePath,
			Body: map[string]any{
				"local_root":         request.LocalRoot,
				"remote_root":        request.RemoteRoot,
				"mount_profile":      request.MountProfile,
				"paths":              request.Paths,
				"profile_pack_paths": request.ProfilePackPaths,
			},
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", opts.Profile.Name)},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", opts.Profile.CloudProvider, opts.Profile.RegionCode)},
		dryrun.Check{Name: "permission_requirement", Status: "passed", Message: string(authz.FSFileWrite)},
		dryrun.Check{Name: "local_root", Status: "passed", Message: request.LocalRoot},
	), nil
}

func (s Service) DryRunUnpackFileSystem(ctx context.Context, commandPath string, opts UnpackFileSystemOptions) (dryrun.Result, error) {
	request, err := s.resolveUnpackFileSystemRequest(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	if _, err := s.dataClient(opts.Profile, authz.FSFileRead, "unpack ti fs local overlay"); err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"unpack_file_system",
		dryrun.RequestSummary{
			Description: "normal execution downloads a ti fs pack archive and restores it into local-root/overlay",
			Method:      "GET",
			Path:        request.ArchivePath,
			Body: map[string]any{
				"local_root":    request.LocalRoot,
				"remote_root":   request.RemoteRoot,
				"mount_profile": request.MountProfile,
				"replace":       !opts.NoReplace,
			},
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", opts.Profile.Name)},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", opts.Profile.CloudProvider, opts.Profile.RegionCode)},
		dryrun.Check{Name: "permission_requirement", Status: "passed", Message: string(authz.FSFileRead)},
		dryrun.Check{Name: "local_root", Status: "passed", Message: request.LocalRoot},
	), nil
}

type resolvedPackFileSystemRequest struct {
	LocalRoot        string
	RemoteRoot       string
	LocalPrefix      string
	MountProfile     string
	ArchivePath      string
	Paths            []string
	ProfilePackPaths []string
}

func (r resolvedPackFileSystemRequest) archiveOptions() packArchiveOptions {
	return packArchiveOptions{
		LocalRoot:        r.LocalRoot,
		RemoteRoot:       r.RemoteRoot,
		LocalPrefix:      r.LocalPrefix,
		MountProfile:     r.MountProfile,
		Paths:            r.Paths,
		ProfilePackPaths: r.ProfilePackPaths,
	}
}

func (s Service) resolvePackFileSystemRequest(opts PackFileSystemOptions) (resolvedPackFileSystemRequest, error) {
	localRoot := strings.TrimSpace(opts.LocalRoot)
	remoteRoot := strings.TrimSpace(opts.RemoteRoot)
	mountProfile := strings.TrimSpace(opts.MountProfile)
	archivePath := strings.TrimSpace(opts.ArchivePath)
	localPrefix := ""
	profilePackPaths := []string(nil)
	if strings.TrimSpace(opts.MountPath) != "" {
		state, err := s.packMountState(opts.MountPath)
		if err != nil {
			return resolvedPackFileSystemRequest{}, err
		}
		if localRoot == "" {
			localRoot = state.LocalRoot
		}
		if remoteRoot == "" {
			remoteRoot = state.RemotePath
		}
		if mountProfile == "" {
			mountProfile = state.MountProfile
		}
		profilePackPaths = append(profilePackPaths, state.PackPaths...)
	}
	profileConfig, err := loadPackProfileConfig(mountProfile)
	if err != nil {
		return resolvedPackFileSystemRequest{}, err
	}
	mountProfile = profileConfig.Name
	if len(profilePackPaths) == 0 {
		profilePackPaths = append(profilePackPaths, profileConfig.PackPaths...)
	}
	localRoot, err = validatePackLocalRoot(localRoot, "--local-root")
	if err != nil {
		return resolvedPackFileSystemRequest{}, err
	}
	remoteRoot, err = normalizeRemotePath(defaultRemotePath(remoteRoot))
	if err != nil {
		return resolvedPackFileSystemRequest{}, err
	}
	if archivePath == "" {
		archivePath, err = defaultPackArchivePath(remoteRoot, mountProfile)
		if err != nil {
			return resolvedPackFileSystemRequest{}, err
		}
	} else {
		archivePath, err = normalizeRemotePath(archivePath)
		if err != nil {
			return resolvedPackFileSystemRequest{}, err
		}
	}
	return resolvedPackFileSystemRequest{
		LocalRoot:        localRoot,
		RemoteRoot:       remoteRoot,
		LocalPrefix:      localPrefix,
		MountProfile:     mountProfile,
		ArchivePath:      archivePath,
		Paths:            append([]string(nil), opts.Paths...),
		ProfilePackPaths: profilePackPaths,
	}, nil
}

func (s Service) resolveUnpackFileSystemRequest(opts UnpackFileSystemOptions) (resolvedPackFileSystemRequest, error) {
	packOpts := PackFileSystemOptions{
		Profile:      opts.Profile,
		LocalRoot:    opts.LocalRoot,
		RemoteRoot:   opts.RemoteRoot,
		MountPath:    opts.MountPath,
		MountProfile: opts.MountProfile,
		ArchivePath:  opts.ArchivePath,
	}
	return s.resolvePackFileSystemRequest(packOpts)
}

func (s Service) packMountState(mountPath string) (mountstate.State, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return mountstate.State{}, err
	}
	state, _, err := mountstate.Read(homeDir, mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return mountstate.State{}, apperr.New("fs.mount_state_not_found", "runtime", 1, fmt.Sprintf("no ti fs mount state found for %q", mountPath))
		}
		return mountstate.State{}, apperr.Wrap("fs.read_mount_state", "runtime", 1, fmt.Sprintf("read mount state for %q", mountPath), err)
	}
	return state, nil
}

func writePackArchive(ctx context.Context, w io.Writer, opts packArchiveOptions) (*packManifest, error) {
	localRoot, err := validatePackLocalRoot(opts.LocalRoot, "--local-root")
	if err != nil {
		return nil, err
	}
	remoteRoot, err := normalizeRemotePath(defaultRemotePath(opts.RemoteRoot))
	if err != nil {
		return nil, err
	}
	if len(opts.Paths) == 0 && len(opts.ProfilePackPaths) == 0 {
		return nil, apperr.New("fs.pack_missing_paths", "usage", 2, "pack-file-system requires --path or a mount profile with pack paths")
	}
	opts.LocalRoot = localRoot
	opts.RemoteRoot = remoteRoot
	sources, err := resolvePackSources(opts)
	if err != nil {
		return nil, err
	}
	items, err := collectPackItems(ctx, sources)
	if err != nil {
		return nil, err
	}
	manifest := packManifest{
		Format:       packArchiveFormat,
		Version:      1,
		CreatedAt:    time.Now().UTC(),
		Profile:      strings.TrimSpace(opts.MountProfile),
		RemoteRoot:   remoteRoot,
		Paths:        packSourcePaths(sources),
		ReplacePaths: packReplacePaths(opts, packSourcePaths(sources)),
		Entries:      packManifestEntries(items),
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := writePackManifest(tw, manifest); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return nil, err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return nil, err
		}
		if err := writePackItem(tw, item); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func readPackArchiveManifest(ctx context.Context, r io.Reader) (*packManifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, apperr.Wrap("fs.pack_read_gzip", "runtime", 1, "open ti fs pack gzip stream", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if errors.Is(err, io.EOF) {
		return nil, apperr.New("fs.invalid_pack_archive", "runtime", 1, "invalid pack archive: missing manifest")
	}
	if err != nil {
		return nil, apperr.Wrap("fs.pack_read_manifest", "runtime", 1, "read ti fs pack manifest entry", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if hdr.Name != packManifestEntryName && hdr.Name != drive9ManifestEntryName {
		return nil, apperr.New("fs.invalid_pack_archive", "runtime", 1, "invalid pack archive: missing leading manifest")
	}
	var manifest packManifest
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		return nil, apperr.Wrap("fs.pack_decode_manifest", "runtime", 1, "decode ti fs pack manifest", err)
	}
	if !supportedPackFormat(manifest.Format) {
		return nil, apperr.New("fs.unsupported_pack_format", "runtime", 1, fmt.Sprintf("unsupported pack format %q", manifest.Format))
	}
	return &manifest, nil
}

func extractPackArchive(ctx context.Context, r io.Reader, opts unpackArchiveOptions) (*packManifest, error) {
	localRoot, err := validatePackLocalRoot(opts.LocalRoot, "--local-root")
	if err != nil {
		return nil, err
	}
	localParent := filepath.Dir(filepath.Clean(localRoot))
	if err := os.MkdirAll(localParent, 0o755); err != nil {
		return nil, apperr.Wrap("fs.unpack_prepare_local_root", "runtime", 1, fmt.Sprintf("prepare local root parent %q", localParent), err)
	}
	stageRoot, err := os.MkdirTemp(localParent, ".ti-unpack-")
	if err != nil {
		return nil, apperr.Wrap("fs.unpack_stage", "runtime", 1, "create staged unpack root", err)
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()
	if err := os.MkdirAll(filepath.Join(stageRoot, "overlay"), 0o755); err != nil {
		return nil, apperr.Wrap("fs.unpack_stage_overlay", "runtime", 1, "prepare staged overlay root", err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, apperr.Wrap("fs.unpack_gzip", "runtime", 1, "open ti fs pack gzip stream", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var manifest packManifest
	sawManifest := false
	var dirTimes []packDirTime
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperr.Wrap("fs.unpack_read_entry", "runtime", 1, "read ti fs pack entry", err)
		}
		if hdr.Name == packManifestEntryName || hdr.Name == drive9ManifestEntryName {
			sawManifest = true
			if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
				return nil, apperr.Wrap("fs.unpack_decode_manifest", "runtime", 1, "decode ti fs pack manifest", err)
			}
			if !supportedPackFormat(manifest.Format) {
				return nil, apperr.New("fs.unsupported_pack_format", "runtime", 1, fmt.Sprintf("unsupported pack format %q", manifest.Format))
			}
			continue
		}
		if !sawManifest {
			return nil, apperr.New("fs.invalid_pack_archive", "runtime", 1, "invalid pack archive: missing leading manifest")
		}
		if err := extractPackEntry(stageRoot, hdr, tr, &dirTimes); err != nil {
			return nil, err
		}
	}
	if !sawManifest {
		return nil, apperr.New("fs.invalid_pack_archive", "runtime", 1, "invalid pack archive: missing manifest")
	}
	for i := len(dirTimes) - 1; i >= 0; i-- {
		_ = os.Chtimes(dirTimes[i].Path, dirTimes[i].ModTime, dirTimes[i].ModTime)
	}
	if err := installStagedPack(localRoot, stageRoot, manifest, opts.Replace); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func readExistingPackReplacePaths(ctx context.Context, client *apifs.Client, archivePath string, opts packArchiveOptions) ([]string, error) {
	if strings.TrimSpace(opts.MountProfile) == noneMountProfile || len(opts.ProfilePackPaths) == 0 {
		return nil, nil
	}
	data, err := client.ReadFile(ctx, archivePath)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	manifest, err := readPackArchiveManifest(ctx, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return manifestReplacePaths(*manifest), nil
}

func writePackManifest(tw *tar.Writer, manifest packManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	hdr := &tar.Header{
		Name:    packManifestEntryName,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: manifest.CreatedAt,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

func writePackItem(tw *tar.Writer, item packItem) error {
	hdr, err := tar.FileInfoHeader(item.Info, item.Linkname)
	if err != nil {
		return apperr.Wrap("fs.pack_header", "runtime", 1, fmt.Sprintf("create pack header for %s", item.ArchivePath), err)
	}
	hdr.Name = packArchiveEntryPrefix + strings.TrimPrefix(item.ArchivePath, "/")
	if item.Type == "file" {
		hdr.Size = item.Info.Size()
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return apperr.Wrap("fs.pack_write_header", "runtime", 1, fmt.Sprintf("write pack header for %s", item.ArchivePath), err)
	}
	if item.Type != "file" {
		return nil
	}
	f, err := os.Open(item.LocalPath)
	if err != nil {
		return apperr.Wrap("fs.pack_open_source", "runtime", 1, fmt.Sprintf("open pack source %q", item.LocalPath), err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return apperr.Wrap("fs.pack_write_content", "runtime", 1, fmt.Sprintf("write pack content for %s", item.ArchivePath), err)
	}
	return nil
}

func resolvePackSources(opts packArchiveOptions) ([]packSource, error) {
	var out []packSource
	if len(opts.ProfilePackPaths) > 0 {
		sources, err := resolveProfilePackSources(opts)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	if len(opts.Paths) > 0 {
		sources, err := resolveExplicitPackSources(opts, opts.Paths, false)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	return normalizePackSources(out), nil
}

func resolveProfilePackSources(opts packArchiveOptions) ([]packSource, error) {
	names, explicit, allLocal := splitProfilePackPaths(opts.ProfilePackPaths)
	var out []packSource
	if allLocal {
		sources, err := discoverAllLocalPackSources(opts.LocalRoot, opts.RemoteRoot, opts.LocalPrefix)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	if len(names) > 0 {
		sources, err := discoverNamedPackSources(opts.LocalRoot, opts.RemoteRoot, opts.LocalPrefix, names)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	if len(explicit) > 0 {
		sources, err := resolveExplicitPackSources(opts, explicit, true)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	}
	return normalizePackSources(out), nil
}

func resolveExplicitPackSources(opts packArchiveOptions, paths []string, skipMissing bool) ([]packSource, error) {
	out := make([]packSource, 0, len(paths))
	for _, raw := range paths {
		archivePath, remotePath, err := resolvePackPath(opts.RemoteRoot, opts.LocalPrefix, raw)
		if err != nil {
			return nil, err
		}
		localPath, err := overlayPathForArchivePath(opts.LocalRoot, archivePath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(localPath); err != nil {
			if skipMissing && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, apperr.Wrap("fs.pack_stat_source", "runtime", 1, fmt.Sprintf("stat pack path %s (%s)", remotePath, localPath), err)
		}
		out = append(out, packSource{ArchivePath: archivePath, RemotePath: remotePath, LocalPath: localPath})
	}
	return normalizePackSources(out), nil
}

func discoverAllLocalPackSources(localRoot, remoteRoot, localPrefix string) ([]packSource, error) {
	prefixPath, err := canonicalArchivePath(localPrefix)
	if err != nil {
		return nil, err
	}
	root, err := overlayPathForArchivePath(localRoot, prefixPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, apperr.Wrap("fs.pack_stat_overlay", "runtime", 1, fmt.Sprintf("stat profile pack root %q", root), err)
	}
	if !info.IsDir() {
		return []packSource{{ArchivePath: prefixPath, RemotePath: toRemotePath(remoteRoot, prefixPath), LocalPath: root}}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, apperr.Wrap("fs.pack_read_overlay", "runtime", 1, fmt.Sprintf("read profile pack root %q", root), err)
	}
	out := make([]packSource, 0, len(entries))
	for _, entry := range entries {
		localPath := filepath.Join(root, entry.Name())
		archivePath, err := archivePathForOverlay(localRoot, localPath)
		if err != nil {
			return nil, err
		}
		out = append(out, packSource{ArchivePath: archivePath, RemotePath: toRemotePath(remoteRoot, archivePath), LocalPath: localPath})
	}
	return normalizePackSources(out), nil
}

func discoverNamedPackSources(localRoot, remoteRoot, localPrefix string, packNames []string) ([]packSource, error) {
	prefixPath, err := canonicalArchivePath(localPrefix)
	if err != nil {
		return nil, err
	}
	root, err := overlayPathForArchivePath(localRoot, prefixPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, apperr.Wrap("fs.pack_stat_overlay", "runtime", 1, fmt.Sprintf("stat profile pack root %q", root), err)
	}
	names := map[string]struct{}{}
	for _, name := range packNames {
		names[name] = struct{}{}
	}
	var out []packSource
	err = filepath.WalkDir(root, func(localPath string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if localPath == root {
			return nil
		}
		if _, ok := names[entry.Name()]; !ok {
			return nil
		}
		archivePath, err := archivePathForOverlay(localRoot, localPath)
		if err != nil {
			return err
		}
		out = append(out, packSource{ArchivePath: archivePath, RemotePath: toRemotePath(remoteRoot, archivePath), LocalPath: localPath})
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap("fs.pack_discover_sources", "runtime", 1, "discover mount profile pack paths", err)
	}
	return normalizePackSources(out), nil
}

func collectPackItems(ctx context.Context, sources []packSource) ([]packItem, error) {
	var items []packItem
	seen := map[string]struct{}{}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Lstat(source.LocalPath)
		if err != nil {
			return nil, apperr.Wrap("fs.pack_stat_source", "runtime", 1, fmt.Sprintf("stat pack source %q", source.LocalPath), err)
		}
		if !info.IsDir() {
			item, err := newPackItem(source.ArchivePath, source.RemotePath, source.LocalPath, info)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[item.ArchivePath]; !ok {
				items = append(items, item)
				seen[item.ArchivePath] = struct{}{}
			}
			continue
		}
		err = filepath.WalkDir(source.LocalPath, func(localPath string, entry iofs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(source.LocalPath, localPath)
			if err != nil {
				return err
			}
			archivePath := source.ArchivePath
			if rel != "." {
				archivePath = path.Join(source.ArchivePath, filepath.ToSlash(rel))
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			remotePath := toRemotePath(source.RemotePath, strings.TrimPrefix(strings.TrimPrefix(archivePath, source.ArchivePath), "/"))
			if archivePath == source.ArchivePath {
				remotePath = source.RemotePath
			}
			item, err := newPackItem(archivePath, remotePath, localPath, info)
			if err != nil {
				return err
			}
			if _, ok := seen[item.ArchivePath]; ok {
				return nil
			}
			items = append(items, item)
			seen[item.ArchivePath] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, apperr.Wrap("fs.pack_walk_source", "runtime", 1, fmt.Sprintf("walk pack source %q", source.LocalPath), err)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if strings.Count(items[i].ArchivePath, "/") == strings.Count(items[j].ArchivePath, "/") {
			return items[i].ArchivePath < items[j].ArchivePath
		}
		return strings.Count(items[i].ArchivePath, "/") < strings.Count(items[j].ArchivePath, "/")
	})
	return items, nil
}

func newPackItem(archivePath, remotePath, localPath string, info iofs.FileInfo) (packItem, error) {
	item := packItem{ArchivePath: archivePath, RemotePath: remotePath, LocalPath: localPath, Info: info}
	switch {
	case info.IsDir():
		item.Type = "dir"
	case info.Mode()&os.ModeSymlink != 0:
		linkname, err := os.Readlink(localPath)
		if err != nil {
			return packItem{}, apperr.Wrap("fs.pack_read_symlink", "runtime", 1, fmt.Sprintf("read symlink %q", localPath), err)
		}
		item.Type = "symlink"
		item.Linkname = linkname
	case info.Mode().IsRegular():
		item.Type = "file"
	default:
		return packItem{}, apperr.New("fs.pack_unsupported_file_type", "usage", 2, fmt.Sprintf("unsupported pack source type %s (%s)", localPath, info.Mode()))
	}
	return item, nil
}

func extractPackEntry(localRoot string, hdr *tar.Header, r io.Reader, dirTimes *[]packDirTime) error {
	rel, err := packEntryRel(hdr.Name)
	if err != nil {
		return err
	}
	target, err := safeOverlayTarget(localRoot, rel)
	if err != nil {
		return err
	}
	mode := iofs.FileMode(hdr.Mode & 0o777)
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := ensureNoSymlinkPath(localRoot, target, true); err != nil {
			return err
		}
		if err := os.MkdirAll(target, mode); err != nil {
			return apperr.Wrap("fs.unpack_mkdir", "runtime", 1, fmt.Sprintf("mkdir unpack target %q", target), err)
		}
		*dirTimes = append(*dirTimes, packDirTime{Path: target, ModTime: hdr.ModTime})
	case tar.TypeReg, 0:
		if err := ensureNoSymlinkPath(localRoot, filepath.Dir(target), true); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return apperr.Wrap("fs.unpack_mkdir_parent", "runtime", 1, fmt.Sprintf("mkdir unpack parent %q", filepath.Dir(target)), err)
		}
		if err := ensureNoSymlinkPath(localRoot, filepath.Dir(target), true); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return apperr.Wrap("fs.unpack_replace_target", "runtime", 1, fmt.Sprintf("replace unpack target %q", target), err)
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return apperr.Wrap("fs.unpack_create_file", "runtime", 1, fmt.Sprintf("create unpack target %q", target), err)
		}
		_, copyErr := io.Copy(f, r)
		closeErr := f.Close()
		if copyErr != nil {
			return apperr.Wrap("fs.unpack_write_file", "runtime", 1, fmt.Sprintf("write unpack target %q", target), copyErr)
		}
		if closeErr != nil {
			return apperr.Wrap("fs.unpack_close_file", "runtime", 1, fmt.Sprintf("close unpack target %q", target), closeErr)
		}
		_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
	case tar.TypeSymlink:
		if err := validatePackSymlinkTarget(localRoot, rel, hdr.Linkname); err != nil {
			return err
		}
		if err := ensureNoSymlinkPath(localRoot, filepath.Dir(target), true); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return apperr.Wrap("fs.unpack_mkdir_parent", "runtime", 1, fmt.Sprintf("mkdir unpack parent %q", filepath.Dir(target)), err)
		}
		if err := ensureNoSymlinkPath(localRoot, filepath.Dir(target), true); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return apperr.Wrap("fs.unpack_replace_symlink", "runtime", 1, fmt.Sprintf("replace unpack symlink %q", target), err)
		}
		if err := os.Symlink(hdr.Linkname, target); err != nil {
			return apperr.Wrap("fs.unpack_create_symlink", "runtime", 1, fmt.Sprintf("create unpack symlink %q", target), err)
		}
	default:
		return apperr.New("fs.unsupported_pack_entry", "runtime", 1, fmt.Sprintf("unsupported pack entry type %d for %s", hdr.Typeflag, hdr.Name))
	}
	return nil
}

func installStagedPack(localRoot, stageRoot string, manifest packManifest, replace bool) error {
	paths := manifest.Paths
	if replace {
		paths = manifestReplacePaths(manifest)
		if err := removeManifestPaths(localRoot, paths); err != nil {
			return err
		}
	}
	for _, archivePath := range paths {
		rel, err := manifestPathRel(archivePath)
		if err != nil {
			return err
		}
		src, err := safeOverlayTarget(stageRoot, rel)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return apperr.Wrap("fs.unpack_stat_staged", "runtime", 1, fmt.Sprintf("stat staged unpack path %q", src), err)
		}
		dst, err := safeOverlayTarget(localRoot, rel)
		if err != nil {
			return err
		}
		if replace {
			if err := moveStagedPath(localRoot, src, dst); err != nil {
				return err
			}
			continue
		}
		if err := mergeStagedPath(localRoot, src, dst); err != nil {
			return err
		}
	}
	return nil
}

func moveStagedPath(localRoot, src, dst string) error {
	if err := ensureNoSymlinkPath(localRoot, filepath.Dir(dst), true); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return apperr.Wrap("fs.unpack_mkdir_parent", "runtime", 1, fmt.Sprintf("mkdir unpack parent %q", filepath.Dir(dst)), err)
	}
	if err := ensureNoSymlinkPath(localRoot, filepath.Dir(dst), true); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return apperr.Wrap("fs.unpack_install_path", "runtime", 1, fmt.Sprintf("install unpack path %q", dst), err)
	}
	return nil
}

func mergeStagedPath(localRoot, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return apperr.Wrap("fs.unpack_stat_staged", "runtime", 1, fmt.Sprintf("stat staged unpack path %q", src), err)
	}
	if info.IsDir() {
		if err := ensureNoSymlinkPath(localRoot, dst, true); err != nil {
			return err
		}
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return apperr.Wrap("fs.unpack_mkdir", "runtime", 1, fmt.Sprintf("mkdir unpack target %q", dst), err)
		}
		if err := ensureNoSymlinkPath(localRoot, dst, true); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return apperr.Wrap("fs.unpack_read_staged", "runtime", 1, fmt.Sprintf("read staged unpack dir %q", src), err)
		}
		for _, entry := range entries {
			if err := mergeStagedPath(localRoot, filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
		return nil
	}
	if err := ensureNoSymlinkPath(localRoot, filepath.Dir(dst), true); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return apperr.Wrap("fs.unpack_mkdir_parent", "runtime", 1, fmt.Sprintf("mkdir unpack parent %q", filepath.Dir(dst)), err)
	}
	if err := ensureNoSymlinkPath(localRoot, filepath.Dir(dst), true); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return apperr.Wrap("fs.unpack_replace_target", "runtime", 1, fmt.Sprintf("replace unpack target %q", dst), err)
	}
	if err := os.Rename(src, dst); err != nil {
		return apperr.Wrap("fs.unpack_install_path", "runtime", 1, fmt.Sprintf("install unpack path %q", dst), err)
	}
	return nil
}

func removeManifestPaths(localRoot string, paths []string) error {
	for _, archivePath := range paths {
		rel, err := manifestPathRel(archivePath)
		if err != nil {
			return err
		}
		target, err := safeOverlayTarget(localRoot, rel)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkPath(localRoot, target, false); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return apperr.Wrap("fs.unpack_remove_replace_path", "runtime", 1, fmt.Sprintf("replace unpack root %q", target), err)
		}
	}
	return nil
}

func packManifestEntries(items []packItem) []packManifestEntry {
	out := make([]packManifestEntry, len(items))
	for i, item := range items {
		mode := uint32(item.Info.Mode().Perm())
		if item.Type == "symlink" {
			mode = 0o777
		}
		out[i] = packManifestEntry{
			Path:       item.ArchivePath,
			RemotePath: item.RemotePath,
			Type:       item.Type,
			Mode:       mode,
			Size:       item.Info.Size(),
			Linkname:   item.Linkname,
			ModTime:    item.Info.ModTime().Unix(),
		}
	}
	return out
}

func packSourcePaths(sources []packSource) []string {
	out := make([]string, len(sources))
	for i, source := range sources {
		out[i] = source.ArchivePath
	}
	return out
}

func packReplacePaths(opts packArchiveOptions, paths []string) []string {
	if strings.TrimSpace(opts.MountProfile) == noneMountProfile {
		return nil
	}
	packNames, explicitPaths, allLocal := splitProfilePackPaths(opts.ProfilePackPaths)
	explicitPaths = append(explicitPaths, opts.Paths...)
	parents := map[string]struct{}{}
	for _, archivePath := range paths {
		parents[path.Dir(archivePath)] = struct{}{}
	}
	if len(parents) == 0 {
		if prefix, err := canonicalArchivePath(opts.LocalPrefix); err == nil && prefix != "/" {
			parents[prefix] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(opts.PreviousReplacePaths)+len(explicitPaths)+len(parents)*len(packNames))
	add := func(archivePath string) {
		canonical, err := canonicalArchivePath(archivePath)
		if err != nil || canonical == "/" {
			return
		}
		if _, ok := seen[canonical]; ok {
			return
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	for _, archivePath := range opts.PreviousReplacePaths {
		add(archivePath)
	}
	if allLocal {
		for _, archivePath := range paths {
			add(archivePath)
		}
	}
	for _, raw := range explicitPaths {
		archivePath, _, err := resolvePackPath(opts.RemoteRoot, opts.LocalPrefix, raw)
		if err == nil {
			add(archivePath)
		}
	}
	for parent := range parents {
		for _, name := range packNames {
			add(path.Join(parent, name))
		}
	}
	sort.Strings(out)
	return out
}

func splitProfilePackPaths(paths []string) (names []string, explicit []string, allLocal bool) {
	seenNames := map[string]struct{}{}
	for _, raw := range paths {
		if profilePackAllLocal(raw) {
			allLocal = true
			continue
		}
		if name, ok := profilePackRootName(raw); ok {
			if _, seen := seenNames[name]; !seen {
				seenNames[name] = struct{}{}
				names = append(names, name)
			}
			continue
		}
		if strings.TrimSpace(raw) != "" {
			explicit = append(explicit, raw)
		}
	}
	sort.Strings(names)
	return names, explicit, allLocal
}

func profilePackAllLocal(raw string) bool {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "/")
	return value == "" || value == "." || value == "/"
}

func profilePackRootName(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, "/")
	if value == "" || value == "." || value == ".." {
		return "", false
	}
	if strings.ContainsAny(value, `/\*?[`) {
		return "", false
	}
	return value, true
}

func normalizePackSources(in []packSource) []packSource {
	sort.Slice(in, func(i, j int) bool {
		if len(in[i].ArchivePath) == len(in[j].ArchivePath) {
			return in[i].ArchivePath < in[j].ArchivePath
		}
		return len(in[i].ArchivePath) < len(in[j].ArchivePath)
	})
	out := make([]packSource, 0, len(in))
	seen := map[string]struct{}{}
	for _, source := range in {
		if _, ok := seen[source.ArchivePath]; ok {
			continue
		}
		skip := false
		for _, existing := range out {
			if source.ArchivePath != existing.ArchivePath && strings.HasPrefix(source.ArchivePath, strings.TrimSuffix(existing.ArchivePath, "/")+"/") {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		seen[source.ArchivePath] = struct{}{}
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ArchivePath < out[j].ArchivePath })
	return out
}

func resolvePackPath(remoteRoot, localPrefix, raw string) (archivePath, remotePath string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", apperr.New("fs.invalid_pack_path", "usage", 2, "pack path is empty")
	}
	prefix, err := canonicalArchivePath(localPrefix)
	if err != nil {
		return "", "", err
	}
	remoteRoot, err = normalizeRemotePath(defaultRemotePath(remoteRoot))
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(raw, "/") {
		if local, ok := remoteToLocalPath(remoteRoot, raw); ok {
			archivePath, err = canonicalArchivePath(local)
			if err != nil {
				return "", "", err
			}
			remotePath, err = normalizeRemotePath(raw)
			return archivePath, remotePath, err
		}
		archivePath, err = canonicalArchivePath(raw)
		if err != nil {
			return "", "", err
		}
		return archivePath, toRemotePath(remoteRoot, archivePath), nil
	}
	archivePath, err = joinArchivePath(prefix, raw)
	if err != nil {
		return "", "", err
	}
	return archivePath, toRemotePath(remoteRoot, archivePath), nil
}

func canonicalArchivePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "/", nil
	}
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", apperr.New("fs.invalid_pack_path", "usage", 2, fmt.Sprintf("invalid pack path %q", value))
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		cleaned = "/"
	}
	for _, part := range strings.Split(strings.Trim(cleaned, "/"), "/") {
		if part == "." || part == ".." {
			return "", apperr.New("fs.invalid_pack_path", "usage", 2, fmt.Sprintf("invalid pack path %q", value))
		}
	}
	return cleaned, nil
}

func joinArchivePath(base, rel string) (string, error) {
	base, err := canonicalArchivePath(base)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", apperr.New("fs.invalid_pack_path", "usage", 2, "pack path is empty")
	}
	if strings.Contains(rel, "\\") {
		return "", apperr.New("fs.invalid_pack_path", "usage", 2, fmt.Sprintf("pack path contains backslash: %q", rel))
	}
	if strings.HasPrefix(rel, "/") {
		return canonicalArchivePath(rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." || part == ".." {
			return "", apperr.New("fs.invalid_pack_path", "usage", 2, fmt.Sprintf("invalid relative pack path %q", rel))
		}
	}
	if base == "/" {
		return canonicalArchivePath("/" + filepath.ToSlash(rel))
	}
	return canonicalArchivePath(strings.TrimSuffix(base, "/") + "/" + filepath.ToSlash(rel))
}

func overlayPathForArchivePath(localRoot, archivePath string) (string, error) {
	archivePath, err := canonicalArchivePath(archivePath)
	if err != nil {
		return "", err
	}
	rel := strings.TrimPrefix(archivePath, "/")
	root := filepath.Join(localRoot, "overlay")
	if rel == "" {
		return root, nil
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

func archivePathForOverlay(localRoot, localPath string) (string, error) {
	root := filepath.Join(localRoot, "overlay")
	rel, err := filepath.Rel(root, localPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "/", nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apperr.New("fs.invalid_pack_source", "usage", 2, fmt.Sprintf("local path %q is outside overlay root %q", localPath, root))
	}
	return canonicalArchivePath("/" + filepath.ToSlash(rel))
}

func packEntryRel(name string) (string, error) {
	if !strings.HasPrefix(name, packArchiveEntryPrefix) {
		return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, fmt.Sprintf("unsupported pack entry name %q", name))
	}
	rel := strings.TrimPrefix(name, packArchiveEntryPrefix)
	if rel == "" {
		return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, "empty pack entry path")
	}
	if strings.ContainsRune(rel, '\x00') || strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") {
		return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, fmt.Sprintf("unsafe pack entry path %q", rel))
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned != rel {
		return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, fmt.Sprintf("unclean pack entry path %q", rel))
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, fmt.Sprintf("unsafe pack entry path %q", rel))
		}
	}
	return rel, nil
}

func safeOverlayTarget(localRoot, rel string) (string, error) {
	overlayRoot := filepath.Join(localRoot, "overlay")
	target := filepath.Join(overlayRoot, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(overlayRoot)
	cleanTarget := filepath.Clean(target)
	if cleanTarget != cleanRoot && !strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator)) {
		return "", apperr.New("fs.invalid_pack_entry", "runtime", 1, fmt.Sprintf("pack entry %q escapes local overlay root", rel))
	}
	return cleanTarget, nil
}

func ensureNoSymlinkPath(localRoot, target string, includeTarget bool) error {
	overlayRoot := filepath.Clean(filepath.Join(localRoot, "overlay"))
	cleanTarget := filepath.Clean(target)
	if cleanTarget != overlayRoot && !strings.HasPrefix(cleanTarget, overlayRoot+string(filepath.Separator)) {
		return apperr.New("fs.invalid_pack_target", "runtime", 1, fmt.Sprintf("pack target %q escapes local overlay root", target))
	}
	if info, err := os.Lstat(overlayRoot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return apperr.Wrap("fs.unpack_stat_path", "runtime", 1, fmt.Sprintf("stat unpack path %q", overlayRoot), err)
		}
	} else if info.Mode()&os.ModeSymlink != 0 {
		return apperr.New("fs.unsafe_unpack_path", "runtime", 1, fmt.Sprintf("refusing to unpack through symlink %s", overlayRoot))
	}
	rel, err := filepath.Rel(overlayRoot, cleanTarget)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	limit := len(parts)
	if !includeTarget {
		limit--
	}
	cur := overlayRoot
	for i := 0; i < limit; i++ {
		cur = filepath.Join(cur, parts[i])
		info, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return apperr.Wrap("fs.unpack_stat_path", "runtime", 1, fmt.Sprintf("stat unpack path %q", cur), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return apperr.New("fs.unsafe_unpack_path", "runtime", 1, fmt.Sprintf("refusing to unpack through symlink %s", cur))
		}
	}
	return nil
}

func manifestReplacePaths(manifest packManifest) []string {
	if len(manifest.ReplacePaths) > 0 {
		return manifest.ReplacePaths
	}
	return manifest.Paths
}

func manifestPathRel(archivePath string) (string, error) {
	canonical, err := canonicalArchivePath(archivePath)
	if err != nil {
		return "", apperr.Wrap("fs.invalid_pack_manifest_path", "runtime", 1, fmt.Sprintf("invalid manifest path %q", archivePath), err)
	}
	rel := strings.TrimPrefix(canonical, "/")
	if rel == "" {
		return "", apperr.New("fs.invalid_pack_manifest_path", "runtime", 1, "refusing to replace local overlay root from pack manifest")
	}
	return rel, nil
}

// validatePackSymlinkTarget rejects pack symlink targets that could place a link
// escaping the local overlay. Packs are remote-supplied, so an absolute target
// or one that resolves outside <local root>/overlay would let the extracted
// tree plant links to paths outside the workspace, which later local writes
// could follow. Targets are resolved relative to the link's own directory and
// only links that stay inside the overlay are accepted.
func validatePackSymlinkTarget(localRoot, rel, linkname string) error {
	if linkname == "" || strings.ContainsRune(linkname, '\x00') || strings.Contains(linkname, "\\") {
		return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("unsafe pack symlink target %q for %q", linkname, rel))
	}
	if strings.HasPrefix(linkname, "/") || filepath.IsAbs(linkname) || filepath.VolumeName(linkname) != "" {
		return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("pack symlink target for %q must be relative, got %q", rel, linkname))
	}
	overlayRoot := filepath.Clean(filepath.Join(localRoot, "overlay"))
	linkDir := filepath.Join(overlayRoot, filepath.FromSlash(path.Dir(rel)))
	resolved := filepath.Clean(filepath.Join(linkDir, filepath.FromSlash(linkname)))
	if resolved != overlayRoot && !strings.HasPrefix(resolved, overlayRoot+string(filepath.Separator)) {
		return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("pack symlink target for %q escapes the local overlay root: %q", rel, linkname))
	}
	return nil
}

// maxOverlaySymlinkHops bounds symlink resolution so a link cycle in the
// unpacked overlay fails closed instead of looping.
const maxOverlaySymlinkHops = 32

// scanOverlaySymlinks indexes the symlinks under <local root>/overlay by
// absolute path. The map lets a link target be resolved through the complete
// installed link graph instead of only lexically.
func scanOverlaySymlinks(localRoot string) (map[string]string, error) {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" || !filepath.IsAbs(localRoot) {
		return nil, nil
	}
	overlayRoot := filepath.Join(localRoot, "overlay")
	info, err := os.Lstat(overlayRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("stat unpacked overlay %q", overlayRoot), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("unpacked overlay %q is a symlink", overlayRoot))
	}
	if !info.IsDir() {
		return nil, nil
	}
	links := map[string]string{}
	walkErr := filepath.WalkDir(overlayRoot, func(linkPath string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("read unpacked symlink %q", linkPath), err)
		}
		links[filepath.Clean(linkPath)] = target
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return links, nil
}

// overlayLinkGraph is the pre-unpack symlink snapshot used to reconstruct the
// state before a companion run. Name lookups inside a directory follow that
// directory's own case-folding semantics: case folding is a per-directory
// property, so the semantics of the overlay's parent say nothing about the
// overlay itself.
type overlayLinkGraph struct {
	byPath map[string]string
	byFold map[string]string
	probe  func(dir string) (folds bool, known bool)
	cache  map[string]dirCaseFolding
}

type dirCaseFolding struct {
	folds bool
	known bool
}

// newOverlayLinkGraph builds a snapshot graph. probe reports whether a directory
// resolves entry names case-insensitively; it may return known=false when that
// cannot be determined.
func newOverlayLinkGraph(links map[string]string, probe func(string) (bool, bool)) overlayLinkGraph {
	graph := overlayLinkGraph{byPath: links, probe: probe}
	if links == nil {
		return graph
	}
	graph.byFold = make(map[string]string, len(links))
	for path, target := range links {
		graph.byFold[strings.ToLower(path)] = target
	}
	graph.cache = map[string]dirCaseFolding{}
	return graph
}

// dirCaseFolding reports the folding rule of dir, caching the probe.
func (g overlayLinkGraph) dirCaseFolding(dir string) (bool, bool) {
	if cached, ok := g.cache[dir]; ok {
		return cached.folds, cached.known
	}
	if g.probe == nil {
		return false, false
	}
	folds, known := g.probe(dir)
	if g.cache != nil {
		g.cache[dir] = dirCaseFolding{folds: folds, known: known}
	}
	return folds, known
}

// linkLookupClass describes how a snapshot lookup resolved.
type linkLookupClass int

const (
	linkAbsent linkLookupClass = iota
	linkFound
	// linkAmbiguous means a case-variant entry matched but the directory's
	// folding rule is unknown, so the match may or may not apply.
	linkAmbiguous
)

// lookup returns the snapshot link for name inside dir.
func (g overlayLinkGraph) lookup(dir, name string) (string, linkLookupClass) {
	if g.byPath == nil {
		return "", linkAbsent
	}
	exact := filepath.Clean(filepath.Join(dir, name))
	if target, ok := g.byPath[exact]; ok {
		return target, linkFound
	}
	if g.byFold == nil {
		return "", linkAbsent
	}
	folded, ok := g.byFold[strings.ToLower(exact)]
	if !ok {
		return "", linkAbsent
	}
	folds, known := g.dirCaseFolding(dir)
	switch {
	case known && folds:
		return folded, linkFound
	case known:
		return "", linkAbsent
	default:
		return folded, linkAmbiguous
	}
}

// canonicalDirEntry returns the stored spelling of name inside dir when the
// entry exists. It is how a case-insensitive directory aliases a differently
// cased ancestor segment back to the name the filesystem actually stores.
func canonicalDirEntry(dir, name string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name, false
	}
	lowered := strings.ToLower(name)
	for _, entry := range entries {
		if entry.Name() == name {
			return name, true
		}
	}
	for _, entry := range entries {
		if strings.ToLower(entry.Name()) == lowered {
			return entry.Name(), true
		}
	}
	return name, false
}

// overlayDirCaseFolding reports whether dir resolves entry names
// case-insensitively, and whether that could be determined. It inspects the
// directory's own entries first, so a read-only directory is still classified,
// and only falls back to creating a probe file inside it.
func overlayDirCaseFolding(dir string) (folds bool, known bool) {
	entries, err := os.ReadDir(dir)
	if err == nil {
		names := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			names[entry.Name()] = struct{}{}
		}
		for _, entry := range entries {
			name := entry.Name()
			upper := strings.ToUpper(name)
			if upper == name {
				continue
			}
			if _, distinct := names[upper]; distinct {
				// A separately stored case-variant entry proves nothing about
				// folding: on a case-sensitive volume it can be another entry,
				// even a hard link to the same inode.
				continue
			}
			if _, err := os.Lstat(filepath.Join(dir, upper)); err == nil {
				return true, true
			} else if errors.Is(err, os.ErrNotExist) {
				return false, true
			}
		}
	}
	probe, err := os.CreateTemp(dir, ".ti-casefold-")
	if err != nil {
		return false, false
	}
	name := probe.Name()
	_ = probe.Close()
	defer os.Remove(name)
	variantName := filepath.Join(dir, strings.ToUpper(filepath.Base(name)))
	if _, err := os.Lstat(name); err != nil {
		return false, false
	}
	if _, err := os.Lstat(variantName); err == nil {
		return true, true
	} else if errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	return false, false
}

// overlayCaseFoldingProbe is a seam so tests can supply folding answers.
var overlayCaseFoldingProbe = overlayDirCaseFolding

// resolveOverlaySymlinkTarget resolves the target of the link at linkPath, applying
// ".." to the already resolved prefix the way the kernel does. A zero graph makes
// the live filesystem decide, so resolution agrees with each directory's own name
// resolution. A snapshot graph reconstructs the pre-run state; ambiguous is set
// when a component matched only through a case variant whose applicability is
// unknown. Non-existent tail components are kept lexically so dangling links still
// resolve.
func resolveOverlaySymlinkTarget(graph overlayLinkGraph, linkPath string) (resolved string, ambiguous bool, err error) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", false, err
	}
	current := filepath.Dir(linkPath)
	pending := strings.Split(filepath.ToSlash(target), "/")
	hops := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		if graph.byPath != nil {
			// Walk the path the way the kernel does: a directory's own folding
			// rule decides the stored spelling of the next component, so an
			// ancestor segment reached through a case alias is canonicalized
			// before the snapshot is consulted.
			if folds, known := graph.dirCaseFolding(current); known && folds {
				if actual, ok := canonicalDirEntry(current, part); ok {
					part = actual
				}
			}
		}
		next := filepath.Join(current, part)
		linkTarget, class := graph.lookup(current, part)
		if class == linkAbsent && graph.byPath == nil {
			if info, lstatErr := os.Lstat(next); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				if liveTarget, ok := linkTargetOf(next); ok {
					linkTarget, class = liveTarget, linkFound
				}
			}
		}
		if class == linkAmbiguous {
			ambiguous = true
		}
		if class != linkAbsent {
			hops++
			if hops > maxOverlaySymlinkHops {
				return "", false, fmt.Errorf("too many symlink hops while resolving %q", linkPath)
			}
			if filepath.IsAbs(linkTarget) {
				current = string(filepath.Separator)
			}
			pending = append(strings.Split(filepath.ToSlash(linkTarget), "/"), pending...)
			continue
		}
		current = next
	}
	return filepath.Clean(current), ambiguous, nil
}

// linkTargetOf reads one symlink target, returning ok=false when the link is gone.
func linkTargetOf(linkPath string) (string, bool) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", false
	}
	return target, true
}

// overlayLinkConfined reports whether the link at linkPath stays inside the
// overlay, and whether that answer is uncertain because a name could only be
// matched through a case variant of unknown applicability. A zero graph resolves
// against the live filesystem; a snapshot graph resolves against the pre-run
// state.
func overlayLinkConfined(localRoot, overlayRoot string, graph overlayLinkGraph, linkPath, target string) (confined bool, ambiguous bool) {
	rel, err := filepath.Rel(overlayRoot, linkPath)
	if err != nil {
		return false, false
	}
	if err := validatePackSymlinkTarget(localRoot, filepath.ToSlash(rel), target); err != nil {
		return false, false
	}
	resolved, ambiguous, err := resolveOverlaySymlinkTarget(graph, linkPath)
	if err != nil {
		return false, false
	}
	return overlayPathWithin(overlayRoot, resolved), ambiguous
}

// overlayPathWithin reports whether path stays inside overlayRoot.
func overlayPathWithin(overlayRoot, path string) bool {
	rel, err := filepath.Rel(overlayRoot, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// auditUnpackedOverlaySymlinks enforces the pack symlink policy on the public
// unpack path. The tar extraction runs inside the ti-drive9 companion, so the
// overlay it produced is verified afterwards: every link whose target is
// absolute or resolves outside the overlay is removed and the command fails
// closed. Resolution follows the live filesystem, so a chain such as `s -> ..`
// plus `a -> s/../outside` is caught even though each target is lexically
// inside the overlay, and case-insensitive volumes are handled the way the
// volume itself resolves names.
//
// before is the snapshot taken right before the companion ran. A link whose text
// did not change is kept only when it was already confined before the run: if the
// archive installed or replaced a component its target passes through, the link
// became unsafe because of this archive and is removed. Links that were already
// unconfined before the run are pre-existing local state and are left alone.
func auditUnpackedOverlaySymlinks(localRoot string, before map[string]string) error {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" || !filepath.IsAbs(localRoot) {
		return apperr.New("fs.unpack_unknown_local_root", "runtime", 1, "cannot audit the unpacked overlay without an absolute local root")
	}
	links, err := scanOverlaySymlinks(localRoot)
	if err != nil || links == nil {
		return err
	}
	overlayRoot := filepath.Join(localRoot, "overlay")
	// The pre-run snapshot must resolve names the same way the volume does,
	// otherwise the before/after comparison disagrees on case-insensitive
	// volumes. When that cannot be determined, the exemption is dropped: a link
	// that is unconfined now is treated as newly unsafe rather than pre-existing.
	beforeGraph := newOverlayLinkGraph(before, overlayCaseFoldingProbe)
	type violation struct {
		path   string
		target string
		rel    string
	}
	var violations []violation
	for linkPath, target := range links {
		rel, relErr := filepath.Rel(overlayRoot, linkPath)
		if relErr != nil {
			return apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("resolve unpacked path %q", linkPath), relErr)
		}
		confinedNow, _ := overlayLinkConfined(localRoot, overlayRoot, overlayLinkGraph{}, linkPath, target)
		if previous, ok := before[linkPath]; ok && previous == target {
			if confinedNow {
				continue
			}
			// The exemption only applies to a link that was definitely already
			// unconfined before this run. An uncertain pre-run answer (unknown
			// directory folding) is not enough, so it fails closed.
			confinedBefore, ambiguousBefore := overlayLinkConfined(localRoot, overlayRoot, beforeGraph, linkPath, target)
			if ambiguousBefore || confinedBefore {
				violations = append(violations, violation{path: linkPath, target: target, rel: rel})
			}
			continue
		}
		if confinedNow {
			continue
		}
		violations = append(violations, violation{path: linkPath, target: target, rel: rel})
	}
	if len(violations) == 0 {
		return nil
	}
	// Decide first, then remove, so one removal cannot change the resolution of
	// another link that is still being audited.
	var details []string
	for _, item := range violations {
		if removeErr := os.Remove(item.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("remove unsafe unpacked symlink %q", item.path), removeErr)
		}
		details = append(details, fmt.Sprintf("%s -> %s", item.rel, item.target))
	}
	sort.Strings(details)
	return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("removed %d unsafe symlink(s) from %s: %s; the remote pack contains links that escape the local overlay", len(details), overlayRoot, strings.Join(details, ", ")))
}

func validatePackLocalRoot(localRoot, flagName string) (string, error) {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" {
		return "", apperr.New("fs.missing_local_root", "usage", 2, flagName+" is required")
	}
	if !filepath.IsAbs(localRoot) {
		return "", apperr.New("fs.invalid_local_root", "usage", 2, flagName+" must be an absolute path")
	}
	return filepath.Clean(localRoot), nil
}

func loadPackProfileConfig(name string) (packProfileConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultMountProfile
	}
	switch name {
	case defaultMountProfile:
		return packProfileConfig{Name: defaultMountProfile}, nil
	case portableMountProfile:
		return packProfileConfig{Name: portableMountProfile, PackPaths: []string{"/"}}, nil
	case noneMountProfile:
		return packProfileConfig{Name: noneMountProfile}, nil
	default:
		return packProfileConfig{}, apperr.New("fs.unknown_mount_profile", "usage", 2, fmt.Sprintf("unknown mount profile %q; valid values are coding-agent, portable, none", name))
	}
}

func defaultPackArchivePath(remoteRoot, mountProfile string) (string, error) {
	remoteRoot, err := normalizeRemotePath(defaultRemotePath(remoteRoot))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(mountProfile) == "" {
		mountProfile = defaultMountProfile
	}
	sum := sha256.Sum256([]byte(remoteRoot + "\x00" + mountProfile))
	name := mountProfile + "-" + hex.EncodeToString(sum[:8]) + ".tar.gz"
	return normalizeRemotePath(path.Join(defaultPackRoot, name))
}

func packArchiveTags(mountProfile string) map[string]string {
	tags := map[string]string{"ti.pack.format": packArchiveFormat}
	if mountProfile = strings.TrimSpace(mountProfile); mountProfile != "" {
		tags["ti.pack.profile"] = mountProfile
	}
	return tags
}

func supportedPackFormat(format string) bool {
	return format == packArchiveFormat || format == drive9PackArchiveFormat
}

func toRemotePath(remoteRoot, localPath string) string {
	remoteRoot, err := normalizeRemotePath(defaultRemotePath(remoteRoot))
	if err != nil {
		remoteRoot = "/"
	}
	localPath, err = canonicalArchivePath(localPath)
	if err != nil {
		localPath = "/"
	}
	if localPath == "/" {
		return remoteRoot
	}
	return path.Join(remoteRoot, strings.TrimPrefix(localPath, "/"))
}

func remoteToLocalPath(remoteRoot, remotePath string) (string, bool) {
	remoteRoot, err := normalizeRemotePath(defaultRemotePath(remoteRoot))
	if err != nil {
		return "", false
	}
	remotePath, err = normalizeRemotePath(remotePath)
	if err != nil {
		return "", false
	}
	if remoteRoot == "/" {
		return remotePath, true
	}
	root := strings.TrimSuffix(remoteRoot, "/")
	if remotePath == root {
		return "/", true
	}
	prefix := root + "/"
	if strings.HasPrefix(remotePath, prefix) {
		return "/" + strings.TrimPrefix(remotePath, prefix), true
	}
	return "", false
}
