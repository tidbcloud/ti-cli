package fs

import (
	"archive/tar"
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

type unpackArchiveOptions struct {
	LocalRoot string
	Replace   bool
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

// overlayPathWithin reports whether path stays inside overlayRoot.
func overlayPathWithin(overlayRoot, path string) bool {
	rel, err := filepath.Rel(overlayRoot, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// overlayEntryKind classifies one path in an overlay snapshot.
type overlayEntryKind int

const (
	overlayKindDir overlayEntryKind = iota
	overlayKindFile
	overlayKindSymlink
)

// overlayEntryInfo is one path of an overlay snapshot. Only the kind and the
// stored symlink target are recorded: attribution compares literal snapshots of
// the same filesystem and never re-models name equivalence.
type overlayEntryInfo struct {
	kind   overlayEntryKind
	target string
}

// scanOverlayEntries records every path under <local root>/overlay with its kind
// and, for links, its stored target. Traversal problems are returned separately
// so a caller can decide whether a partial scan is acceptable.
func scanOverlayEntries(localRoot string) (map[string]overlayEntryInfo, []string, error) {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" || !filepath.IsAbs(localRoot) {
		return nil, nil, apperr.New("fs.unpack_unknown_local_root", "runtime", 1, "cannot audit the unpacked overlay without an absolute local root")
	}
	overlayRoot := filepath.Join(localRoot, "overlay")
	info, err := os.Lstat(overlayRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("stat unpacked overlay %q", overlayRoot), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("unpacked overlay %q is a symlink", overlayRoot))
	}
	if !info.IsDir() {
		return nil, nil, nil
	}
	entries := map[string]overlayEntryInfo{}
	var problems []string
	walkErr := filepath.WalkDir(overlayRoot, func(path string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Keep walking so an unreadable directory cannot hide links that were
			// already collected; the caller fails closed afterwards.
			problems = append(problems, fmt.Sprintf("%s: %v", path, walkErr))
			return nil
		}
		clean := filepath.Clean(path)
		switch {
		case entry.IsDir():
			entries[clean] = overlayEntryInfo{kind: overlayKindDir}
		case entry.Type()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(clean)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					return nil
				}
				problems = append(problems, fmt.Sprintf("%s: %v", clean, readErr))
				return nil
			}
			entries[clean] = overlayEntryInfo{kind: overlayKindSymlink, target: target}
		default:
			entries[clean] = overlayEntryInfo{kind: overlayKindFile}
		}
		return nil
	})
	if walkErr != nil {
		problems = append(problems, walkErr.Error())
	}
	return entries, problems, nil
}

// changedOverlayPaths returns the paths whose snapshot differs between the two
// scans, including paths that existed before and no longer exist. Replaced
// directories, retyped links and removed links are all attributable.
func changedOverlayPaths(before, after map[string]overlayEntryInfo) map[string]struct{} {
	changed := make(map[string]struct{}, len(before)+len(after))
	for path, prev := range before {
		if current, ok := after[path]; ok && current == prev {
			continue
		}
		changed[path] = struct{}{}
	}
	for path := range after {
		if _, ok := before[path]; ok {
			continue
		}
		changed[path] = struct{}{}
	}
	return changed
}

// storedEntryName returns the spelling the directory actually stores for the
// entry described by info. Resolution is delegated to the filesystem, which may
// reach an entry through a different spelling (case-insensitive volumes), so
// attribution must compare against the stored name, not the requested one.
func storedEntryName(parent string, info os.FileInfo) (string, bool) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}
		if os.SameFile(entryInfo, info) {
			return entry.Name(), true
		}
	}
	return "", false
}

// resolveOverlayTargetLive resolves the target of the link at linkPath through
// the live filesystem, applying ".." to the already resolved prefix the way the
// kernel does. With canonicalize set, every component is replaced by the name the
// parent directory actually stores (matched by filesystem identity), so an
// ancestor reached through an alternate spelling still maps back to the changed
// entry; containment itself never needs it, because the filesystem resolves the
// requested path. tainted reports whether the walk passed through a path this
// unpack installed, changed, or removed.
func resolveOverlayTargetLive(linkPath string, changed map[string]struct{}, canonicalize bool) (resolved string, tainted bool, err error) {
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
		next := filepath.Join(current, part)
		info, lstatErr := os.Lstat(next)
		if lstatErr == nil && canonicalize {
			if stored, ok := storedEntryName(filepath.Dir(next), info); ok {
				next = filepath.Join(filepath.Dir(next), stored)
			}
		}
		if lstatErr == nil {
			if _, ok := changed[filepath.Clean(next)]; ok {
				tainted = true
			}
		}
		if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			hops++
			if hops > maxOverlaySymlinkHops {
				return "", false, fmt.Errorf("too many symlink hops while resolving %q", linkPath)
			}
			linkTarget, readErr := os.Readlink(next)
			if readErr != nil {
				return "", false, readErr
			}
			// A link target is relative to the directory holding the link; an
			// absolute target restarts at the filesystem root and must not be
			// overwritten by the link's own directory.
			current = filepath.Dir(next)
			if filepath.IsAbs(linkTarget) {
				current = string(filepath.Separator)
			}
			pending = append(strings.Split(filepath.ToSlash(linkTarget), "/"), pending...)
			continue
		}
		current = next
	}
	return filepath.Clean(current), tainted, nil
}

// auditUnpackedOverlaySymlinks enforces the unpack symlink invariant without
// touching unrelated local state. A link is attributable to this run when its
// path is new, its stored target changed, or its live resolution passes through a
// path this run installed, changed, or removed. Attribution comes from ti's own
// pre-run snapshot, so an archive cannot narrow or forge it, and the comparison
// follows the filesystem's own name resolution through stored spellings.
func auditUnpackedOverlaySymlinks(localRoot string, before map[string]overlayEntryInfo) error {
	after, walkProblems, err := scanOverlayEntries(localRoot)
	if err != nil {
		return err
	}
	if after == nil {
		if len(walkProblems) > 0 {
			return apperr.New("fs.unpack_audit_incomplete", "runtime", 1, fmt.Sprintf("could not audit the overlay under %q: %s", localRoot, strings.Join(walkProblems, "; ")))
		}
		return nil
	}
	overlayRoot := filepath.Join(localRoot, "overlay")
	changed := changedOverlayPaths(before, after)
	var violations []string
	var targets []string
	for linkPath, entry := range after {
		if entry.kind != overlayKindSymlink {
			continue
		}
		rel, relErr := filepath.Rel(overlayRoot, linkPath)
		if relErr != nil {
			walkProblems = append(walkProblems, fmt.Sprintf("%s: %v", linkPath, relErr))
			continue
		}
		_, attributable := changed[linkPath]
		escape := validatePackSymlinkTarget(localRoot, filepath.ToSlash(rel), entry.target) != nil
		tainted := false
		if !escape {
			resolved, dependsOnChanged, resolveErr := resolveOverlayTargetLive(linkPath, changed, false)
			tainted = dependsOnChanged
			escape = resolveErr != nil || !overlayPathWithin(overlayRoot, resolved)
			if escape && !attributable && !tainted {
				// The requested spelling may not be the stored one for an ancestor,
				// so re-resolve with every component canonicalized before deciding
				// that this link is not attributable.
				_, dependsOnChanged, _ = resolveOverlayTargetLive(linkPath, changed, true)
				tainted = dependsOnChanged
			}
		}
		if !escape || (!attributable && !tainted) {
			continue
		}
		violations = append(violations, fmt.Sprintf("%s -> %s", rel, entry.target))
		targets = append(targets, linkPath)
	}
	sort.Strings(violations)
	// Decide first, then remove, so one removal cannot change another link's
	// resolution while it is still being audited. Cleanup always runs before the
	// audit reports anything.
	var removeErr error
	for _, linkPath := range targets {
		if err := os.Remove(linkPath); err != nil && !errors.Is(err, os.ErrNotExist) && removeErr == nil {
			removeErr = apperr.Wrap("fs.unpack_audit_overlay", "runtime", 1, fmt.Sprintf("remove unsafe unpacked symlink %q", linkPath), err)
		}
	}
	if removeErr != nil {
		return removeErr
	}
	removedSummary := ""
	if len(violations) > 0 {
		removedSummary = fmt.Sprintf("removed %d unsafe symlink(s): %s", len(violations), strings.Join(violations, ", "))
	}
	switch {
	case len(walkProblems) > 0:
		detail := strings.Join(walkProblems, "; ")
		if removedSummary != "" {
			detail = removedSummary + "; " + detail
		}
		return apperr.New("fs.unpack_audit_incomplete", "runtime", 1, fmt.Sprintf("could not audit the whole overlay %s, so the unpacked links are not trusted: %s", overlayRoot, detail))
	case len(violations) > 0:
		return apperr.New("fs.unsafe_pack_symlink", "runtime", 1, fmt.Sprintf("%s from %s; the remote pack contains links that escape the local overlay", removedSummary, overlayRoot))
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
