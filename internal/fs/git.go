package fs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

const (
	gitWorkspaceAPITimeout        = 2 * time.Minute
	gitHydrateTimeout             = 30 * time.Minute
	gitStateStorageTarGzNoObjects = "tar.gz-no-objects"
)

type GitWorkspaceCreateOptions struct {
	Profile           *config.Profile
	RootPath          string
	RepoURL           string
	RemoteName        string
	BranchName        string
	BaseCommit        string
	HeadCommit        string
	Mode              string
	WorkspaceKind     string
	CommonWorkspaceID string
	WorktreeName      string
	GitDirRel         string
}

type GitWorkspaceDescribeOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	RootPath    string
}

type GitWorkspaceDeleteOptions struct {
	Profile     *config.Profile
	WorkspaceID string
}

type GitTreeReplaceOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	CommitSHA   string
	NodeJSON    []string
}

type GitTreeListOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	CommitSHA   string
}

type GitStateUpsertOptions struct {
	Profile          *config.Profile
	WorkspaceID      string
	CheckpointCommit string
	StorageType      string
	StorageRef       string
	StorageRefHash   string
	ChecksumSHA256   string
	SizeBytes        int64
	Content          string
}

type GitWorkspaceIDOptions struct {
	Profile     *config.Profile
	WorkspaceID string
}

type GitObjectPackPutOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	Content     string
}

type GitObjectPackDescribeOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	PackID      string
}

type GitOverlayPutOptions struct {
	Profile        *config.Profile
	WorkspaceID    string
	Path           string
	Operation      string
	ResourceKind   string
	Mode           string
	StorageType    string
	StorageRef     string
	StorageRefHash string
	ChecksumSHA256 string
	SizeBytes      int64
	BaseObjectSHA  string
	Content        string
}

type GitOverlayDescribeOptions struct {
	Profile     *config.Profile
	WorkspaceID string
	Path        string
}

type GitWorkspaceCloneOptions struct {
	Profile     *config.Profile
	RepoURL     string
	TargetPath  string
	Blobless    bool
	HydrateMode string
}

type GitWorkspaceHydrateOptions struct {
	Profile    *config.Profile
	TargetPath string
	Timeout    time.Duration
}

type GitWorkspaceRestoreOptions struct {
	Profile    *config.Profile
	TargetPath string
}

type GitWorktreeAddOptions struct {
	Profile      *config.Profile
	BasePath     string
	WorktreePath string
	BranchName   string
	Detach       bool
	Blobless     bool
	HydrateMode  string
	CommitISH    string
}

type GitWorktreeRemoveOptions struct {
	Profile      *config.Profile
	WorktreePath string
	Force        bool
}

type GitWorkspaceCloneResult struct {
	Operation   string             `json:"operation"`
	Workspace   apifs.GitWorkspace `json:"workspace"`
	TargetPath  string             `json:"target_path"`
	RemotePath  string             `json:"remote_path"`
	HeadCommit  string             `json:"head_commit"`
	BranchName  string             `json:"branch_name,omitempty"`
	TreeEntries int                `json:"tree_entries"`
	Hydrate     *GitHydrateResult  `json:"hydrate,omitempty"`
}

type GitHydrateResult struct {
	Operation   string        `json:"operation"`
	WorkspaceID string        `json:"workspace_id"`
	TargetPath  string        `json:"target_path"`
	CommitSHA   string        `json:"commit_sha"`
	Files       int           `json:"files"`
	Objects     int           `json:"objects"`
	Skipped     int           `json:"skipped"`
	Duration    time.Duration `json:"duration"`
}

type GitRestoreResult struct {
	Operation       string `json:"operation"`
	WorkspaceID     string `json:"workspace_id"`
	TargetPath      string `json:"target_path"`
	GitDir          string `json:"git_dir"`
	StateRestored   bool   `json:"state_restored"`
	ObjectPacks     int    `json:"object_packs"`
	ObjectPackBytes int64  `json:"object_pack_bytes"`
	Status          string `json:"status"`
}

type GitWorktreeRemoveResult struct {
	Operation   string `json:"operation"`
	WorkspaceID string `json:"workspace_id"`
	RemotePath  string `json:"remote_path"`
	Status      string `json:"status"`
}

type GitDeleteResult struct {
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Status    string `json:"status"`
}

type mountedGitTarget struct {
	MountPoint  string
	MountRel    string
	RemoteRoot  string
	RemotePath  string
	Profile     string
	LocalRoot   string
	LocalGitDir string
}

type gitHydrateMode string

const (
	gitHydrateModeOff        gitHydrateMode = "off"
	gitHydrateModeBackground gitHydrateMode = "background"
	gitHydrateModeSync       gitHydrateMode = "sync"
)

func (s Service) CreateGitWorkspace(ctx context.Context, opts GitWorkspaceCreateOptions) (apifs.GitWorkspace, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "create ti fs-git workspace")
	if err != nil {
		return apifs.GitWorkspace{}, err
	}
	if strings.TrimSpace(opts.RootPath) == "" {
		return apifs.GitWorkspace{}, apperr.New("git.missing_root_path", "usage", 2, "--root-path is required")
	}
	if strings.TrimSpace(opts.RepoURL) == "" {
		return apifs.GitWorkspace{}, apperr.New("git.missing_repo_url", "usage", 2, "--repo-url is required")
	}
	return client.UpsertGitWorkspace(ctx, apifs.GitWorkspaceRequest{
		RootPath:          strings.TrimSpace(opts.RootPath),
		RepoURL:           strings.TrimSpace(opts.RepoURL),
		RemoteName:        strings.TrimSpace(opts.RemoteName),
		BranchName:        strings.TrimSpace(opts.BranchName),
		BaseCommit:        strings.TrimSpace(opts.BaseCommit),
		HeadCommit:        strings.TrimSpace(opts.HeadCommit),
		Mode:              strings.TrimSpace(opts.Mode),
		WorkspaceKind:     strings.TrimSpace(opts.WorkspaceKind),
		CommonWorkspaceID: strings.TrimSpace(opts.CommonWorkspaceID),
		WorktreeName:      strings.TrimSpace(opts.WorktreeName),
		GitDirRel:         strings.TrimSpace(opts.GitDirRel),
	})
}

func (s Service) ListGitWorkspaces(ctx context.Context, profile *config.Profile) (apifs.GitWorkspacesResponse, error) {
	client, err := s.dataClient(profile, authz.FSGitWorkspaceRead, "list ti fs-git workspaces")
	if err != nil {
		return apifs.GitWorkspacesResponse{}, err
	}
	return client.ListGitWorkspaces(ctx)
}

func (s Service) DescribeGitWorkspace(ctx context.Context, opts GitWorkspaceDescribeOptions) (apifs.GitWorkspace, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "describe ti fs-git workspace")
	if err != nil {
		return apifs.GitWorkspace{}, err
	}
	if strings.TrimSpace(opts.WorkspaceID) != "" {
		return client.GetGitWorkspace(ctx, opts.WorkspaceID)
	}
	return client.GetGitWorkspaceByRoot(ctx, opts.RootPath)
}

func (s Service) DeleteGitWorkspace(ctx context.Context, opts GitWorkspaceDeleteOptions) (GitDeleteResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "delete ti fs-git workspace")
	if err != nil {
		return GitDeleteResult{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return GitDeleteResult{}, err
	}
	if err := client.DeleteGitWorkspace(ctx, workspaceID); err != nil {
		return GitDeleteResult{}, err
	}
	return GitDeleteResult{Operation: "delete_git_workspace", ID: workspaceID, Status: "deleted"}, nil
}

func (s Service) ReplaceGitTree(ctx context.Context, opts GitTreeReplaceOptions) (GitDeleteResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "replace ti fs-git tree")
	if err != nil {
		return GitDeleteResult{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return GitDeleteResult{}, err
	}
	nodes, err := parseGitTreeNodes(opts.NodeJSON)
	if err != nil {
		return GitDeleteResult{}, err
	}
	if err := client.ReplaceGitTree(ctx, workspaceID, apifs.GitTreeReplaceRequest{CommitSHA: strings.TrimSpace(opts.CommitSHA), Nodes: nodes}); err != nil {
		return GitDeleteResult{}, err
	}
	return GitDeleteResult{Operation: "replace_git_tree", ID: workspaceID, Status: "replaced"}, nil
}

func (s Service) ListGitTree(ctx context.Context, opts GitTreeListOptions) (apifs.GitTreeResponse, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "list ti fs-git tree")
	if err != nil {
		return apifs.GitTreeResponse{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitTreeResponse{}, err
	}
	return client.ListGitTree(ctx, workspaceID, strings.TrimSpace(opts.CommitSHA))
}

func (s Service) UpsertGitState(ctx context.Context, opts GitStateUpsertOptions) (apifs.GitState, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "upsert ti fs-git state")
	if err != nil {
		return apifs.GitState{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitState{}, err
	}
	return client.UpsertGitState(ctx, workspaceID, apifs.GitStateRequest{
		CheckpointCommit: strings.TrimSpace(opts.CheckpointCommit),
		StorageType:      strings.TrimSpace(opts.StorageType),
		StorageRef:       strings.TrimSpace(opts.StorageRef),
		StorageRefHash:   strings.TrimSpace(opts.StorageRefHash),
		ChecksumSHA256:   strings.TrimSpace(opts.ChecksumSHA256),
		SizeBytes:        opts.SizeBytes,
		Content:          []byte(opts.Content),
	})
}

func (s Service) DescribeGitState(ctx context.Context, opts GitWorkspaceIDOptions) (apifs.GitState, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "describe ti fs-git state")
	if err != nil {
		return apifs.GitState{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitState{}, err
	}
	return client.GetGitState(ctx, workspaceID)
}

func (s Service) PutGitObjectPack(ctx context.Context, opts GitObjectPackPutOptions) (apifs.GitObjectPack, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "put ti fs-git object pack")
	if err != nil {
		return apifs.GitObjectPack{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitObjectPack{}, err
	}
	return client.PutGitObjectPack(ctx, workspaceID, apifs.GitObjectPackRequest{Content: []byte(opts.Content)})
}

func (s Service) ListGitObjectPacks(ctx context.Context, opts GitWorkspaceIDOptions) (apifs.GitObjectPacksResponse, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "list ti fs-git object packs")
	if err != nil {
		return apifs.GitObjectPacksResponse{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitObjectPacksResponse{}, err
	}
	return client.ListGitObjectPacks(ctx, workspaceID)
}

func (s Service) DescribeGitObjectPack(ctx context.Context, opts GitObjectPackDescribeOptions) (apifs.GitObjectPack, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "describe ti fs-git object pack")
	if err != nil {
		return apifs.GitObjectPack{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitObjectPack{}, err
	}
	if strings.TrimSpace(opts.PackID) == "" {
		return apifs.GitObjectPack{}, apperr.New("git.missing_pack_id", "usage", 2, "--pack-id is required")
	}
	return client.GetGitObjectPack(ctx, workspaceID, opts.PackID)
}

func (s Service) PutGitOverlayEntry(ctx context.Context, opts GitOverlayPutOptions) (apifs.GitOverlayEntry, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceWrite, "put ti fs-git overlay entry")
	if err != nil {
		return apifs.GitOverlayEntry{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitOverlayEntry{}, err
	}
	if strings.TrimSpace(opts.Path) == "" {
		return apifs.GitOverlayEntry{}, apperr.New("git.missing_path", "usage", 2, "--path is required")
	}
	return client.PutGitOverlayEntry(ctx, workspaceID, apifs.GitOverlayEntryRequest{
		Path:           strings.TrimSpace(opts.Path),
		Op:             strings.TrimSpace(opts.Operation),
		Kind:           strings.TrimSpace(opts.ResourceKind),
		Mode:           strings.TrimSpace(opts.Mode),
		StorageType:    strings.TrimSpace(opts.StorageType),
		StorageRef:     strings.TrimSpace(opts.StorageRef),
		StorageRefHash: strings.TrimSpace(opts.StorageRefHash),
		ChecksumSHA256: strings.TrimSpace(opts.ChecksumSHA256),
		SizeBytes:      opts.SizeBytes,
		BaseObjectSHA:  strings.TrimSpace(opts.BaseObjectSHA),
		Content:        []byte(opts.Content),
	})
}

func (s Service) DescribeGitOverlayEntry(ctx context.Context, opts GitOverlayDescribeOptions) (apifs.GitOverlayEntry, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "describe ti fs-git overlay entry")
	if err != nil {
		return apifs.GitOverlayEntry{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitOverlayEntry{}, err
	}
	if strings.TrimSpace(opts.Path) == "" {
		return apifs.GitOverlayEntry{}, apperr.New("git.missing_path", "usage", 2, "--path is required")
	}
	return client.GetGitOverlayEntry(ctx, workspaceID, opts.Path)
}

func (s Service) ListGitOverlayEntries(ctx context.Context, opts GitWorkspaceIDOptions) (apifs.GitOverlayEntriesResponse, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "list ti fs-git overlay entries")
	if err != nil {
		return apifs.GitOverlayEntriesResponse{}, err
	}
	workspaceID, err := requireGitWorkspaceID(opts.WorkspaceID)
	if err != nil {
		return apifs.GitOverlayEntriesResponse{}, err
	}
	return client.ListGitOverlayEntries(ctx, workspaceID)
}

func (s Service) CloneGitWorkspace(ctx context.Context, opts GitWorkspaceCloneOptions) (GitWorkspaceCloneResult, error) {
	return s.drive9GitCloneWorkspace(ctx, opts)
}

func (s Service) HydrateGitWorkspace(ctx context.Context, opts GitWorkspaceHydrateOptions) (GitHydrateResult, error) {
	return s.drive9GitHydrateWorkspace(ctx, opts)
}

func (s Service) RestoreGitWorkspace(ctx context.Context, opts GitWorkspaceRestoreOptions) (GitRestoreResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSGitWorkspaceRead, "restore ti fs-git workspace")
	if err != nil {
		return GitRestoreResult{}, err
	}
	target := strings.TrimSpace(opts.TargetPath)
	if target == "" {
		return GitRestoreResult{}, apperr.New("git.missing_target_path", "usage", 2, "--target-path is required")
	}
	resolved, err := s.resolveMountedGitTarget(target)
	if err != nil {
		return GitRestoreResult{}, err
	}
	apiCtx, cancel := context.WithTimeout(ctx, gitWorkspaceAPITimeout)
	workspace, err := client.GetGitWorkspaceByRoot(apiCtx, resolved.RemotePath)
	cancel()
	if err != nil {
		return GitRestoreResult{}, err
	}
	result, err := restoreGitWorkspaceLocalState(ctx, client, workspace, resolved, target)
	if err != nil {
		return GitRestoreResult{}, err
	}
	return result, nil
}

func (s Service) AddGitWorktree(ctx context.Context, opts GitWorktreeAddOptions) (GitWorkspaceCloneResult, error) {
	return s.drive9GitAddWorktree(ctx, opts)
}

func (s Service) RemoveGitWorktree(ctx context.Context, opts GitWorktreeRemoveOptions) (GitWorktreeRemoveResult, error) {
	return s.drive9GitRemoveWorktree(ctx, opts)
}

func requireGitWorkspaceID(raw string) (string, error) {
	workspaceID := strings.TrimSpace(raw)
	if workspaceID == "" {
		return "", apperr.New("git.missing_workspace_id", "usage", 2, "--workspace-id is required")
	}
	return workspaceID, nil
}

func parseGitTreeNodes(rawNodes []string) ([]apifs.GitTreeNode, error) {
	if len(rawNodes) == 0 {
		return nil, apperr.New("git.missing_tree_nodes", "usage", 2, "at least one --node-json is required")
	}
	nodes := make([]apifs.GitTreeNode, 0, len(rawNodes))
	for i, raw := range rawNodes {
		var node apifs.GitTreeNode
		if err := json.Unmarshal([]byte(raw), &node); err != nil {
			return nil, apperr.Wrap("git.decode_node_json", "usage", 2, fmt.Sprintf("decode --node-json %d", i+1), err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (s Service) hydrateGitWorkspaceWithClient(ctx context.Context, client *apifs.Client, opts GitWorkspaceHydrateOptions, resolved mountedGitTarget, workspace apifs.GitWorkspace) (GitHydrateResult, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = gitHydrateTimeout
	}
	hydrateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	apiCtx, apiCancel := context.WithTimeout(hydrateCtx, gitWorkspaceAPITimeout)
	tree, err := client.ListGitTree(apiCtx, workspace.WorkspaceID, workspace.HeadCommit)
	apiCancel()
	if err != nil {
		return GitHydrateResult{}, err
	}
	started := time.Now()
	result := GitHydrateResult{
		Operation:   "hydrate_git_workspace",
		WorkspaceID: workspace.WorkspaceID,
		TargetPath:  opts.TargetPath,
		CommitSHA:   workspace.HeadCommit,
	}
	for _, node := range tree.Nodes {
		if node.Kind == "dir" || strings.TrimSpace(node.ObjectSHA) == "" {
			result.Skipped++
			continue
		}
		if err := runGit(hydrateCtx, opts.TargetPath, "cat-file", "-e", node.ObjectSHA+"^{object}"); err != nil {
			return GitHydrateResult{}, apperr.Wrap("git.hydrate_object", "runtime", 1, fmt.Sprintf("hydrate git object for %s", node.Path), err)
		}
		if node.Kind == "file" {
			result.Files++
		}
		result.Objects++
	}
	result.Duration = time.Since(started)
	_ = resolved
	return result, nil
}

func resolveGitHydrateMode(raw string, blobless bool) (gitHydrateMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "auto" {
		if blobless {
			return gitHydrateModeBackground, nil
		}
		return gitHydrateModeOff, nil
	}
	switch gitHydrateMode(raw) {
	case gitHydrateModeOff:
		return gitHydrateModeOff, nil
	case gitHydrateModeBackground, gitHydrateModeSync:
		if !blobless {
			return "", apperr.New("git.invalid_hydrate_mode", "usage", 2, fmt.Sprintf("--hydrate=%s requires --blobless", raw))
		}
		return gitHydrateMode(raw), nil
	default:
		return "", apperr.New("git.invalid_hydrate_mode", "usage", 2, fmt.Sprintf("invalid --hydrate %q; valid values are auto, background, sync, off", raw))
	}
}

func (s Service) resolveMountedGitTarget(target string) (mountedGitTarget, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return mountedGitTarget{}, err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return mountedGitTarget{}, apperr.Wrap("git.resolve_target_path", "usage", 2, "resolve --target-path", err)
	}
	candidate := filepath.Clean(absTarget)
	for {
		state, _, err := mountstate.Read(homeDir, candidate)
		if err == nil {
			if strings.TrimSpace(state.RemotePath) == "" {
				return mountedGitTarget{}, apperr.New("git.mount_missing_remote_root", "runtime", 1, fmt.Sprintf("ti fs mount metadata for %q does not include remote_path", candidate))
			}
			absMount, rel, ok, err := relToMountedTarget(absTarget, state.MountPath)
			if err != nil {
				return mountedGitTarget{}, err
			}
			if !ok {
				absMount, rel, ok, err = relToMountedTarget(absTarget, candidate)
				if err != nil {
					return mountedGitTarget{}, err
				}
			}
			if !ok {
				return mountedGitTarget{}, apperr.New("git.target_outside_mount", "usage", 2, fmt.Sprintf("target %q is outside ti fs mount %q", target, candidate))
			}
			localPath := "/"
			if rel != "." {
				localPath = filepath.ToSlash(rel)
			}
			remotePath := toRemotePath(state.RemotePath, localPath)
			localGitDir, err := localGitDirForMountedTarget(state.LocalRoot, rel)
			if err != nil {
				return mountedGitTarget{}, err
			}
			return mountedGitTarget{
				MountPoint:  absMount,
				MountRel:    rel,
				RemoteRoot:  state.RemotePath,
				RemotePath:  remotePath,
				Profile:     state.Profile,
				LocalRoot:   state.LocalRoot,
				LocalGitDir: localGitDir,
			}, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	return mountedGitTarget{}, apperr.New("git.target_not_mounted", "usage", 2, fmt.Sprintf("target %q is not inside a ti fs mount with readable mount metadata", target))
}

func relToMountedTarget(absTarget, mountPoint string) (absMount string, rel string, ok bool, err error) {
	mountPoint = strings.TrimSpace(mountPoint)
	if mountPoint == "" {
		return "", "", false, nil
	}
	absMount, err = filepath.Abs(mountPoint)
	if err != nil {
		return "", "", false, apperr.Wrap("git.resolve_mount_path", "runtime", 1, "resolve mount path", err)
	}
	rel, err = filepath.Rel(absMount, absTarget)
	if err != nil {
		return "", "", false, apperr.Wrap("git.map_target_to_mount", "runtime", 1, "map target to mount", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absMount, "", false, nil
	}
	return absMount, rel, true, nil
}

func localGitDirForMountedTarget(localRoot, rel string) (string, error) {
	localRoot = strings.TrimSpace(localRoot)
	if localRoot == "" {
		return "", nil
	}
	if !filepath.IsAbs(localRoot) {
		return "", apperr.New("git.invalid_local_root", "runtime", 1, fmt.Sprintf("ti fs mount metadata local_root must be absolute, got %q", localRoot))
	}
	localPath := filepath.Join(localRoot, "overlay")
	if rel != "" && rel != "." {
		localPath = filepath.Join(localPath, rel)
	}
	return filepath.Join(localPath, ".git"), nil
}

func localOverlayRootForMountedTarget(resolved mountedGitTarget) (string, error) {
	localRoot := strings.TrimSpace(resolved.LocalRoot)
	if localRoot == "" {
		return "", nil
	}
	if !filepath.IsAbs(localRoot) {
		return "", apperr.New("git.invalid_local_root", "runtime", 1, fmt.Sprintf("ti fs mount metadata local_root must be absolute, got %q", localRoot))
	}
	localPath := filepath.Join(localRoot, "overlay")
	if resolved.MountRel != "" && resolved.MountRel != "." {
		localPath = filepath.Join(localPath, resolved.MountRel)
	}
	return localPath, nil
}

func sameTIMount(a, b mountedGitTarget) bool {
	return filepath.Clean(a.MountPoint) == filepath.Clean(b.MountPoint) &&
		strings.TrimRight(a.RemoteRoot, "/") == strings.TrimRight(b.RemoteRoot, "/") &&
		filepath.Clean(a.LocalRoot) == filepath.Clean(b.LocalRoot)
}

func mainGitStateDirForTarget(target string, resolved mountedGitTarget) (string, error) {
	gitDir := filepath.Join(target, ".git")
	if resolved.LocalGitDir == "" {
		return gitDir, nil
	}
	info, err := os.Stat(resolved.LocalGitDir)
	if err == nil {
		if !info.IsDir() {
			return "", apperr.New("git.invalid_git_state_dir", "runtime", 1, fmt.Sprintf("local .git checkpoint path %s is not a directory", resolved.LocalGitDir))
		}
		return resolved.LocalGitDir, nil
	}
	if !os.IsNotExist(err) {
		return "", apperr.Wrap("git.stat_local_git_state_dir", "runtime", 1, "stat local .git checkpoint path", err)
	}
	return gitDir, nil
}

func uploadGitStateCheckpoint(ctx context.Context, client *apifs.Client, workspaceID, checkpointCommit, gitDir string) error {
	pack, err := packReachableGitObjects(ctx, gitDir)
	if err != nil {
		return err
	}
	if len(pack) > 0 {
		apiCtx, cancel := context.WithTimeout(ctx, gitWorkspaceAPITimeout)
		_, err := client.PutGitObjectPack(apiCtx, workspaceID, apifs.GitObjectPackRequest{Content: pack})
		cancel()
		if err != nil {
			return err
		}
	}
	gitState, err := archiveGitStateDir(gitDir)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(gitState)
	apiCtx, cancel := context.WithTimeout(ctx, gitWorkspaceAPITimeout)
	defer cancel()
	_, err = client.UpsertGitState(apiCtx, workspaceID, apifs.GitStateRequest{
		CheckpointCommit: checkpointCommit,
		StorageType:      gitStateStorageTarGzNoObjects,
		ChecksumSHA256:   hex.EncodeToString(sum[:]),
		SizeBytes:        int64(len(gitState)),
		Content:          gitState,
	})
	return err
}

func restoreGitWorkspaceLocalState(ctx context.Context, client *apifs.Client, workspace apifs.GitWorkspace, resolved mountedGitTarget, targetPath string) (GitRestoreResult, error) {
	worktreeRoot, gitDir, err := restoreGitLayout(ctx, client, workspace, resolved)
	if err != nil {
		return GitRestoreResult{}, err
	}
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return GitRestoreResult{}, apperr.Wrap("git.restore_worktree_root", "runtime", 1, "create git worktree root", err)
	}
	apiCtx, cancel := context.WithTimeout(ctx, gitWorkspaceAPITimeout)
	state, err := client.GetGitState(apiCtx, workspace.WorkspaceID)
	cancel()
	if err != nil {
		return GitRestoreResult{}, err
	}
	if len(state.Content) == 0 {
		return GitRestoreResult{}, apperr.New("git.empty_git_state", "runtime", 1, fmt.Sprintf("git workspace %s has no inline git state content to restore", workspace.WorkspaceID))
	}
	if err := restoreGitStateArchive(state.Content, gitDir); err != nil {
		return GitRestoreResult{}, err
	}
	apiCtx, cancel = context.WithTimeout(ctx, gitWorkspaceAPITimeout)
	packs, err := client.ListGitObjectPacks(apiCtx, workspace.WorkspaceID)
	cancel()
	if err != nil {
		return GitRestoreResult{}, err
	}
	var packCount int
	var packBytes int64
	for _, pack := range packs.Packs {
		if strings.TrimSpace(pack.PackID) != "" && len(pack.Content) == 0 {
			apiCtx, cancel = context.WithTimeout(ctx, gitWorkspaceAPITimeout)
			pack, err = client.GetGitObjectPack(apiCtx, workspace.WorkspaceID, pack.PackID)
			cancel()
			if err != nil {
				return GitRestoreResult{}, err
			}
		}
		if len(pack.Content) == 0 {
			continue
		}
		if err := unpackGitObjectPack(ctx, gitDir, pack.Content); err != nil {
			return GitRestoreResult{}, err
		}
		packCount++
		packBytes += int64(len(pack.Content))
	}
	if err := runGit(ctx, worktreeRoot, "config", "gc.auto", "0"); err != nil {
		return GitRestoreResult{}, err
	}
	configureFastCloneGitOptimizations(ctx, worktreeRoot)
	return GitRestoreResult{
		Operation:       "restore_git_workspace",
		WorkspaceID:     workspace.WorkspaceID,
		TargetPath:      targetPath,
		GitDir:          gitDir,
		StateRestored:   true,
		ObjectPacks:     packCount,
		ObjectPackBytes: packBytes,
		Status:          "restored",
	}, nil
}

func restoreGitLayout(ctx context.Context, client *apifs.Client, workspace apifs.GitWorkspace, resolved mountedGitTarget) (worktreeRoot string, gitDir string, err error) {
	worktreeRoot, err = localOverlayRootForMountedTarget(resolved)
	if err != nil {
		return "", "", err
	}
	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(resolved.MountPoint, resolved.MountRel)
	}
	gitDir = filepath.Join(worktreeRoot, ".git")
	if workspace.WorkspaceKind != "linked" || strings.TrimSpace(workspace.CommonWorkspaceID) == "" || strings.TrimSpace(workspace.GitDirRel) == "" {
		return worktreeRoot, gitDir, nil
	}
	apiCtx, cancel := context.WithTimeout(ctx, gitWorkspaceAPITimeout)
	common, err := client.GetGitWorkspace(apiCtx, workspace.CommonWorkspaceID)
	cancel()
	if err != nil {
		return "", "", err
	}
	commonRel, ok := remoteToLocalPath(resolved.RemoteRoot, common.RootPath)
	if !ok {
		return "", "", apperr.New("git.common_workspace_outside_mount", "runtime", 1, fmt.Sprintf("common workspace root %q is outside mounted root %q", common.RootPath, resolved.RemoteRoot))
	}
	commonRoot, err := overlayPathForArchivePath(resolved.LocalRoot, commonRel)
	if err != nil {
		return "", "", err
	}
	gitDir = filepath.Join(commonRoot, ".git", filepath.FromSlash(workspace.GitDirRel))
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return "", "", err
	}
	relGitDir, err := filepath.Rel(worktreeRoot, gitDir)
	if err != nil {
		relGitDir = gitDir
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, ".git"), []byte("gitdir: "+filepath.ToSlash(relGitDir)+"\n"), 0o644); err != nil {
		return "", "", err
	}
	return worktreeRoot, gitDir, nil
}

func restoreGitStateArchive(content []byte, gitDir string) error {
	if len(content) == 0 {
		return apperr.New("git.empty_git_state_archive", "runtime", 1, "git state archive is empty")
	}
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return apperr.Wrap("git.restore_git_dir", "runtime", 1, "create .git directory", err)
	}
	if err := removeGitStateExceptObjects(gitDir); err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return apperr.Wrap("git.read_git_state_archive", "runtime", 1, "read git state archive", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return apperr.Wrap("git.extract_git_state_archive", "runtime", 1, "read git state archive entry", err)
		}
		target, err := safeGitStateArchiveTarget(gitDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(target, data, mode); err != nil {
				return err
			}
		default:
			return apperr.New("git.unsupported_git_state_entry", "runtime", 1, fmt.Sprintf("unsupported git state archive entry %q type %d", hdr.Name, hdr.Typeflag))
		}
	}
	return nil
}

func safeGitStateArchiveTarget(gitDir, name string) (string, error) {
	clean := pathpkg.Clean(strings.TrimPrefix(filepath.ToSlash(name), "/"))
	if clean == "." || clean == "" || clean == "objects" || strings.HasPrefix(clean, "objects/") {
		return "", apperr.New("git.invalid_git_state_entry", "runtime", 1, fmt.Sprintf("invalid git state archive entry %q", name))
	}
	target := filepath.Join(gitDir, filepath.FromSlash(clean))
	rel, err := filepath.Rel(gitDir, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", apperr.New("git.invalid_git_state_entry", "runtime", 1, fmt.Sprintf("git state archive entry escapes .git: %q", name))
	}
	return target, nil
}

func removeGitStateExceptObjects(gitDir string) error {
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "objects" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(gitDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func unpackGitObjectPack(ctx context.Context, gitDir string, content []byte) error {
	if err := os.MkdirAll(filepath.Join(gitDir, "objects"), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "unpack-objects", "-r")
	cmd.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return apperr.Wrap("git.unpack_object_pack", "runtime", 1, "git unpack-objects: "+msg, err)
		}
		return apperr.Wrap("git.unpack_object_pack", "runtime", 1, "git unpack-objects", err)
	}
	return nil
}

func packReachableGitObjects(ctx context.Context, gitDir string) ([]byte, error) {
	ids, err := reachableGitObjectIDs(ctx, gitDir)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	objects := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		objects[id] = struct{}{}
	}
	pack, err := packGitObjects(ctx, gitDir, objects)
	if err != nil {
		return nil, err
	}
	if len(pack) > 256<<20 {
		return nil, apperr.New("git.object_pack_too_large", "runtime", 1, "git object pack exceeds 256 MiB client restore limit")
	}
	return pack, nil
}

func reachableGitObjectIDs(ctx context.Context, gitDir string) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(raw string) {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			return
		}
		id := strings.ToLower(strings.TrimSpace(fields[0]))
		if isGitObjectID(id) {
			seen[id] = struct{}{}
		}
	}
	if out, err := gitCommandOutput(ctx, "--git-dir", gitDir, "rev-list", "--objects", "--all"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			add(line)
		}
	} else if !isMissingGitObjectError(err) {
		return nil, err
	}
	if out, err := gitCommandOutput(ctx, "--git-dir", gitDir, "ls-files", "-s"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				add(fields[1])
			}
		}
	} else if !isMissingGitObjectError(err) {
		return nil, err
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func archiveGitStateDir(gitDir string) ([]byte, error) {
	info, err := os.Stat(gitDir)
	if err != nil {
		return nil, apperr.Wrap("git.stat_git_state", "runtime", 1, "stat .git checkpoint path", err)
	}
	if !info.IsDir() {
		return nil, apperr.New("git.invalid_git_state", "runtime", 1, fmt.Sprintf(".git checkpoint path %s is not a directory", gitDir))
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(gitDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(gitDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if skipGitStatePath(relSlash, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = relSlash
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return nil, apperr.Wrap("git.archive_git_state", "runtime", 1, "archive .git checkpoint", walkErr)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func skipGitStatePath(rel string, entry fs.DirEntry) bool {
	clean := pathpkg.Clean(rel)
	if clean == "objects" || strings.HasPrefix(clean, "objects/") {
		return true
	}
	if strings.HasSuffix(clean, ".lock") {
		return true
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if part == "objects" {
			if i > 0 && parts[i-1] == "modules" {
				return true
			}
		}
	}
	_ = entry
	return false
}

func linkedWorktreeGitDirMetadata(gitFile, commonGitDir string) (gitDir string, worktreeName string, gitDirRel string, err error) {
	if strings.TrimSpace(gitFile) == "" {
		return "", "", "", apperr.New("git.missing_linked_git_file", "runtime", 1, "linked worktree .git file path is unavailable")
	}
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", "", "", apperr.Wrap("git.read_linked_git_file", "runtime", 1, "read linked worktree .git file", err)
	}
	gitDir, err = parseGitDirFile(data, filepath.Dir(gitFile))
	if err != nil {
		return "", "", "", err
	}
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", "", "", apperr.Wrap("git.stat_linked_gitdir", "runtime", 1, "stat linked worktree gitdir", err)
	}
	if !info.IsDir() {
		return "", "", "", apperr.New("git.invalid_linked_gitdir", "runtime", 1, fmt.Sprintf("linked worktree gitdir %s is not a directory", gitDir))
	}
	worktreeName = filepath.Base(gitDir)
	if worktreeName == "." || worktreeName == string(filepath.Separator) || worktreeName == "" {
		return "", "", "", apperr.New("git.invalid_worktree_name", "runtime", 1, fmt.Sprintf("could not derive linked worktree name from gitdir %s", gitDir))
	}
	gitDirRel = filepath.ToSlash(filepath.Join("worktrees", worktreeName))
	if commonGitDir != "" {
		if rel, relErr := filepath.Rel(commonGitDir, gitDir); relErr == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			gitDirRel = filepath.ToSlash(rel)
		}
	}
	return gitDir, worktreeName, gitDirRel, nil
}

func parseGitDirFile(data []byte, baseDir string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		gitDir, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			continue
		}
		gitDir = strings.TrimSpace(gitDir)
		if gitDir == "" {
			return "", apperr.New("git.empty_gitdir", "runtime", 1, "linked worktree .git file has empty gitdir")
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(baseDir, gitDir)
		}
		return filepath.Clean(gitDir), nil
	}
	return "", apperr.New("git.invalid_gitdir_file", "runtime", 1, "linked worktree .git file does not contain gitdir")
}

func gitFastWorktreeAddCommit(branch string, detach bool, commitISH, resolvedCommit string) string {
	if strings.TrimSpace(commitISH) == "" {
		return ""
	}
	if strings.TrimSpace(branch) != "" || detach {
		return resolvedCommit
	}
	return strings.TrimSpace(commitISH)
}

func gitWorktreeStatusClean(ctx context.Context, worktreePath string) (bool, string, error) {
	status, err := gitOutput(ctx, worktreePath, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return false, "", err
	}
	status = strings.TrimSpace(status)
	return status == "", status, nil
}

func initializeFastCloneIndex(ctx context.Context, repoDir, commitSHA string) error {
	if err := runGit(ctx, repoDir, "read-tree", "--reset", commitSHA); err != nil {
		return apperr.Wrap("git.initialize_index", "runtime", 1, "initialize git index", err)
	}
	return nil
}

func configureFastCloneGitOptimizations(ctx context.Context, repoDir string) {
	_ = runGit(ctx, repoDir, "config", "gc.auto", "0")
	_ = runGit(ctx, repoDir, "config", "maintenance.auto", "false")
	if err := runGit(ctx, repoDir, "update-index", "--test-untracked-cache"); err == nil {
		_ = runGit(ctx, repoDir, "config", "core.untrackedCache", "true")
	}
	if err := runGit(ctx, repoDir, "config", "core.splitIndex", "true"); err == nil {
		_ = runGit(ctx, repoDir, "update-index", "--split-index")
	}
}

func gitListTree(ctx context.Context, repoDir, commitSHA string, includeSizes bool) ([]apifs.GitTreeNode, error) {
	args := []string{"ls-tree", "-r", "-t"}
	if includeSizes {
		args = append(args, "-l")
	}
	args = append(args, "-z", commitSHA)
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, apperr.Wrap("git.list_tree", "runtime", 1, "git ls-tree: "+msg, err)
		}
		return nil, apperr.Wrap("git.list_tree", "runtime", 1, "git ls-tree", err)
	}
	nodes, err := parseGitLsTree(out)
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

func parseGitLsTree(out []byte) ([]apifs.GitTreeNode, error) {
	records := bytes.Split(out, []byte{0})
	nodes := make([]apifs.GitTreeNode, 0, len(records))
	for _, rec := range records {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, apperr.New("git.invalid_tree_record", "runtime", 1, "git ls-tree record missing path separator")
		}
		meta := strings.Fields(string(rec[:tab]))
		if len(meta) < 3 {
			return nil, apperr.New("git.invalid_tree_record", "runtime", 1, fmt.Sprintf("git ls-tree record metadata has %d fields", len(meta)))
		}
		mode, gitType, objectSHA := meta[0], meta[1], meta[2]
		gitPath := string(rec[tab+1:])
		parent, name, err := splitGitManifestPath(gitPath)
		if err != nil {
			return nil, err
		}
		kind, err := gitTreeKind(mode, gitType)
		if err != nil {
			return nil, err
		}
		size := int64(-1)
		if len(meta) >= 4 && meta[3] != "-" {
			size, err = strconv.ParseInt(meta[3], 10, 64)
			if err != nil {
				return nil, apperr.Wrap("git.invalid_tree_size", "runtime", 1, fmt.Sprintf("invalid git tree size %q for %q", meta[3], gitPath), err)
			}
		}
		nodes = append(nodes, apifs.GitTreeNode{
			Path:       gitPath,
			ParentPath: parent,
			Name:       name,
			Kind:       kind,
			Mode:       mode,
			ObjectSHA:  objectSHA,
			SizeBytes:  size,
		})
	}
	return nodes, nil
}

func splitGitManifestPath(raw string) (string, string, error) {
	raw = strings.Trim(raw, "/")
	if raw == "" || strings.Contains(raw, "\x00") {
		return "", "", apperr.New("git.invalid_tree_path", "runtime", 1, fmt.Sprintf("invalid git tree path %q", raw))
	}
	parent := pathpkg.Dir(raw)
	if parent == "." {
		parent = ""
	}
	name := pathpkg.Base(raw)
	if name == "." || name == "/" || name == "" {
		return "", "", apperr.New("git.invalid_tree_path", "runtime", 1, fmt.Sprintf("invalid git tree path %q", raw))
	}
	return parent, name, nil
}

func gitTreeKind(mode, gitType string) (string, error) {
	switch gitType {
	case "tree":
		return "dir", nil
	case "blob":
		if mode == "120000" {
			return "symlink", nil
		}
		return "file", nil
	case "commit":
		return "submodule", nil
	default:
		return "", apperr.New("git.unknown_tree_kind", "runtime", 1, fmt.Sprintf("unknown git tree type %q", gitType))
	}
}

func runGit(ctx context.Context, repoDir string, args ...string) error {
	if repoDir != "" {
		args = append([]string{"-C", repoDir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func gitOutput(ctx context.Context, repoDir string, args ...string) (string, error) {
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCommandOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
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

func packGitObjects(ctx context.Context, gitDir string, objects map[string]struct{}) ([]byte, error) {
	if len(objects) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	cmd := exec.CommandContext(ctx, "git", "--git-dir", gitDir, "pack-objects", "--stdout")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("git pack-objects: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("git pack-objects: %w", err)
	}
	return stdout.Bytes(), nil
}

func isMissingGitObjectError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "missing") ||
		strings.Contains(msg, "Not a valid object name") ||
		strings.Contains(msg, "could not get object info") ||
		strings.Contains(msg, "bad object")
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func startGitHydrateBackground(ctx context.Context, targetPath, profileName string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"fs-git", "hydrate-git-workspace", "--target-path", targetPath, "--timeout", gitHydrateTimeout.String()}
	if strings.TrimSpace(profileName) != "" {
		args = append(args, "--profile", profileName)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func sanitizeGitRepoURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return raw
	}
	parsed.User = nil
	return parsed.String()
}

func gitProfileName(profile *config.Profile) string {
	if profile == nil {
		return ""
	}
	return profile.Name
}
