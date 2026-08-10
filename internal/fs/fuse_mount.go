//go:build !windows

package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/tidbcloud/ti-cli/internal/api"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

var errFuseWriteConflict = errors.New("remote file changed since it was opened")

func (s Service) mountFUSEForeground(ctx context.Context, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck) (MountResult, error) {
	info, err := statFuseRemote(ctx, inputs.client, inputs.remotePath)
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.mount_remote_root", "runtime", 1, fmt.Sprintf("stat remote mount root %q", inputs.remotePath), err)
	}
	if !info.IsDir() {
		return MountResult{}, apperr.New("fs.mount_remote_root_not_directory", "usage", 2, fmt.Sprintf("remote mount root %q is not a directory", inputs.remotePath))
	}
	if err := os.MkdirAll(inputs.mountPath, 0o755); err != nil {
		return MountResult{}, apperr.Wrap("fs.create_mount_path", "runtime", 1, fmt.Sprintf("create mount path %q", inputs.mountPath), err)
	}

	timeout := time.Second
	runtime := newRemoteFuseRuntime(inputs)
	recovered, err := runtime.recoverPending(ctx)
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.fuse_recover_pending_writes", "runtime", 1, fmt.Sprintf("recover pending FUSE writes from %q", inputs.cacheDir), err)
	}
	if recovered > 0 {
		checks = append(checks, MountRuntimeCheck{Name: "fuse_write_back_recovery", Status: "passed", Message: fmt.Sprintf("uploaded %d pending writes", recovered)})
	}
	root := newRemoteFuseNode(runtime, inputs.remotePath)
	options := &gofs.Options{
		AttrTimeout:     &timeout,
		EntryTimeout:    &timeout,
		NegativeTimeout: &timeout,
		UID:             uint32(os.Getuid()),
		GID:             uint32(os.Getgid()),
		MountOptions: gofuse.MountOptions{
			FsName: "ti-fs:" + inputs.fileSystemName,
			Name:   "tifs",
		},
	}
	if inputs.readOnly {
		options.MountOptions.Options = append(options.MountOptions.Options, "ro")
	}
	server, err := gofs.Mount(inputs.mountPath, root, options)
	if err != nil {
		return MountResult{}, apperr.Wrap("fs.mount_fuse", "runtime", 1, fmt.Sprintf("mount ti fs with FUSE at %q", inputs.mountPath), err)
	}
	controlServer, err := startMountControlServer(inputs.mountPath, runtime)
	if err != nil {
		_ = server.Unmount()
		return MountResult{}, apperr.Wrap("fs.mount_control", "runtime", 1, fmt.Sprintf("start ti fs mount control socket for %q", inputs.mountPath), err)
	}
	defer controlServer.Close()

	state, err := mountstate.New(inputs.profile.Name, inputs.fileSystemName, inputs.mountPath, inputs.remotePath, inputs.driver.Name(), inputs.endpoint.BaseURL, os.Getpid(), inputs.readOnly, time.Now().UTC())
	if err != nil {
		_ = server.Unmount()
		return MountResult{}, apperr.Wrap("fs.mount_state", "runtime", 1, fmt.Sprintf("create mount state for %q", inputs.mountPath), err)
	}
	state.ControlSocket = controlServer.SocketPath()
	state.MountProfile = inputs.mountProfile
	state.LocalRoot = inputs.localRoot
	state.PackPaths = append([]string(nil), inputs.packPaths...)
	stateFile, err := mountstate.Write(inputs.homeDir, state)
	if err != nil {
		_ = server.Unmount()
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
		_ = server.Unmount()
	}()

	server.Wait()
	checks = append(checks, MountRuntimeCheck{Name: "mount_state", Status: "passed", Message: stateFile})
	return mountResult("unmounted", inputs, remote, checks, os.Getpid(), stateFile, ""), nil
}

type remoteFuseNode struct {
	gofs.Inode

	runtime    *remoteFuseRuntime
	remotePath string
}

func newRemoteFuseNode(runtime *remoteFuseRuntime, remotePath string) *remoteFuseNode {
	root, err := normalizeRemotePath(defaultRemotePath(remotePath))
	if err != nil {
		root = "/"
	}
	return &remoteFuseNode{runtime: runtime, remotePath: root}
}

type remoteFuseRuntime struct {
	client      *apifs.Client
	mountPath   string
	remoteRoot  string
	readOnly    bool
	localRoot   string
	profile     string
	git         *fuseGitRuntime
	metadata    *fsMetadataStore
	readCache   *fuseReadCache
	writeBack   *fuseWriteBackStore
	openHandles map[*remoteFuseFile]struct{}
	openSeq     uint64
	openMu      sync.Mutex
}

func newRemoteFuseRuntime(inputs mountInputs) *remoteFuseRuntime {
	var writeBack *fuseWriteBackStore
	if inputs.writeBackCache {
		writeBack = newFuseWriteBackStore(inputs.cacheDir, defaultFuseWriteBackMaxBytes, inputs.cacheIdentity)
	}
	return &remoteFuseRuntime{
		client:      inputs.client,
		mountPath:   inputs.mountPath,
		remoteRoot:  inputs.remotePath,
		readOnly:    inputs.readOnly,
		localRoot:   inputs.localRoot,
		profile:     inputs.mountProfile,
		git:         newFuseGitRuntime(),
		metadata:    inputs.metadataStore,
		readCache:   newFuseReadCache(inputs.readCacheBytes, inputs.readCacheFileBytes, inputs.readCacheTTL),
		writeBack:   writeBack,
		openHandles: map[*remoteFuseFile]struct{}{},
	}
}

func (r *remoteFuseRuntime) overlayEnabled() bool {
	return r != nil && strings.TrimSpace(r.localRoot) != "" && strings.TrimSpace(r.profile) != "" && r.profile != noneMountProfile
}

func (r *remoteFuseRuntime) localPath(remotePath string) (string, bool) {
	if !r.overlayEnabled() {
		return "", false
	}
	localPath, ok := remoteToLocalPath(r.remoteRoot, remotePath)
	if !ok {
		return "", false
	}
	return localPath, true
}

func (r *remoteFuseRuntime) localAbs(remotePath string) (string, bool) {
	localPath, ok := r.localPath(remotePath)
	if !ok {
		return "", false
	}
	abs, err := overlayPathForArchivePath(r.localRoot, localPath)
	if err != nil {
		return "", false
	}
	return abs, true
}

func (r *remoteFuseRuntime) localInfo(remotePath string) (os.FileInfo, string, bool) {
	abs, ok := r.localAbs(remotePath)
	if !ok {
		return nil, "", false
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, abs, false
	}
	return info, abs, true
}

func (r *remoteFuseRuntime) shouldUseLocal(remotePath string) bool {
	if _, _, ok := r.localInfo(remotePath); ok {
		return true
	}
	return r.isLocalOnly(remotePath)
}

func (r *remoteFuseRuntime) isLocalOnly(remotePath string) bool {
	localPath, ok := r.localPath(remotePath)
	if !ok {
		return false
	}
	clean := strings.Trim(strings.TrimPrefix(path.Clean(localPath), "/"), "/")
	if clean == "" {
		return false
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		switch part {
		case ".git", ".hg", ".svn", "node_modules", ".pnpm-store", "target", "dist", "build", "coverage", "tmp", ".tmp", ".tmp-api-extractor", ".cache", ".turbo", ".gradle", ".venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache":
			return true
		case ".next", ".vitepress":
			if i+1 < len(parts) && parts[i+1] == "cache" {
				return true
			}
		}
	}
	return false
}

func (r *remoteFuseRuntime) localParentExists(remotePath string) bool {
	localPath, ok := r.localPath(remotePath)
	if !ok {
		return false
	}
	if localPath == "/" {
		return false
	}
	parent := path.Dir(localPath)
	if parent == "/" {
		parentRemote := toRemotePath(r.remoteRoot, parent)
		return r.isLocalOnly(parentRemote) || r.profile == portableMountProfile
	}
	parentRemote := toRemotePath(r.remoteRoot, parent)
	if r.isLocalOnly(parentRemote) {
		return true
	}
	if r.profile != portableMountProfile {
		return false
	}
	info, _, ok := r.localInfo(parentRemote)
	return ok && info.IsDir()
}

func (r *remoteFuseRuntime) mkdirLocalParent(remotePath string) error {
	abs, ok := r.localAbs(remotePath)
	if !ok {
		return os.ErrInvalid
	}
	return os.MkdirAll(filepath.Dir(abs), 0o755)
}

func localFuseDirEntry(name string, info os.FileInfo, inoPath string) gofuse.DirEntry {
	mode := uint32(gofuse.S_IFREG)
	switch {
	case info.IsDir():
		mode = gofuse.S_IFDIR
	case info.Mode()&os.ModeSymlink != 0:
		mode = gofuse.S_IFLNK
	}
	return gofuse.DirEntry{Name: name, Mode: mode, Ino: fuseInode("local:" + inoPath)}
}

func (r *remoteFuseRuntime) recoverPending(ctx context.Context) (int, error) {
	if r == nil || r.writeBack == nil {
		return 0, nil
	}
	return r.writeBack.recover(ctx, r.upload)
}

func (r *remoteFuseRuntime) readFile(ctx context.Context, remotePath string, version fuseObjectVersion) ([]byte, error) {
	if data, ok := r.readCache.get(remotePath, version); ok {
		return data, nil
	}
	data, err := r.client.ReadFile(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	r.readCache.put(remotePath, data, version)
	return data, nil
}

func (r *remoteFuseRuntime) statRemote(ctx context.Context, remotePath string) (fuseRemoteStat, error) {
	stat, err := statFuseRemoteVersion(ctx, r.client, remotePath)
	if err != nil {
		return fuseRemoteStat{}, err
	}
	if info, ok := stat.info.(remoteFileInfo); ok && r.metadata != nil {
		stat.info = r.metadata.applyFileInfo(remotePath, info)
	}
	return stat, nil
}

func (r *remoteFuseRuntime) statRemoteInfo(ctx context.Context, remotePath string) (os.FileInfo, error) {
	stat, err := r.statRemote(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	return stat.info, nil
}

func (r *remoteFuseRuntime) writeFile(ctx context.Context, remotePath string, data []byte, baseVersion fuseObjectVersion) (fuseObjectVersion, error) {
	return r.writeFileWithDirty(ctx, remotePath, data, baseVersion, int64(len(data)), []fuseDirtyRange{{Start: 0, End: int64(len(data))}})
}

func (r *remoteFuseRuntime) writeFileWithDirty(ctx context.Context, remotePath string, data []byte, baseVersion fuseObjectVersion, baseSize int64, dirtyRanges []fuseDirtyRange) (fuseObjectVersion, error) {
	var version fuseObjectVersion
	var err error
	if r.writeBack != nil {
		version, err = r.writeBack.putAndUpload(ctx, remotePath, data, baseVersion, baseSize, dirtyRanges, r.upload)
	} else {
		version, err = r.upload(ctx, remotePath, data, baseVersion, baseSize, dirtyRanges)
	}
	if err != nil {
		r.readCache.invalidate(remotePath)
		return fuseObjectVersion{}, err
	}
	r.readCache.put(remotePath, data, version)
	return version, nil
}

func (r *remoteFuseRuntime) upload(ctx context.Context, remotePath string, data []byte, baseVersion fuseObjectVersion, baseSize int64, dirtyRanges []fuseDirtyRange) (fuseObjectVersion, error) {
	if r == nil || r.client == nil {
		return fuseObjectVersion{}, errors.New("ti fs FUSE runtime has no data-plane client")
	}
	if err := r.checkWriteBase(ctx, remotePath, baseVersion); err != nil {
		return fuseObjectVersion{}, err
	}
	if shouldPatchFuseWrite(baseVersion, baseSize, int64(len(data)), dirtyRanges) {
		if version, err := r.patchUpload(ctx, remotePath, data, baseVersion, baseSize, dirtyRanges); err == nil {
			return version, nil
		} else if !shouldFallbackFusePatch(err) {
			return fuseObjectVersion{}, err
		}
	}
	response, err := r.client.WriteFile(ctx, remotePath, data)
	if err != nil {
		return fuseObjectVersion{}, err
	}
	return baseVersion.withRevision(response.Revision), nil
}

func (r *remoteFuseRuntime) patchUpload(ctx context.Context, remotePath string, data []byte, baseVersion fuseObjectVersion, baseSize int64, dirtyRanges []fuseDirtyRange) (fuseObjectVersion, error) {
	partSizeBytes := apifs.CalcAdaptivePartSize(max(baseSize, int64(len(data))))
	dirtyParts := fuseDirtyParts(dirtyRanges, partSizeBytes, int64(len(data)))
	if len(dirtyParts) == 0 {
		return baseVersion, nil
	}
	expectedRevision := baseVersion.Revision
	if err := r.client.PatchFile(ctx, remotePath, int64(len(data)), dirtyParts, func(partNumber int, partSize int64, original []byte) ([]byte, error) {
		offset := int64(partNumber-1) * partSizeBytes
		if offset >= int64(len(data)) {
			return make([]byte, partSize), nil
		}
		end := offset + partSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		out := make([]byte, partSize)
		copy(out, data[offset:end])
		return out, nil
	}, apifs.PatchFileOptions{PartSize: partSizeBytes, ExpectedRevision: &expectedRevision}); err != nil {
		return fuseObjectVersion{}, err
	}
	stat, err := statFuseRemoteVersion(ctx, r.client, remotePath)
	if err != nil {
		return fuseObjectVersion{}, err
	}
	return stat.version, nil
}

func (r *remoteFuseRuntime) checkWriteBase(ctx context.Context, remotePath string, baseVersion fuseObjectVersion) error {
	if !baseVersion.known() {
		return nil
	}
	stat, err := statFuseRemoteVersion(ctx, r.client, remotePath)
	if err != nil {
		if errors.Is(mapWebDAVError(err), os.ErrNotExist) {
			return fmt.Errorf("%w: remote file %q no longer exists", errFuseWriteConflict, remotePath)
		}
		return err
	}
	if stat.version.conflictsWith(baseVersion) {
		return fmt.Errorf("%w: remote file %q changed since it was opened", errFuseWriteConflict, remotePath)
	}
	return nil
}

func shouldPatchFuseWrite(baseVersion fuseObjectVersion, baseSize, newSize int64, dirtyRanges []fuseDirtyRange) bool {
	if !baseVersion.known() || baseVersion.Revision <= 0 || baseSize <= 0 || newSize <= 0 || len(dirtyRanges) == 0 {
		return false
	}
	if newSize != baseSize {
		return false
	}
	totalDirty := int64(0)
	for _, r := range mergeFuseDirtyRanges(dirtyRanges) {
		if r.End > r.Start {
			totalDirty += r.End - r.Start
		}
	}
	return totalDirty > 0 && totalDirty < newSize
}

func shouldFallbackFusePatch(err error) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusMethodNotAllowed || apiErr.Code == "api.contract_gap" {
		return true
	}
	if apiErr.StatusCode == http.StatusBadRequest {
		message := strings.ToLower(apiErr.Message + " " + apiErr.Body)
		return strings.Contains(message, "unknown") || strings.Contains(message, "not s3") || strings.Contains(message, "s3 not configured")
	}
	return false
}

func mergeFuseDirtyRanges(ranges []fuseDirtyRange) []fuseDirtyRange {
	if len(ranges) == 0 {
		return nil
	}
	cleaned := make([]fuseDirtyRange, 0, len(ranges))
	for _, r := range ranges {
		if r.Start < 0 {
			r.Start = 0
		}
		if r.End < r.Start {
			continue
		}
		cleaned = append(cleaned, r)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		if cleaned[i].Start == cleaned[j].Start {
			return cleaned[i].End < cleaned[j].End
		}
		return cleaned[i].Start < cleaned[j].Start
	})
	out := cleaned[:0]
	for _, r := range cleaned {
		if len(out) == 0 || r.Start > out[len(out)-1].End {
			out = append(out, r)
			continue
		}
		if r.End > out[len(out)-1].End {
			out[len(out)-1].End = r.End
		}
	}
	return append([]fuseDirtyRange(nil), out...)
}

func fuseDirtyParts(ranges []fuseDirtyRange, partSizeBytes, newSize int64) []int {
	partSizeBytes = max(partSizeBytes, int64(1))
	seen := map[int]struct{}{}
	for _, r := range mergeFuseDirtyRanges(ranges) {
		if r.Start >= newSize {
			continue
		}
		end := r.End
		if end > newSize {
			end = newSize
		}
		if end <= r.Start {
			end = r.Start + 1
		}
		first := int(r.Start/partSizeBytes) + 1
		last := int((end-1)/partSizeBytes) + 1
		for part := first; part <= last; part++ {
			seen[part] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for part := range seen {
		out = append(out, part)
	}
	sort.Ints(out)
	return out
}

func (r *remoteFuseRuntime) invalidate(remotePath string) {
	r.readCache.invalidate(remotePath)
}

func (r *remoteFuseRuntime) invalidatePrefix(remotePath string) {
	r.readCache.invalidatePrefix(remotePath)
}

func (r *remoteFuseRuntime) registerOpenHandle(handle *remoteFuseFile) {
	if r == nil || handle == nil {
		return
	}
	r.openMu.Lock()
	defer r.openMu.Unlock()
	if r.openHandles == nil {
		r.openHandles = map[*remoteFuseFile]struct{}{}
	}
	r.openSeq++
	handle.openSeq = r.openSeq
	r.openHandles[handle] = struct{}{}
}

func (r *remoteFuseRuntime) unregisterOpenHandle(handle *remoteFuseFile) {
	if r == nil || handle == nil {
		return
	}
	r.openMu.Lock()
	defer r.openMu.Unlock()
	delete(r.openHandles, handle)
}

func (r *remoteFuseRuntime) retargetOpenHandles(source, target string) {
	r.retargetOpenHandlesWithVersion(source, target, fuseObjectVersion{})
}

func (r *remoteFuseRuntime) retargetOpenHandlesWithVersion(source, target string, version fuseObjectVersion) {
	for _, handle := range r.openHandleSnapshot() {
		handle.retarget(source, target, version)
	}
}

func (r *remoteFuseRuntime) markDeletedOpenHandles(remotePath string) {
	for _, handle := range r.openHandleSnapshot() {
		handle.markDeleted(remotePath)
	}
}

func (r *remoteFuseRuntime) openHandleSnapshot() []*remoteFuseFile {
	if r == nil {
		return nil
	}
	r.openMu.Lock()
	defer r.openMu.Unlock()
	out := make([]*remoteFuseFile, 0, len(r.openHandles))
	for handle := range r.openHandles {
		out = append(out, handle)
	}
	return out
}

func (r *remoteFuseRuntime) adoptSingleCallerPathSetattr(ctx context.Context, remotePath string, in *gofuse.SetAttrIn, out *gofuse.AttrOut) (bool, syscall.Errno) {
	callerPID := fuseCallerPID(ctx)
	if callerPID == 0 {
		return false, gofs.OK
	}
	matching := r.writableOpenHandlesForPath(remotePath)
	if len(matching) != 1 {
		return false, gofs.OK
	}
	handle := matching[0]
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.openPID != callerPID {
		return false, gofs.OK
	}
	return true, handle.setattrLocked(in, out)
}

func (r *remoteFuseRuntime) refreshOpenHandlesAfterPathTruncate(remotePath string, version fuseObjectVersion) {
	for _, handle := range r.writableOpenHandlesForPath(remotePath) {
		handle.refreshAfterPathTruncate(remotePath, version)
	}
}

func (r *remoteFuseRuntime) writableOpenHandlesForPath(remotePath string) []*remoteFuseFile {
	var out []*remoteFuseFile
	for _, handle := range r.openHandleSnapshot() {
		handle.mu.Lock()
		ok := !handle.closed && !handle.deleted && handle.writable && handle.remotePath == remotePath
		handle.mu.Unlock()
		if ok {
			out = append(out, handle)
		}
	}
	return out
}

func (r *remoteFuseRuntime) dirtyOpenHandleData(remotePath string) ([]byte, fuseObjectVersion, bool) {
	var (
		bestData []byte
		bestVer  fuseObjectVersion
		bestSeq  uint64
		found    bool
	)
	for _, handle := range r.openHandleSnapshot() {
		handle.mu.Lock()
		ok := !handle.closed && !handle.deleted && handle.remotePath == remotePath && handle.dirty
		if ok && (!found || handle.openSeq > bestSeq) {
			bestData = append(bestData[:0], handle.data...)
			bestVer = handle.version
			bestSeq = handle.openSeq
			found = true
		}
		handle.mu.Unlock()
	}
	if !found {
		return nil, fuseObjectVersion{}, false
	}
	return bestData, bestVer, true
}

func fuseCallerPID(ctx context.Context) uint32 {
	if caller, ok := gofuse.FromContext(ctx); ok && caller != nil {
		return caller.Pid
	}
	return 0
}

var _ gofs.NodeGetattrer = (*remoteFuseNode)(nil)
var _ gofs.NodeLookuper = (*remoteFuseNode)(nil)
var _ gofs.NodeReaddirer = (*remoteFuseNode)(nil)
var _ gofs.NodeMkdirer = (*remoteFuseNode)(nil)
var _ gofs.NodeCreater = (*remoteFuseNode)(nil)
var _ gofs.NodeOpener = (*remoteFuseNode)(nil)
var _ gofs.NodeUnlinker = (*remoteFuseNode)(nil)
var _ gofs.NodeRmdirer = (*remoteFuseNode)(nil)
var _ gofs.NodeRenamer = (*remoteFuseNode)(nil)
var _ gofs.NodeSetattrer = (*remoteFuseNode)(nil)
var _ gofs.NodeReadlinker = (*remoteFuseNode)(nil)
var _ gofs.NodeSymlinker = (*remoteFuseNode)(nil)
var _ gofs.NodeLinker = (*remoteFuseNode)(nil)

func (n *remoteFuseNode) Getattr(ctx context.Context, f gofs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	if f != nil {
		if getter, ok := f.(gofs.FileGetattrer); ok {
			return getter.Getattr(ctx, out)
		}
	}
	if info, _, ok := n.runtime.localInfo(n.remotePath); ok || n.runtime.isLocalOnly(n.remotePath) {
		if !ok {
			return syscall.ENOENT
		}
		fillFuseAttr(&out.Attr, info)
		return gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, n.remotePath); err != nil {
		return fuseErrno(err)
	} else if ok {
		fillFuseAttr(&out.Attr, entry.info())
		return gofs.OK
	}
	info, err := n.runtime.statRemoteInfo(ctx, n.remotePath)
	if err != nil {
		return fuseErrno(err)
	}
	fillFuseAttr(&out.Attr, info)
	return gofs.OK
}

func (n *remoteFuseNode) Lookup(ctx context.Context, name string, out *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return nil, errno
	}
	if info, _, ok := n.runtime.localInfo(remotePath); ok || n.runtime.isLocalOnly(remotePath) {
		if !ok {
			return nil, syscall.ENOENT
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, remotePath); err != nil {
		return nil, fuseErrno(err)
	} else if ok {
		info := entry.info()
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("git:"+remotePath, info)), gofs.OK
	}
	info, err := n.runtime.statRemoteInfo(ctx, remotePath)
	if err != nil {
		return nil, fuseErrno(err)
	}
	fillFuseAttr(&out.Attr, info)
	child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
	return n.NewInode(ctx, child, fuseStableAttr(remotePath, info)), gofs.OK
}

func (n *remoteFuseNode) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	entryByName := map[string]gofuse.DirEntry{}
	if !n.runtime.isLocalOnly(n.remotePath) {
		response, err := n.runtime.client.List(ctx, n.remotePath)
		if err != nil {
			if _, _, ok := n.runtime.localInfo(n.remotePath); !ok {
				return nil, fuseErrno(err)
			}
		} else {
			for _, entry := range response.Entries {
				if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.Contains(entry.Name, "/") {
					continue
				}
				mode := uint32(gofuse.S_IFREG)
				if entry.IsDir {
					mode = gofuse.S_IFDIR
				}
				remotePath, errno := n.childPath(entry.Name)
				if errno != gofs.OK {
					continue
				}
				entryByName[entry.Name] = gofuse.DirEntry{Name: entry.Name, Mode: mode, Ino: fuseInode(remotePath)}
			}
		}
	}
	if gitEntries, err := n.runtime.gitReaddir(ctx, n.remotePath); err != nil {
		if len(entryByName) == 0 {
			return nil, fuseErrno(err)
		}
	} else {
		for _, entry := range gitEntries {
			entryByName[entry.Name] = entry
		}
	}
	if _, abs, ok := n.runtime.localInfo(n.remotePath); ok {
		localEntries, err := os.ReadDir(abs)
		if err == nil {
			for _, entry := range localEntries {
				if entry.Name() == "" || entry.Name() == "." || entry.Name() == ".." || strings.Contains(entry.Name(), "/") {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				remotePath, errno := n.childPath(entry.Name())
				if errno != gofs.OK {
					continue
				}
				entryByName[entry.Name()] = localFuseDirEntry(entry.Name(), info, remotePath)
			}
		}
	}
	entries := make([]gofuse.DirEntry, 0, len(entryByName))
	for _, entry := range entryByName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return gofs.NewListDirStream(entries), gofs.OK
}

func (n *remoteFuseNode) Mkdir(ctx context.Context, name string, mode uint32, out *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if n.runtime.readOnly {
		return nil, syscall.EROFS
	}
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return nil, errno
	}
	perm := int64(mode & 0o777)
	if perm == 0 {
		perm = 0o755
	}
	if n.runtime.isLocalOnly(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Mkdir(abs, os.FileMode(perm)); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.OK
	}
	if entry, ok, err := n.runtime.gitParentEntry(ctx, remotePath); err != nil {
		return nil, fuseErrno(err)
	} else if ok {
		info, err := n.runtime.gitPutDirectory(ctx, entry, remotePath, os.FileMode(perm))
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("git:"+remotePath, info)), gofs.OK
	}
	if n.runtime.localParentExists(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Mkdir(abs, os.FileMode(perm)); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.OK
	}
	if err := n.runtime.client.Mkdir(ctx, remotePath, perm); err != nil {
		return nil, fuseErrno(err)
	}
	info, err := n.runtime.statRemoteInfo(ctx, remotePath)
	if err != nil {
		info = remoteFileInfo{name: path.Base(remotePath), mode: os.ModeDir | os.FileMode(perm), modTime: time.Now()}
	}
	fillFuseAttr(&out.Attr, info)
	child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
	return n.NewInode(ctx, child, fuseStableAttr(remotePath, info)), gofs.OK
}

func (n *remoteFuseNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *gofuse.EntryOut) (*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	if n.runtime.readOnly {
		return nil, nil, 0, syscall.EROFS
	}
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return nil, nil, 0, errno
	}
	if n.runtime.isLocalOnly(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, nil, 0, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, nil, 0, fuseErrno(err)
		}
		perm := mode & 0o777
		if perm == 0 {
			perm = 0o644
		}
		fd, err := syscall.Open(abs, int(flags)|syscall.O_CREAT, uint32(perm))
		if err != nil {
			return nil, nil, 0, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			_ = syscall.Close(fd)
			return nil, nil, 0, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.NewLoopbackFile(fd), 0, gofs.OK
	}
	if entry, ok, err := n.runtime.gitParentEntry(ctx, remotePath); err != nil {
		return nil, nil, 0, fuseErrno(err)
	} else if ok {
		perm := os.FileMode(mode & 0o777)
		if perm == 0 {
			perm = 0o644
		}
		info := remoteFileInfo{name: path.Base(remotePath), mode: perm, modTime: time.Now()}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		handle := newGitFuseFile(n.runtime, entry.workspace, remotePath, nil, true, true, perm).withOpenContext(ctx, flags)
		return n.NewInode(ctx, child, fuseStableAttr("git:"+remotePath, info)), handle, gofuse.FOPEN_DIRECT_IO, gofs.OK
	}
	if n.runtime.localParentExists(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, nil, 0, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, nil, 0, fuseErrno(err)
		}
		perm := mode & 0o777
		if perm == 0 {
			perm = 0o644
		}
		fd, err := syscall.Open(abs, int(flags)|syscall.O_CREAT, uint32(perm))
		if err != nil {
			return nil, nil, 0, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			_ = syscall.Close(fd)
			return nil, nil, 0, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.NewLoopbackFile(fd), 0, gofs.OK
	}
	if flags&uint32(syscall.O_EXCL) != 0 {
		if _, err := n.runtime.statRemoteInfo(ctx, remotePath); err == nil {
			return nil, nil, 0, syscall.EEXIST
		} else if !errors.Is(mapWebDAVError(err), os.ErrNotExist) {
			return nil, nil, 0, fuseErrno(err)
		}
	}
	version, err := n.runtime.writeFile(ctx, remotePath, nil, fuseObjectVersion{})
	if err != nil {
		return nil, nil, 0, fuseErrno(err)
	}
	perm := os.FileMode(mode & 0o777)
	if perm == 0 {
		perm = 0o644
	}
	info := remoteFileInfo{name: path.Base(remotePath), mode: perm, modTime: time.Now()}
	fillFuseAttr(&out.Attr, info)
	child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
	handle := newRemoteFuseFile(n.runtime, remotePath, nil, true, false, version).withOpenContext(ctx, flags)
	return n.NewInode(ctx, child, fuseStableAttr(remotePath, info)), handle, gofuse.FOPEN_DIRECT_IO, gofs.OK
}

func (n *remoteFuseNode) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	writable := flags&gofuse.O_ANYWRITE != 0
	if writable && n.runtime.readOnly {
		return nil, 0, syscall.EROFS
	}
	if info, abs, ok := n.runtime.localInfo(n.remotePath); ok || n.runtime.isLocalOnly(n.remotePath) {
		if !ok {
			return nil, 0, syscall.ENOENT
		}
		if info.IsDir() {
			return nil, 0, syscall.EISDIR
		}
		fd, err := syscall.Open(abs, int(flags), 0)
		if err != nil {
			return nil, 0, fuseErrno(err)
		}
		return gofs.NewLoopbackFile(fd), 0, gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, n.remotePath); err != nil {
		return nil, 0, fuseErrno(err)
	} else if ok {
		if entry.kind() == "dir" {
			return nil, 0, syscall.EISDIR
		}
		data := []byte{}
		if !writable || flags&uint32(syscall.O_TRUNC) == 0 {
			if dirtyData, _, ok := n.runtime.dirtyOpenHandleData(n.remotePath); ok {
				data = dirtyData
			} else {
				readData, readErr := n.runtime.gitReadFile(ctx, entry)
				if readErr != nil {
					return nil, 0, fuseErrno(readErr)
				}
				data = readData
			}
		}
		dirty := writable && flags&uint32(syscall.O_TRUNC) != 0
		return newGitFuseFile(n.runtime, entry.workspace, n.remotePath, data, writable, dirty, entry.fileMode()).withOpenContext(ctx, flags), gofuse.FOPEN_DIRECT_IO, gofs.OK
	}
	stat, err := n.runtime.statRemote(ctx, n.remotePath)
	if err != nil {
		return nil, 0, fuseErrno(err)
	}
	info := stat.info
	if info.IsDir() {
		return nil, 0, syscall.EISDIR
	}
	data := []byte{}
	version := stat.version
	if !writable || flags&uint32(syscall.O_TRUNC) == 0 {
		if dirtyData, dirtyVersion, ok := n.runtime.dirtyOpenHandleData(n.remotePath); ok {
			data = dirtyData
			version = dirtyVersion
		} else {
			data, err = n.runtime.readFile(ctx, n.remotePath, stat.version)
			if err != nil {
				return nil, 0, fuseErrno(err)
			}
		}
	}
	dirty := writable && flags&uint32(syscall.O_TRUNC) != 0
	return newRemoteFuseFile(n.runtime, n.remotePath, data, writable, dirty, version).withOpenContext(ctx, flags), gofuse.FOPEN_DIRECT_IO, gofs.OK
}

func (n *remoteFuseNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.runtime.readOnly {
		return syscall.EROFS
	}
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return errno
	}
	if _, abs, ok := n.runtime.localInfo(remotePath); ok || n.runtime.isLocalOnly(remotePath) {
		if !ok {
			return syscall.ENOENT
		}
		if err := os.Remove(abs); err != nil {
			return fuseErrno(err)
		}
		return gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, remotePath); err != nil {
		return fuseErrno(err)
	} else if ok {
		if err := n.runtime.gitWhiteout(ctx, entry.workspace, remotePath); err != nil {
			return fuseErrno(err)
		}
		if n.runtime.metadata != nil {
			_ = n.runtime.metadata.remove(remotePath, false)
		}
		return gofs.OK
	}
	if err := n.runtime.client.DeleteFile(ctx, remotePath, false); err != nil {
		return fuseErrno(err)
	}
	if n.runtime.metadata != nil {
		_ = n.runtime.metadata.remove(remotePath, false)
	}
	n.runtime.invalidatePrefix(remotePath)
	n.runtime.markDeletedOpenHandles(remotePath)
	return gofs.OK
}

func (n *remoteFuseNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.runtime.readOnly {
		return syscall.EROFS
	}
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return errno
	}
	if _, abs, ok := n.runtime.localInfo(remotePath); ok || n.runtime.isLocalOnly(remotePath) {
		if !ok {
			return syscall.ENOENT
		}
		if err := os.Remove(abs); err != nil {
			return fuseErrno(err)
		}
		return gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, remotePath); err != nil {
		return fuseErrno(err)
	} else if ok {
		if entry.kind() != "dir" {
			return syscall.ENOTDIR
		}
		if err := n.runtime.gitWhiteout(ctx, entry.workspace, remotePath); err != nil {
			return fuseErrno(err)
		}
		if n.runtime.metadata != nil {
			_ = n.runtime.metadata.remove(remotePath, true)
		}
		return gofs.OK
	}
	if err := n.runtime.client.DeleteFile(ctx, remotePath, false); err != nil {
		return fuseErrno(err)
	}
	if n.runtime.metadata != nil {
		_ = n.runtime.metadata.remove(remotePath, true)
	}
	n.runtime.invalidatePrefix(remotePath)
	n.runtime.markDeletedOpenHandles(remotePath)
	return gofs.OK
}

func (n *remoteFuseNode) Rename(ctx context.Context, name string, newParent gofs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.runtime.readOnly {
		return syscall.EROFS
	}
	if flags != 0 {
		return syscall.ENOTSUP
	}
	source, errno := n.childPath(name)
	if errno != gofs.OK {
		return errno
	}
	parent, ok := newParent.(*remoteFuseNode)
	if !ok {
		return syscall.EIO
	}
	target, errno := parent.childPath(newName)
	if errno != gofs.OK {
		return errno
	}
	if n.runtime.shouldUseLocal(source) || parent.runtime.shouldUseLocal(target) {
		sourceAbs, ok := n.runtime.localAbs(source)
		if !ok {
			return syscall.EINVAL
		}
		targetAbs, ok := parent.runtime.localAbs(target)
		if !ok {
			return syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
			return fuseErrno(err)
		}
		if err := os.Rename(sourceAbs, targetAbs); err != nil {
			return fuseErrno(err)
		}
		return gofs.OK
	}
	if sourceEntry, ok, err := n.runtime.gitEntry(ctx, source); err != nil {
		return fuseErrno(err)
	} else if ok {
		targetParent, parentOK, err := parent.runtime.gitParentEntry(ctx, target)
		if err != nil {
			return fuseErrno(err)
		}
		if parentOK && targetParent.workspace.WorkspaceID == sourceEntry.workspace.WorkspaceID {
			data := []byte{}
			if sourceEntry.kind() != "dir" {
				data, err = n.runtime.gitReadFile(ctx, sourceEntry)
				if err != nil {
					return fuseErrno(err)
				}
			}
			if err := n.runtime.gitPutEntry(ctx, sourceEntry.workspace, target, sourceEntry.kind(), sourceEntry.mode(), data); err != nil {
				return fuseErrno(err)
			}
			if err := n.runtime.gitWhiteout(ctx, sourceEntry.workspace, source); err != nil {
				return fuseErrno(err)
			}
			if n.runtime.metadata != nil {
				_ = n.runtime.metadata.move(source, target)
			}
			return gofs.OK
		}
	}
	if parent.runtime.localParentExists(target) {
		sourceAbs, ok := n.runtime.localAbs(source)
		if !ok {
			return syscall.EINVAL
		}
		targetAbs, ok := parent.runtime.localAbs(target)
		if !ok {
			return syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
			return fuseErrno(err)
		}
		if err := os.Rename(sourceAbs, targetAbs); err != nil {
			return fuseErrno(err)
		}
		return gofs.OK
	}
	if err := n.runtime.client.Rename(ctx, source, target); err != nil {
		return fuseErrno(err)
	}
	targetVersion := fuseObjectVersion{}
	if stat, err := statFuseRemoteVersion(ctx, n.runtime.client, target); err == nil {
		targetVersion = stat.version
	}
	if n.runtime.metadata != nil {
		_ = n.runtime.metadata.move(source, target)
	}
	n.runtime.invalidatePrefix(source)
	n.runtime.invalidatePrefix(target)
	n.runtime.retargetOpenHandlesWithVersion(source, target, targetVersion)
	return gofs.OK
}

func (n *remoteFuseNode) Setattr(ctx context.Context, f gofs.FileHandle, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	if f != nil {
		if setter, ok := f.(gofs.FileSetattrer); ok {
			return setter.Setattr(ctx, in, out)
		}
	}
	size, hasSize := in.GetSize()
	_, hasMode := in.GetMode()
	_, hasMTime := in.GetMTime()
	_, hasATime := in.GetATime()
	if !hasSize && !hasMode && !hasMTime && !hasATime {
		return n.Getattr(ctx, f, out)
	}
	if n.runtime.readOnly {
		return syscall.EROFS
	}
	if info, abs, ok := n.runtime.localInfo(n.remotePath); ok || n.runtime.isLocalOnly(n.remotePath) {
		if !ok {
			return syscall.ENOENT
		}
		if size, ok := in.GetSize(); ok {
			if info.IsDir() {
				return syscall.EISDIR
			}
			if err := os.Truncate(abs, int64(size)); err != nil {
				return fuseErrno(err)
			}
		}
		if mode, ok := in.GetMode(); ok {
			if err := os.Chmod(abs, os.FileMode(mode)); err != nil {
				return fuseErrno(err)
			}
		}
		if mtime, ok := in.GetMTime(); ok {
			atime := mtime
			if got, ok := in.GetATime(); ok {
				atime = got
			}
			if err := os.Chtimes(abs, atime, mtime); err != nil {
				return fuseErrno(err)
			}
		}
		next, err := os.Lstat(abs)
		if err != nil {
			return fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, next)
		return gofs.OK
	}
	if hasSize {
		if ok, errno := n.runtime.adoptSingleCallerPathSetattr(ctx, n.remotePath, in, out); ok {
			return errno
		}
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, n.remotePath); err != nil {
		return fuseErrno(err)
	} else if ok {
		if entry.kind() == "dir" && hasSize {
			return syscall.EISDIR
		}
		data := []byte{}
		if entry.kind() != "dir" {
			data, err = n.runtime.gitReadFile(ctx, entry)
			if err != nil {
				return fuseErrno(err)
			}
		}
		if hasSize {
			var errno syscall.Errno
			data, errno = resizeFuseData(data, size)
			if errno != gofs.OK {
				return errno
			}
		}
		mode := entry.mode()
		if got, ok := in.GetMode(); ok {
			mode = gitModeString(entry.kind(), os.FileMode(got))
		}
		if err := n.runtime.gitPutEntry(ctx, entry.workspace, n.remotePath, entry.kind(), mode, data); err != nil {
			return fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, entry.withModeAndSize(mode, int64(len(data))).info())
		return gofs.OK
	}
	stat, err := n.runtime.statRemote(ctx, n.remotePath)
	if err != nil {
		return fuseErrno(err)
	}
	info := stat.info
	if info.IsDir() && hasSize {
		return syscall.EISDIR
	}
	var resized []byte
	if hasSize {
		data, err := n.runtime.readFile(ctx, n.remotePath, stat.version)
		if err != nil {
			return fuseErrno(err)
		}
		var errno syscall.Errno
		resized, errno = resizeFuseData(data, size)
		if errno != gofs.OK {
			return errno
		}
		version, err := n.runtime.writeFile(ctx, n.remotePath, resized, stat.version)
		if err != nil {
			return fuseErrno(err)
		}
		n.runtime.refreshOpenHandlesAfterPathTruncate(n.remotePath, version)
	}
	if mode, ok := in.GetMode(); ok {
		if err := n.runtime.client.Chmod(ctx, n.remotePath, int64(mode)); err != nil {
			return fuseErrno(err)
		}
		if n.runtime.metadata != nil {
			_ = n.runtime.metadata.setMode(n.remotePath, int64(mode))
		}
	}
	if hasSize {
		fillFuseAttrFromData(&out.Attr, n.remotePath, resized)
		return gofs.OK
	}
	return n.Getattr(ctx, f, out)
}

func (n *remoteFuseNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	if _, abs, ok := n.runtime.localInfo(n.remotePath); ok {
		target, err := os.Readlink(abs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		return []byte(target), gofs.OK
	}
	if entry, ok, err := n.runtime.gitEntry(ctx, n.remotePath); err != nil {
		return nil, fuseErrno(err)
	} else if ok && entry.kind() == "symlink" {
		data, err := n.runtime.gitReadFile(ctx, entry)
		if err != nil {
			return nil, fuseErrno(err)
		}
		return data, gofs.OK
	}
	if n.runtime.metadata != nil {
		if target, ok := n.runtime.metadata.symlinkTarget(n.remotePath); ok {
			return []byte(target), gofs.OK
		}
	}
	return nil, syscall.ENOTSUP
}

func (n *remoteFuseNode) Symlink(ctx context.Context, target, name string, out *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if n.runtime.readOnly {
		return nil, syscall.EROFS
	}
	remotePath, errno := n.childPath(name)
	if errno != gofs.OK {
		return nil, errno
	}
	if n.runtime.isLocalOnly(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Symlink(target, abs); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.OK
	}
	if entry, ok, err := n.runtime.gitParentEntry(ctx, remotePath); err != nil {
		return nil, fuseErrno(err)
	} else if ok {
		if err := n.runtime.gitPutEntry(ctx, entry.workspace, remotePath, "symlink", "120000", []byte(target)); err != nil {
			return nil, fuseErrno(err)
		}
		info := remoteFileInfo{name: path.Base(remotePath), size: int64(len(target)), mode: os.ModeSymlink | 0o777, modTime: time.Now()}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("git:"+remotePath, info)), gofs.OK
	}
	if n.runtime.localParentExists(remotePath) {
		abs, ok := n.runtime.localAbs(remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Symlink(target, abs); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+remotePath, info)), gofs.OK
	}
	if err := n.runtime.client.Symlink(ctx, target, remotePath); err != nil {
		return nil, fuseErrno(err)
	}
	if n.runtime.metadata != nil {
		_ = n.runtime.metadata.setSymlink(remotePath, target)
	}
	info := remoteFileInfo{name: path.Base(remotePath), size: int64(len(target)), mode: os.ModeSymlink | 0o777, modTime: time.Now()}
	fillFuseAttr(&out.Attr, info)
	child := &remoteFuseNode{runtime: n.runtime, remotePath: remotePath}
	return n.NewInode(ctx, child, fuseStableAttr(remotePath, info)), gofs.OK
}

func (n *remoteFuseNode) Link(ctx context.Context, target gofs.InodeEmbedder, name string, out *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if n.runtime.readOnly {
		return nil, syscall.EROFS
	}
	sourceNode, ok := target.(*remoteFuseNode)
	if !ok {
		return nil, syscall.EIO
	}
	linkPath, errno := n.childPath(name)
	if errno != gofs.OK {
		return nil, errno
	}
	if sourceNode.runtime.shouldUseLocal(sourceNode.remotePath) || n.runtime.isLocalOnly(linkPath) {
		sourceAbs, ok := sourceNode.runtime.localAbs(sourceNode.remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		linkAbs, ok := n.runtime.localAbs(linkPath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(linkAbs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Link(sourceAbs, linkAbs); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(linkAbs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: linkPath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+linkPath, info)), gofs.OK
	}
	if sourceEntry, ok, err := sourceNode.runtime.gitEntry(ctx, sourceNode.remotePath); err != nil {
		return nil, fuseErrno(err)
	} else if ok {
		targetParent, parentOK, err := n.runtime.gitParentEntry(ctx, linkPath)
		if err != nil {
			return nil, fuseErrno(err)
		}
		if parentOK && targetParent.workspace.WorkspaceID == sourceEntry.workspace.WorkspaceID {
			data, err := sourceNode.runtime.gitReadFile(ctx, sourceEntry)
			if err != nil {
				return nil, fuseErrno(err)
			}
			if err := n.runtime.gitPutEntry(ctx, sourceEntry.workspace, linkPath, sourceEntry.kind(), sourceEntry.mode(), data); err != nil {
				return nil, fuseErrno(err)
			}
			if n.runtime.metadata != nil {
				_ = n.runtime.metadata.copyMetadata(sourceNode.remotePath, linkPath)
			}
			info := sourceEntry.withName(path.Base(linkPath)).info()
			fillFuseAttr(&out.Attr, info)
			child := &remoteFuseNode{runtime: n.runtime, remotePath: linkPath}
			return n.NewInode(ctx, child, fuseStableAttr("git:"+linkPath, info)), gofs.OK
		}
	}
	if n.runtime.localParentExists(linkPath) {
		sourceAbs, ok := sourceNode.runtime.localAbs(sourceNode.remotePath)
		if !ok {
			return nil, syscall.EINVAL
		}
		linkAbs, ok := n.runtime.localAbs(linkPath)
		if !ok {
			return nil, syscall.EINVAL
		}
		if err := os.MkdirAll(filepath.Dir(linkAbs), 0o755); err != nil {
			return nil, fuseErrno(err)
		}
		if err := os.Link(sourceAbs, linkAbs); err != nil {
			return nil, fuseErrno(err)
		}
		info, err := os.Lstat(linkAbs)
		if err != nil {
			return nil, fuseErrno(err)
		}
		fillFuseAttr(&out.Attr, info)
		child := &remoteFuseNode{runtime: n.runtime, remotePath: linkPath}
		return n.NewInode(ctx, child, fuseStableAttr("local:"+linkPath, info)), gofs.OK
	}
	if err := n.runtime.client.Hardlink(ctx, sourceNode.remotePath, linkPath); err != nil {
		return nil, fuseErrno(err)
	}
	if n.runtime.metadata != nil {
		_ = n.runtime.metadata.copyMetadata(sourceNode.remotePath, linkPath)
	}
	info, err := n.runtime.statRemoteInfo(ctx, linkPath)
	if err != nil {
		info = remoteFileInfo{name: path.Base(linkPath), mode: 0o644, modTime: time.Now()}
	}
	fillFuseAttr(&out.Attr, info)
	child := &remoteFuseNode{runtime: n.runtime, remotePath: linkPath}
	return n.NewInode(ctx, child, fuseStableAttr(linkPath, info)), gofs.OK
}

func (n *remoteFuseNode) childPath(name string) (string, syscall.Errno) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.ContainsRune(name, '\x00') {
		return "", syscall.EINVAL
	}
	if n.remotePath == "/" {
		remotePath, err := normalizeRemotePath("/" + name)
		if err != nil {
			return "", syscall.EINVAL
		}
		return remotePath, gofs.OK
	}
	remotePath, err := normalizeRemotePath(path.Join(n.remotePath, name))
	if err != nil {
		return "", syscall.EINVAL
	}
	return remotePath, gofs.OK
}

type remoteFuseFile struct {
	mu               sync.Mutex
	runtime          *remoteFuseRuntime
	remotePath       string
	data             []byte
	version          fuseObjectVersion
	baseSize         int64
	writable         bool
	openSeq          uint64
	openFlags        uint32
	openPID          uint32
	dirty            bool
	dirtyRanges      []fuseDirtyRange
	forceWholeUpload bool
	deleted          bool
	closed           bool
	modTime          time.Time
	mode             os.FileMode
	commit           func(context.Context, *remoteFuseFile) error
}

func newRemoteFuseFile(runtime *remoteFuseRuntime, remotePath string, data []byte, writable bool, dirty bool, version fuseObjectVersion) *remoteFuseFile {
	handle := &remoteFuseFile{
		runtime:    runtime,
		remotePath: remotePath,
		data:       append([]byte(nil), data...),
		version:    version,
		baseSize:   int64(len(data)),
		writable:   writable,
		dirty:      dirty,
		modTime:    time.Now(),
	}
	if dirty {
		handle.markDirtyRange(0, int64(len(data)))
	}
	runtime.registerOpenHandle(handle)
	return handle
}

func (f *remoteFuseFile) withOpenContext(ctx context.Context, flags uint32) *remoteFuseFile {
	f.openFlags = flags
	f.openPID = fuseCallerPID(ctx)
	return f
}

var _ gofs.FileReader = (*remoteFuseFile)(nil)
var _ gofs.FileWriter = (*remoteFuseFile)(nil)
var _ gofs.FileFlusher = (*remoteFuseFile)(nil)
var _ gofs.FileFsyncer = (*remoteFuseFile)(nil)
var _ gofs.FileReleaser = (*remoteFuseFile)(nil)
var _ gofs.FileSetattrer = (*remoteFuseFile)(nil)
var _ gofs.FileGetattrer = (*remoteFuseFile)(nil)

func (f *remoteFuseFile) Read(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if off >= int64(len(f.data)) {
		return gofuse.ReadResultData(nil), gofs.OK
	}
	end := off + int64(len(dest))
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	return gofuse.ReadResultData(append([]byte(nil), f.data[off:end]...)), gofs.OK
}

func (f *remoteFuseFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.writable {
		return 0, syscall.EPERM
	}
	if off < 0 {
		return 0, syscall.EINVAL
	}
	end := off + int64(len(data))
	if end > int64(maxIntValue()) {
		return 0, syscall.EFBIG
	}
	if end > int64(len(f.data)) {
		f.data = append(f.data, bytes.Repeat([]byte{0}, int(end)-len(f.data))...)
	}
	copy(f.data[off:end], data)
	f.dirty = true
	f.markDirtyRange(off, end)
	f.modTime = time.Now()
	return uint32(len(data)), gofs.OK
}

func (f *remoteFuseFile) Flush(ctx context.Context) syscall.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushLocked(ctx)
}

func (f *remoteFuseFile) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushLocked(ctx)
}

func (f *remoteFuseFile) Release(ctx context.Context) syscall.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return gofs.OK
	}
	f.closed = true
	defer f.runtime.unregisterOpenHandle(f)
	return f.flushLocked(ctx)
}

func (f *remoteFuseFile) Setattr(ctx context.Context, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setattrLocked(in, out)
}

func (f *remoteFuseFile) refreshAfterPathTruncate(remotePath string, version fuseObjectVersion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.deleted || !f.writable || f.remotePath != remotePath {
		return
	}
	if f.openFlags&uint32(syscall.O_TRUNC) == 0 {
		return
	}
	f.version = version
}

func (f *remoteFuseFile) setattrLocked(in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	size, ok := in.GetSize()
	if ok {
		if !f.writable {
			return syscall.EPERM
		}
		resized, errno := resizeFuseData(f.data, size)
		if errno != gofs.OK {
			return errno
		}
		f.data = resized
		f.dirty = true
		f.forceWholeUpload = true
		f.modTime = time.Now()
	}
	if mode, ok := in.GetMode(); ok {
		f.mode = os.FileMode(mode)
		f.dirty = true
		f.forceWholeUpload = true
	}
	if out != nil {
		fillFuseAttrForHandle(&out.Attr, f.remotePath, f.data, f.mode)
	}
	return gofs.OK
}

func (f *remoteFuseFile) Getattr(ctx context.Context, out *gofuse.AttrOut) syscall.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	fillFuseAttrForHandle(&out.Attr, f.remotePath, f.data, f.mode)
	return gofs.OK
}

func (f *remoteFuseFile) flushLocked(ctx context.Context) syscall.Errno {
	if !f.writable || !f.dirty {
		return gofs.OK
	}
	if f.deleted {
		f.dirty = false
		return gofs.OK
	}
	if f.commit != nil {
		if err := f.commit(ctx, f); err != nil {
			return fuseErrno(err)
		}
		f.baseSize = int64(len(f.data))
		f.dirtyRanges = nil
		f.forceWholeUpload = false
		f.dirty = false
		return gofs.OK
	}
	dirtyRanges := append([]fuseDirtyRange(nil), f.dirtyRanges...)
	if f.forceWholeUpload {
		dirtyRanges = []fuseDirtyRange{{Start: 0, End: int64(len(f.data))}}
	}
	version, err := f.runtime.writeFileWithDirty(ctx, f.remotePath, f.data, f.version, f.baseSize, dirtyRanges)
	if err != nil {
		return fuseErrno(err)
	}
	f.version = version
	f.baseSize = int64(len(f.data))
	f.dirtyRanges = nil
	f.forceWholeUpload = false
	f.dirty = false
	return gofs.OK
}

func (f *remoteFuseFile) markDirtyRange(start, end int64) {
	if end < start {
		return
	}
	if end == start {
		end = start
	}
	f.dirtyRanges = mergeFuseDirtyRanges(append(f.dirtyRanges, fuseDirtyRange{Start: start, End: end}))
}

func (f *remoteFuseFile) retarget(source, target string, version fuseObjectVersion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	retargeted := false
	switch {
	case f.remotePath == source:
		f.remotePath = target
		retargeted = true
	case strings.HasPrefix(f.remotePath, treePrefix(source)):
		f.remotePath = target + strings.TrimPrefix(f.remotePath, source)
		retargeted = true
	}
	if retargeted && version.known() {
		f.version = version
	}
}

func (f *remoteFuseFile) markDeleted(remotePath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remotePath == remotePath || strings.HasPrefix(f.remotePath, treePrefix(remotePath)) {
		f.deleted = true
	}
}

type fuseRemoteStat struct {
	info    os.FileInfo
	version fuseObjectVersion
}

func statFuseRemote(ctx context.Context, client *apifs.Client, remotePath string) (os.FileInfo, error) {
	stat, err := statFuseRemoteVersion(ctx, client, remotePath)
	if err != nil {
		return nil, err
	}
	return stat.info, nil
}

func statFuseRemoteVersion(ctx context.Context, client *apifs.Client, remotePath string) (fuseRemoteStat, error) {
	metadata, err := client.StatMetadata(ctx, remotePath)
	if err == nil {
		return fuseRemoteStat{info: fileInfoFromMetadata(remotePath, metadata), version: fuseVersionFromMetadata(metadata)}, nil
	}
	if isAPINotFound(err) {
		return fuseRemoteStat{}, os.ErrNotExist
	}
	if shouldFallbackStat(err) {
		stat, statErr := client.Stat(ctx, remotePath)
		if statErr == nil {
			return fuseRemoteStat{info: fileInfoFromStat(remotePath, stat), version: fuseVersionFromStat(stat)}, nil
		}
		if isAPINotFound(statErr) {
			return fuseRemoteStat{}, os.ErrNotExist
		}
		return fuseRemoteStat{}, mapWebDAVError(statErr)
	}
	return fuseRemoteStat{}, mapWebDAVError(err)
}

func fillFuseAttr(attr *gofuse.Attr, info os.FileInfo) {
	mode := uint32(info.Mode().Perm())
	if info.IsDir() {
		if mode == 0 {
			mode = 0o755
		}
		attr.Mode = gofuse.S_IFDIR | mode
		attr.Size = 0
	} else if info.Mode()&os.ModeSymlink != 0 {
		if mode == 0 {
			mode = 0o777
		}
		attr.Mode = gofuse.S_IFLNK | mode
		if info.Size() > 0 {
			attr.Size = uint64(info.Size())
		}
	} else {
		if mode == 0 {
			mode = 0o644
		}
		attr.Mode = gofuse.S_IFREG | mode
		if info.Size() > 0 {
			attr.Size = uint64(info.Size())
		} else {
			attr.Size = 0
		}
	}
	attr.Nlink = 1
	attr.Uid = uint32(os.Getuid())
	attr.Gid = uint32(os.Getgid())
	modTime := info.ModTime()
	attr.SetTimes(&modTime, &modTime, &modTime)
}

func fillFuseAttrFromData(attr *gofuse.Attr, remotePath string, data []byte) {
	fillFuseAttr(attr, remoteFileInfo{name: path.Base(remotePath), size: int64(len(data)), mode: 0o644, modTime: time.Now()})
}

func fillFuseAttrForHandle(attr *gofuse.Attr, remotePath string, data []byte, mode os.FileMode) {
	if mode == 0 {
		mode = 0o644
	}
	fillFuseAttr(attr, remoteFileInfo{name: path.Base(remotePath), size: int64(len(data)), mode: mode, modTime: time.Now()})
}

func fuseStableAttr(remotePath string, info os.FileInfo) gofs.StableAttr {
	mode := uint32(gofuse.S_IFREG)
	if info.IsDir() {
		mode = gofuse.S_IFDIR
	} else if info.Mode()&os.ModeSymlink != 0 {
		mode = gofuse.S_IFLNK
	}
	return gofs.StableAttr{Mode: mode, Ino: fuseInode(remotePath)}
}

func fuseInode(remotePath string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(remotePath); i++ {
		h ^= uint64(remotePath[i])
		h *= 1099511628211
	}
	if h < 2 {
		h += 2
	}
	return h
}

func resizeFuseData(data []byte, size uint64) ([]byte, syscall.Errno) {
	if size > maxIntValue() {
		return nil, syscall.EFBIG
	}
	next := int(size)
	if next <= len(data) {
		return data[:next], gofs.OK
	}
	grown := make([]byte, next)
	copy(grown, data)
	return grown, gofs.OK
}

func maxIntValue() uint64 {
	return uint64(int(^uint(0) >> 1))
}

func fuseErrno(err error) syscall.Errno {
	if err == nil {
		return gofs.OK
	}
	err = mapWebDAVError(err)
	switch {
	case errors.Is(err, errFuseWriteConflict):
		return syscall.ESTALE
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrInvalid):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
