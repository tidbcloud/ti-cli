//go:build !windows

package fs

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

func (s Service) mountVaultForeground(ctx context.Context, inputs vaultMountInputs, checks []MountRuntimeCheck) (MountResult, error) {
	if err := os.MkdirAll(inputs.mountPath, 0o755); err != nil {
		return MountResult{}, apperr.Wrap("vault.create_mount_path", "runtime", 1, fmt.Sprintf("create mount path %q", inputs.mountPath), err)
	}

	timeout := time.Second
	root := newVaultFuseNode(newVaultFuseRuntime(inputs), "", "")
	options := &gofs.Options{
		AttrTimeout:     &timeout,
		EntryTimeout:    &timeout,
		NegativeTimeout: &timeout,
		UID:             uint32(os.Getuid()),
		GID:             uint32(os.Getgid()),
		MountOptions: gofuse.MountOptions{
			FsName:  "ti-vault",
			Name:    "tivault",
			Options: []string{"ro"},
		},
	}
	server, err := gofs.Mount(inputs.mountPath, root, options)
	if err != nil {
		return MountResult{}, apperr.Wrap("vault.mount_fuse", "runtime", 1, fmt.Sprintf("mount ti fs-vault with FUSE at %q", inputs.mountPath), err)
	}

	state, err := mountstate.New(inputs.profile.Name, "vault", inputs.mountPath, "/n/vault", "fuse", inputs.endpoint, os.Getpid(), true, time.Now().UTC())
	if err != nil {
		_ = server.Unmount()
		return MountResult{}, apperr.Wrap("vault.mount_state", "runtime", 1, fmt.Sprintf("create mount state for %q", inputs.mountPath), err)
	}
	stateFile, err := mountstate.Write(inputs.homeDir, state)
	if err != nil {
		_ = server.Unmount()
		return MountResult{}, apperr.Wrap("vault.write_mount_state", "runtime", 1, fmt.Sprintf("write mount state for %q", inputs.mountPath), err)
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
	return vaultMountResult("unmounted", inputs, checks, os.Getpid(), stateFile, ""), nil
}

type vaultFuseRuntime struct {
	client    *apifs.Client
	ownerMode bool
}

func newVaultFuseRuntime(inputs vaultMountInputs) *vaultFuseRuntime {
	return &vaultFuseRuntime{client: inputs.client, ownerMode: inputs.ownerMode}
}

func (r *vaultFuseRuntime) listSecrets(ctx context.Context) ([]string, error) {
	if r.ownerMode {
		secrets, err := r.client.ListVaultSecrets(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(secrets))
		for _, secret := range secrets {
			if name := strings.TrimSpace(secret.Name); name != "" && !strings.Contains(name, "/") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		return names, nil
	}
	names, err := r.client.ListReadableVaultSecrets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r *vaultFuseRuntime) readSecret(ctx context.Context, secret string) (map[string]string, error) {
	if r.ownerMode {
		return r.client.ReadVaultSecretAsOwner(ctx, secret)
	}
	return r.client.ReadVaultSecret(ctx, secret)
}

func (r *vaultFuseRuntime) readField(ctx context.Context, secret, field string) (string, error) {
	if r.ownerMode {
		return r.client.ReadVaultSecretFieldAsOwner(ctx, secret, field)
	}
	return r.client.ReadVaultSecretField(ctx, secret, field)
}

type vaultFuseNode struct {
	gofs.Inode

	runtime *vaultFuseRuntime
	secret  string
	field   string
}

func newVaultFuseNode(runtime *vaultFuseRuntime, secret, field string) *vaultFuseNode {
	return &vaultFuseNode{runtime: runtime, secret: secret, field: field}
}

var _ gofs.NodeGetattrer = (*vaultFuseNode)(nil)
var _ gofs.NodeLookuper = (*vaultFuseNode)(nil)
var _ gofs.NodeReaddirer = (*vaultFuseNode)(nil)
var _ gofs.NodeOpener = (*vaultFuseNode)(nil)
var _ gofs.NodeMkdirer = (*vaultFuseNode)(nil)
var _ gofs.NodeCreater = (*vaultFuseNode)(nil)
var _ gofs.NodeUnlinker = (*vaultFuseNode)(nil)
var _ gofs.NodeRmdirer = (*vaultFuseNode)(nil)
var _ gofs.NodeRenamer = (*vaultFuseNode)(nil)
var _ gofs.NodeSetattrer = (*vaultFuseNode)(nil)

func (n *vaultFuseNode) Getattr(ctx context.Context, f gofs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	if f != nil {
		if getter, ok := f.(gofs.FileGetattrer); ok {
			return getter.Getattr(ctx, out)
		}
	}
	switch {
	case n.secret == "" && n.field == "":
		fillVaultDirAttr(&out.Attr, vaultPath("/", "", ""))
		return gofs.OK
	case n.secret != "" && n.field == "":
		exists, err := n.secretExists(ctx, n.secret)
		if err != nil {
			return fuseErrno(err)
		}
		if !exists {
			return syscall.ENOENT
		}
		fillVaultDirAttr(&out.Attr, vaultPath("/", n.secret, ""))
		return gofs.OK
	case n.secret != "" && n.field != "":
		value, err := n.runtime.readField(ctx, n.secret, n.field)
		if err != nil {
			return fuseErrno(err)
		}
		fillVaultFileAttr(&out.Attr, vaultPath("/", n.secret, n.field), []byte(value))
		return gofs.OK
	default:
		return syscall.ENOENT
	}
}

func (n *vaultFuseNode) Lookup(ctx context.Context, name string, out *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	if invalidVaultChildName(name) {
		return nil, syscall.ENOENT
	}
	if n.secret == "" && n.field == "" {
		secrets, err := n.runtime.listSecrets(ctx)
		if err != nil {
			return nil, fuseErrno(err)
		}
		if !containsString(secrets, name) {
			return nil, syscall.ENOENT
		}
		fillVaultDirAttr(&out.Attr, vaultPath("/", name, ""))
		child := newVaultFuseNode(n.runtime, name, "")
		return n.NewInode(ctx, child, gofs.StableAttr{Mode: gofuse.S_IFDIR, Ino: fuseInode("vault:" + name)}), gofs.OK
	}
	if n.secret != "" && n.field == "" {
		fields, err := n.runtime.readSecret(ctx, n.secret)
		if err != nil {
			return nil, fuseErrno(err)
		}
		value, ok := fields[name]
		if !ok {
			return nil, syscall.ENOENT
		}
		fillVaultFileAttr(&out.Attr, vaultPath("/", n.secret, name), []byte(value))
		child := newVaultFuseNode(n.runtime, n.secret, name)
		return n.NewInode(ctx, child, gofs.StableAttr{Mode: gofuse.S_IFREG, Ino: fuseInode("vault:" + n.secret + "/" + name)}), gofs.OK
	}
	return nil, syscall.ENOTDIR
}

func (n *vaultFuseNode) Readdir(ctx context.Context) (gofs.DirStream, syscall.Errno) {
	if n.field != "" {
		return nil, syscall.ENOTDIR
	}
	var entries []gofuse.DirEntry
	if n.secret == "" {
		secrets, err := n.runtime.listSecrets(ctx)
		if err != nil {
			return nil, fuseErrno(err)
		}
		entries = make([]gofuse.DirEntry, 0, len(secrets))
		for _, secret := range secrets {
			entries = append(entries, gofuse.DirEntry{Name: secret, Mode: gofuse.S_IFDIR, Ino: fuseInode("vault:" + secret)})
		}
		return gofs.NewListDirStream(entries), gofs.OK
	}
	fields, err := n.runtime.readSecret(ctx, n.secret)
	if err != nil {
		return nil, fuseErrno(err)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		if !invalidVaultChildName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	entries = make([]gofuse.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, gofuse.DirEntry{Name: name, Mode: gofuse.S_IFREG, Ino: fuseInode("vault:" + n.secret + "/" + name)})
	}
	return gofs.NewListDirStream(entries), gofs.OK
}

func (n *vaultFuseNode) Open(ctx context.Context, flags uint32) (gofs.FileHandle, uint32, syscall.Errno) {
	if flags&gofuse.O_ANYWRITE != 0 {
		return nil, 0, syscall.EROFS
	}
	if n.secret == "" || n.field == "" {
		return nil, 0, syscall.EISDIR
	}
	value, err := n.runtime.readField(ctx, n.secret, n.field)
	if err != nil {
		return nil, 0, fuseErrno(err)
	}
	return newVaultFuseFile(n.secret, n.field, []byte(value)), gofuse.FOPEN_DIRECT_IO, gofs.OK
}

func (n *vaultFuseNode) Mkdir(context.Context, string, uint32, *gofuse.EntryOut) (*gofs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *vaultFuseNode) Create(context.Context, string, uint32, uint32, *gofuse.EntryOut) (*gofs.Inode, gofs.FileHandle, uint32, syscall.Errno) {
	return nil, nil, 0, syscall.EROFS
}

func (n *vaultFuseNode) Unlink(context.Context, string) syscall.Errno {
	return syscall.EROFS
}

func (n *vaultFuseNode) Rmdir(context.Context, string) syscall.Errno {
	return syscall.EROFS
}

func (n *vaultFuseNode) Rename(context.Context, string, gofs.InodeEmbedder, string, uint32) syscall.Errno {
	return syscall.EROFS
}

func (n *vaultFuseNode) Setattr(context.Context, gofs.FileHandle, *gofuse.SetAttrIn, *gofuse.AttrOut) syscall.Errno {
	return syscall.EROFS
}

func (n *vaultFuseNode) secretExists(ctx context.Context, secret string) (bool, error) {
	secrets, err := n.runtime.listSecrets(ctx)
	if err != nil {
		return false, err
	}
	return containsString(secrets, secret), nil
}

type vaultFuseFile struct {
	secret string
	field  string
	data   []byte
}

func newVaultFuseFile(secret, field string, data []byte) *vaultFuseFile {
	return &vaultFuseFile{secret: secret, field: field, data: append([]byte(nil), data...)}
}

var _ gofs.FileReader = (*vaultFuseFile)(nil)
var _ gofs.FileGetattrer = (*vaultFuseFile)(nil)

func (f *vaultFuseFile) Read(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	_ = ctx
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

func (f *vaultFuseFile) Getattr(ctx context.Context, out *gofuse.AttrOut) syscall.Errno {
	_ = ctx
	fillVaultFileAttr(&out.Attr, vaultPath("/", f.secret, f.field), f.data)
	return gofs.OK
}

func fillVaultDirAttr(attr *gofuse.Attr, stablePath string) {
	attr.Ino = fuseInode("vault:" + stablePath)
	attr.Mode = gofuse.S_IFDIR | 0o555
	attr.Nlink = 2
	attr.Uid = uint32(os.Getuid())
	attr.Gid = uint32(os.Getgid())
	now := time.Now()
	attr.SetTimes(&now, &now, &now)
}

func fillVaultFileAttr(attr *gofuse.Attr, stablePath string, data []byte) {
	attr.Ino = fuseInode("vault:" + stablePath)
	attr.Mode = gofuse.S_IFREG | 0o444
	attr.Nlink = 1
	attr.Size = uint64(len(data))
	attr.Uid = uint32(os.Getuid())
	attr.Gid = uint32(os.Getgid())
	now := time.Now()
	attr.SetTimes(&now, &now, &now)
}

func vaultPath(root, secret, field string) string {
	parts := []string{strings.TrimRight(root, "/")}
	if secret != "" {
		parts = append(parts, secret)
	}
	if field != "" {
		parts = append(parts, field)
	}
	out := strings.Join(parts, "/")
	if out == "" {
		return "/"
	}
	return out
}

func invalidVaultChildName(name string) bool {
	return name == "" || name == "." || name == ".." || strings.Contains(name, "/")
}

func containsString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
