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
		if err := validatePackSymlinkTarget(hdr.Linkname); err != nil {
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

func validatePackSymlinkTarget(linkname string) error {
	if linkname == "" || strings.ContainsRune(linkname, '\x00') || strings.Contains(linkname, "\\") {
		return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("unsafe pack symlink target %q", linkname))
	}
	return nil
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
