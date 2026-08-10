//go:build !windows

package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
)

const fuseGitCacheTTL = 2 * time.Second

type fuseGitRuntime struct {
	mu         sync.Mutex
	loadedAt   time.Time
	workspaces []apifs.GitWorkspace
	trees      map[string]map[string]apifs.GitTreeNode
	overlays   map[string]map[string]apifs.GitOverlayEntry
}

type fuseGitEntry struct {
	workspace apifs.GitWorkspace
	relPath   string
	root      bool
	clean     *apifs.GitTreeNode
	overlay   *apifs.GitOverlayEntry
	dir       bool
}

func newFuseGitRuntime() *fuseGitRuntime {
	return &fuseGitRuntime{
		trees:    map[string]map[string]apifs.GitTreeNode{},
		overlays: map[string]map[string]apifs.GitOverlayEntry{},
	}
}

func (r *remoteFuseRuntime) gitEntry(ctx context.Context, remotePath string) (fuseGitEntry, bool, error) {
	if !r.gitEnabled() {
		return fuseGitEntry{}, false, nil
	}
	if err := r.git.load(ctx, r.client); err != nil {
		return fuseGitEntry{}, false, err
	}
	workspace, rel, ok := r.git.workspaceForPath(remotePath)
	if !ok {
		return fuseGitEntry{}, false, nil
	}
	if rel == "" {
		return fuseGitEntry{workspace: workspace, root: true, dir: true}, true, nil
	}
	if overlay, ok := r.git.overlays[workspace.WorkspaceID][rel]; ok {
		if strings.TrimSpace(overlay.Op) == "whiteout" {
			return fuseGitEntry{}, false, nil
		}
		entry := fuseGitEntry{workspace: workspace, relPath: rel, overlay: &overlay}
		if overlay.Kind == "dir" {
			entry.dir = true
		}
		return entry, true, nil
	}
	if clean, ok := r.git.trees[workspace.WorkspaceID][rel]; ok {
		entry := fuseGitEntry{workspace: workspace, relPath: rel, clean: &clean}
		if clean.Kind == "dir" {
			entry.dir = true
		}
		return entry, true, nil
	}
	if r.git.hasVisibleChild(workspace.WorkspaceID, rel) {
		return fuseGitEntry{workspace: workspace, relPath: rel, dir: true}, true, nil
	}
	return fuseGitEntry{}, false, nil
}

func (r *remoteFuseRuntime) gitParentEntry(ctx context.Context, remotePath string) (fuseGitEntry, bool, error) {
	parent := path.Dir(remotePath)
	if parent == "." {
		parent = "/"
	}
	entry, ok, err := r.gitEntry(ctx, parent)
	if err != nil || !ok {
		return fuseGitEntry{}, ok, err
	}
	if entry.kind() != "dir" {
		return fuseGitEntry{}, false, nil
	}
	return entry, true, nil
}

func (r *remoteFuseRuntime) gitReaddir(ctx context.Context, remotePath string) ([]fuse.DirEntry, error) {
	if !r.gitEnabled() {
		return nil, nil
	}
	if err := r.git.load(ctx, r.client); err != nil {
		return nil, err
	}
	entryByName := map[string]fuse.DirEntry{}
	for _, workspace := range r.git.workspaces {
		parent, base := path.Split(strings.TrimRight(workspace.RootPath, "/"))
		if parent == "" {
			parent = "/"
		}
		parent = strings.TrimRight(parent, "/")
		if parent == "" {
			parent = "/"
		}
		if parent == remotePath && base != "" {
			childPath := path.Join(parent, base)
			if parent == "/" {
				childPath = "/" + base
			}
			entryByName[base] = fuse.DirEntry{Name: base, Mode: fuse.S_IFDIR, Ino: fuseInode("git:" + childPath)}
		}
	}
	workspace, parentRel, ok := r.git.workspaceForPath(remotePath)
	if !ok {
		return mapToFuseEntries(entryByName), nil
	}
	hidden := map[string]bool{}
	for rel, overlay := range r.git.overlays[workspace.WorkspaceID] {
		name, direct, nested := gitDirectChild(parentRel, rel)
		if !direct {
			continue
		}
		if strings.TrimSpace(overlay.Op) == "whiteout" {
			if nested {
				continue
			}
			hidden[name] = true
			delete(entryByName, name)
			continue
		}
		kind, mode := overlay.Kind, overlay.Mode
		if nested {
			kind, mode = "dir", "040000"
		}
		entryByName[name] = fuse.DirEntry{Name: name, Mode: gitFuseMode(kind, mode), Ino: fuseInode("git:" + toRemotePath(workspace.RootPath, rel))}
	}
	for rel, node := range r.git.trees[workspace.WorkspaceID] {
		name, direct, nested := gitDirectChild(parentRel, rel)
		if !direct || hidden[name] {
			continue
		}
		if r.git.isWhiteouted(workspace.WorkspaceID, rel) {
			continue
		}
		kind, mode := node.Kind, node.Mode
		if nested {
			kind, mode = "dir", "040000"
		}
		entryByName[name] = fuse.DirEntry{Name: name, Mode: gitFuseMode(kind, mode), Ino: fuseInode("git:" + toRemotePath(workspace.RootPath, rel))}
	}
	return mapToFuseEntries(entryByName), nil
}

func (r *remoteFuseRuntime) gitReadFile(ctx context.Context, entry fuseGitEntry) ([]byte, error) {
	if entry.kind() == "dir" {
		return nil, os.ErrInvalid
	}
	if entry.overlay != nil {
		return append([]byte(nil), entry.overlay.Content...), nil
	}
	if entry.clean == nil {
		return nil, os.ErrNotExist
	}
	repoRoot, err := r.gitLocalRepoRoot(entry.workspace)
	if err != nil {
		if restoreErr := r.restoreGitWorkspace(entry.workspace); restoreErr != nil {
			return nil, err
		}
		repoRoot, err = r.gitLocalRepoRoot(entry.workspace)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(entry.clean.ObjectSHA) == "" {
		return nil, os.ErrNotExist
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "cat-file", "-p", entry.clean.ObjectSHA)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

func (r *remoteFuseRuntime) restoreGitWorkspace(workspace apifs.GitWorkspace) error {
	if r == nil || r.client == nil || strings.TrimSpace(r.localRoot) == "" {
		return os.ErrNotExist
	}
	localPath, ok := remoteToLocalPath(r.remoteRoot, workspace.RootPath)
	if !ok {
		return os.ErrNotExist
	}
	root, err := overlayPathForArchivePath(r.localRoot, localPath)
	if err != nil {
		return err
	}
	resolved := mountedGitTarget{
		MountPoint:  r.mountPath,
		MountRel:    strings.Trim(strings.TrimPrefix(localPath, "/"), "/"),
		RemoteRoot:  r.remoteRoot,
		RemotePath:  workspace.RootPath,
		LocalRoot:   r.localRoot,
		LocalGitDir: filepath.Join(root, ".git"),
	}
	_, err = restoreGitWorkspaceLocalState(context.Background(), r.client, workspace, resolved, root)
	return err
}

func (r *remoteFuseRuntime) gitPutDirectory(ctx context.Context, parent fuseGitEntry, remotePath string, mode os.FileMode) (os.FileInfo, error) {
	if err := r.gitPutEntry(ctx, parent.workspace, remotePath, "dir", gitModeString("dir", mode), nil); err != nil {
		return nil, err
	}
	return remoteFileInfo{name: path.Base(remotePath), mode: os.ModeDir | mode.Perm(), modTime: time.Now()}, nil
}

func (r *remoteFuseRuntime) gitPutEntry(ctx context.Context, workspace apifs.GitWorkspace, remotePath, kind, mode string, data []byte) error {
	rel, ok := gitRelPath(workspace.RootPath, remotePath)
	if !ok || rel == "" {
		return os.ErrInvalid
	}
	if mode == "" {
		mode = gitModeString(kind, 0)
	}
	_, err := r.client.PutGitOverlayEntry(ctx, workspace.WorkspaceID, apifs.GitOverlayEntryRequest{
		Path:      rel,
		Op:        "upsert",
		Kind:      kind,
		Mode:      mode,
		SizeBytes: int64(len(data)),
		Content:   append([]byte(nil), data...),
	})
	if err != nil {
		return err
	}
	r.git.invalidate()
	return nil
}

func (r *remoteFuseRuntime) gitWhiteout(ctx context.Context, workspace apifs.GitWorkspace, remotePath string) error {
	rel, ok := gitRelPath(workspace.RootPath, remotePath)
	if !ok || rel == "" {
		return os.ErrInvalid
	}
	_, err := r.client.PutGitOverlayEntry(ctx, workspace.WorkspaceID, apifs.GitOverlayEntryRequest{
		Path: rel,
		Op:   "whiteout",
	})
	if err != nil {
		return err
	}
	r.git.invalidate()
	return nil
}

func newGitFuseFile(runtime *remoteFuseRuntime, workspace apifs.GitWorkspace, remotePath string, data []byte, writable, dirty bool, mode os.FileMode) *remoteFuseFile {
	handle := newRemoteFuseFile(runtime, remotePath, data, writable, dirty, fuseObjectVersion{})
	if mode == 0 {
		mode = 0o644
	}
	handle.mode = mode
	handle.commit = func(ctx context.Context, f *remoteFuseFile) error {
		return runtime.gitPutEntry(ctx, workspace, f.remotePath, "file", gitModeString("file", f.mode), f.data)
	}
	return handle
}

func (r *remoteFuseRuntime) gitEnabled() bool {
	return r != nil && r.git != nil && r.client != nil && r.overlayEnabled()
}

func (r *remoteFuseRuntime) gitLocalRepoRoot(workspace apifs.GitWorkspace) (string, error) {
	localPath, ok := remoteToLocalPath(r.remoteRoot, workspace.RootPath)
	if !ok {
		return "", os.ErrNotExist
	}
	root, err := overlayPathForArchivePath(r.localRoot, localPath)
	if err != nil {
		return "", err
	}
	gitPath := filepath.Join(root, ".git")
	if _, err := os.Lstat(gitPath); err != nil {
		return "", err
	}
	return root, nil
}

func (g *fuseGitRuntime) load(ctx context.Context, client *apifs.Client) error {
	g.mu.Lock()
	if time.Since(g.loadedAt) < fuseGitCacheTTL && g.loadedAt.After(time.Time{}) {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()

	response, err := client.ListGitWorkspaces(ctx)
	if err != nil {
		if errors.Is(mapWebDAVError(err), os.ErrNotExist) {
			g.store(nil, nil, nil)
			return nil
		}
		return err
	}
	workspaces := make([]apifs.GitWorkspace, 0, len(response.Workspaces))
	trees := map[string]map[string]apifs.GitTreeNode{}
	overlays := map[string]map[string]apifs.GitOverlayEntry{}
	for _, workspace := range response.Workspaces {
		if workspace.WorkspaceID == "" || strings.TrimSpace(workspace.RootPath) == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(workspace.Status))
		if status == "deleted" || status == "inactive" {
			continue
		}
		workspaces = append(workspaces, workspace)
		treeByPath := map[string]apifs.GitTreeNode{}
		if workspace.HeadCommit != "" {
			tree, err := client.ListGitTree(ctx, workspace.WorkspaceID, workspace.HeadCommit)
			if err != nil {
				return err
			}
			for _, node := range tree.Nodes {
				if node.Path == "" {
					continue
				}
				treeByPath[strings.Trim(node.Path, "/")] = node
			}
		}
		trees[workspace.WorkspaceID] = treeByPath
		overlayByPath := map[string]apifs.GitOverlayEntry{}
		overlay, err := client.ListGitOverlayEntries(ctx, workspace.WorkspaceID)
		if err != nil {
			return err
		}
		for _, entry := range overlay.Entries {
			if entry.Path == "" {
				continue
			}
			overlayByPath[strings.Trim(entry.Path, "/")] = entry
		}
		overlays[workspace.WorkspaceID] = overlayByPath
	}
	sort.Slice(workspaces, func(i, j int) bool {
		return len(workspaces[i].RootPath) > len(workspaces[j].RootPath)
	})
	g.store(workspaces, trees, overlays)
	return nil
}

func (g *fuseGitRuntime) store(workspaces []apifs.GitWorkspace, trees map[string]map[string]apifs.GitTreeNode, overlays map[string]map[string]apifs.GitOverlayEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.workspaces = workspaces
	g.trees = trees
	g.overlays = overlays
	g.loadedAt = time.Now()
	if g.trees == nil {
		g.trees = map[string]map[string]apifs.GitTreeNode{}
	}
	if g.overlays == nil {
		g.overlays = map[string]map[string]apifs.GitOverlayEntry{}
	}
}

func (g *fuseGitRuntime) invalidate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadedAt = time.Time{}
}

func (g *fuseGitRuntime) workspaceForPath(remotePath string) (apifs.GitWorkspace, string, bool) {
	for _, workspace := range g.workspaces {
		if rel, ok := gitRelPath(workspace.RootPath, remotePath); ok {
			return workspace, rel, true
		}
	}
	return apifs.GitWorkspace{}, "", false
}

func (g *fuseGitRuntime) hasVisibleChild(workspaceID, rel string) bool {
	prefix := strings.Trim(rel, "/")
	if prefix != "" {
		prefix += "/"
	}
	for child := range g.trees[workspaceID] {
		if strings.HasPrefix(child, prefix) && !g.isWhiteouted(workspaceID, child) {
			return true
		}
	}
	for child, overlay := range g.overlays[workspaceID] {
		if strings.TrimSpace(overlay.Op) == "whiteout" {
			continue
		}
		if strings.HasPrefix(child, prefix) {
			return true
		}
	}
	return false
}

func (g *fuseGitRuntime) isWhiteouted(workspaceID, rel string) bool {
	overlay, ok := g.overlays[workspaceID][rel]
	return ok && strings.TrimSpace(overlay.Op) == "whiteout"
}

func (entry fuseGitEntry) kind() string {
	switch {
	case entry.dir || entry.root:
		return "dir"
	case entry.overlay != nil && entry.overlay.Kind != "":
		return entry.overlay.Kind
	case entry.clean != nil && entry.clean.Kind != "":
		return entry.clean.Kind
	default:
		return "file"
	}
}

func (entry fuseGitEntry) mode() string {
	if entry.overlay != nil && entry.overlay.Mode != "" {
		return entry.overlay.Mode
	}
	if entry.clean != nil && entry.clean.Mode != "" {
		return entry.clean.Mode
	}
	return gitModeString(entry.kind(), 0)
}

func (entry fuseGitEntry) fileMode() os.FileMode {
	return fileModeFromGit(entry.kind(), entry.mode())
}

func (entry fuseGitEntry) info() os.FileInfo {
	size := int64(0)
	if entry.overlay != nil {
		size = entry.overlay.SizeBytes
		if size == 0 && len(entry.overlay.Content) > 0 {
			size = int64(len(entry.overlay.Content))
		}
	} else if entry.clean != nil && entry.clean.SizeBytes > 0 {
		size = entry.clean.SizeBytes
	}
	name := path.Base(entry.relPath)
	if entry.root || entry.relPath == "" {
		name = path.Base(entry.workspace.RootPath)
	}
	return remoteFileInfo{name: name, size: size, mode: entry.fileMode(), modTime: time.Now()}
}

func (entry fuseGitEntry) withModeAndSize(mode string, size int64) fuseGitEntry {
	next := entry
	if next.overlay != nil {
		copyOverlay := *next.overlay
		copyOverlay.Mode = mode
		copyOverlay.SizeBytes = size
		next.overlay = &copyOverlay
		return next
	}
	next.overlay = &apifs.GitOverlayEntry{Path: entry.relPath, Kind: entry.kind(), Mode: mode, SizeBytes: size}
	next.clean = nil
	return next
}

func (entry fuseGitEntry) withName(name string) fuseGitEntry {
	next := entry
	next.relPath = path.Join(path.Dir(entry.relPath), name)
	if next.relPath == "." {
		next.relPath = name
	}
	return next
}

func gitRelPath(rootPath, remotePath string) (string, bool) {
	root, err := normalizeRemotePath(defaultRemotePath(rootPath))
	if err != nil {
		return "", false
	}
	remote, err := normalizeRemotePath(remotePath)
	if err != nil {
		return "", false
	}
	if remote == root {
		return "", true
	}
	if root == "/" {
		return strings.TrimPrefix(remote, "/"), true
	}
	prefix := strings.TrimRight(root, "/") + "/"
	if !strings.HasPrefix(remote, prefix) {
		return "", false
	}
	return strings.TrimPrefix(remote, prefix), true
}

func gitDirectChild(parentRel, childRel string) (string, bool, bool) {
	parentRel = strings.Trim(parentRel, "/")
	childRel = strings.Trim(childRel, "/")
	if parentRel != "" {
		prefix := parentRel + "/"
		if !strings.HasPrefix(childRel, prefix) {
			return "", false, false
		}
		childRel = strings.TrimPrefix(childRel, prefix)
	}
	if childRel == "" {
		return "", false, false
	}
	parts := strings.SplitN(childRel, "/", 2)
	name := parts[0]
	return name, name != "", len(parts) > 1
}

func mapToFuseEntries(entryByName map[string]fuse.DirEntry) []fuse.DirEntry {
	entries := make([]fuse.DirEntry, 0, len(entryByName))
	for _, entry := range entryByName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func gitFuseMode(kind, mode string) uint32 {
	fileMode := fileModeFromGit(kind, mode)
	switch {
	case fileMode.IsDir():
		return fuse.S_IFDIR
	case fileMode&os.ModeSymlink != 0:
		return fuse.S_IFLNK
	default:
		return fuse.S_IFREG
	}
}

func fileModeFromGit(kind, mode string) os.FileMode {
	switch kind {
	case "dir":
		return os.ModeDir | 0o755
	case "symlink":
		return os.ModeSymlink | 0o777
	default:
		if mode == "100755" {
			return 0o755
		}
		return 0o644
	}
}

func gitModeString(kind string, mode os.FileMode) string {
	switch kind {
	case "dir":
		return "040000"
	case "symlink":
		return "120000"
	default:
		if mode&0o111 != 0 {
			return "100755"
		}
		return "100644"
	}
}
