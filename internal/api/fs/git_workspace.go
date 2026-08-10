package fs

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type GitWorkspaceRequest struct {
	RootPath          string `json:"root_path"`
	RepoURL           string `json:"repo_url"`
	RemoteName        string `json:"remote_name,omitempty"`
	BranchName        string `json:"branch_name,omitempty"`
	BaseCommit        string `json:"base_commit,omitempty"`
	HeadCommit        string `json:"head_commit,omitempty"`
	Mode              string `json:"mode,omitempty"`
	WorkspaceKind     string `json:"workspace_kind,omitempty"`
	CommonWorkspaceID string `json:"common_workspace_id,omitempty"`
	WorktreeName      string `json:"worktree_name,omitempty"`
	GitDirRel         string `json:"gitdir_rel,omitempty"`
}

type GitWorkspace struct {
	WorkspaceID       string    `json:"workspace_id"`
	RootPath          string    `json:"root_path"`
	RepoURL           string    `json:"repo_url"`
	RemoteName        string    `json:"remote_name"`
	BranchName        string    `json:"branch_name"`
	BaseCommit        string    `json:"base_commit"`
	HeadCommit        string    `json:"head_commit"`
	Mode              string    `json:"mode"`
	WorkspaceKind     string    `json:"workspace_kind"`
	CommonWorkspaceID string    `json:"common_workspace_id"`
	WorktreeName      string    `json:"worktree_name"`
	GitDirRel         string    `json:"gitdir_rel"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type GitWorkspacesResponse struct {
	Workspaces []GitWorkspace `json:"workspaces"`
}

type GitTreeReplaceRequest struct {
	CommitSHA string        `json:"commit_sha"`
	Nodes     []GitTreeNode `json:"nodes"`
}

type GitTreeNode struct {
	WorkspaceID string    `json:"workspace_id,omitempty"`
	CommitSHA   string    `json:"commit_sha,omitempty"`
	Path        string    `json:"path"`
	ParentPath  string    `json:"parent_path"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Mode        string    `json:"mode"`
	ObjectSHA   string    `json:"object_sha"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

type GitTreeResponse struct {
	Nodes []GitTreeNode `json:"nodes"`
}

type GitStateRequest struct {
	CheckpointCommit string `json:"checkpoint_commit,omitempty"`
	StorageType      string `json:"storage_type,omitempty"`
	StorageRef       string `json:"storage_ref,omitempty"`
	StorageRefHash   string `json:"storage_ref_hash,omitempty"`
	ChecksumSHA256   string `json:"checksum_sha256,omitempty"`
	SizeBytes        int64  `json:"size_bytes,omitempty"`
	Content          []byte `json:"content,omitempty"`
}

type GitState struct {
	WorkspaceID      string    `json:"workspace_id"`
	CheckpointCommit string    `json:"checkpoint_commit"`
	StorageType      string    `json:"storage_type"`
	StorageRef       string    `json:"storage_ref"`
	StorageRefHash   string    `json:"storage_ref_hash"`
	ChecksumSHA256   string    `json:"checksum_sha256"`
	SizeBytes        int64     `json:"size_bytes"`
	Content          []byte    `json:"content,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

type GitObjectPackRequest struct {
	Content []byte `json:"content"`
}

type GitObjectPack struct {
	WorkspaceID    string    `json:"workspace_id"`
	PackID         string    `json:"pack_id"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	SizeBytes      int64     `json:"size_bytes"`
	Content        []byte    `json:"content,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

type GitObjectPacksResponse struct {
	Packs []GitObjectPack `json:"packs"`
}

type GitOverlayEntryRequest struct {
	Path           string `json:"path"`
	Op             string `json:"op,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Mode           string `json:"mode,omitempty"`
	StorageType    string `json:"storage_type,omitempty"`
	StorageRef     string `json:"storage_ref,omitempty"`
	StorageRefHash string `json:"storage_ref_hash,omitempty"`
	ChecksumSHA256 string `json:"checksum_sha256,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	BaseObjectSHA  string `json:"base_object_sha,omitempty"`
	Content        []byte `json:"content,omitempty"`
}

type GitOverlayEntry struct {
	WorkspaceID    string    `json:"workspace_id"`
	Path           string    `json:"path"`
	Op             string    `json:"op"`
	Kind           string    `json:"kind"`
	Mode           string    `json:"mode"`
	StorageType    string    `json:"storage_type"`
	StorageRef     string    `json:"storage_ref"`
	StorageRefHash string    `json:"storage_ref_hash"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	SizeBytes      int64     `json:"size_bytes"`
	BaseObjectSHA  string    `json:"base_object_sha"`
	Content        []byte    `json:"content,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type GitOverlayEntriesResponse struct {
	Entries []GitOverlayEntry `json:"entries"`
}

func (c *Client) UpsertGitWorkspace(ctx context.Context, request GitWorkspaceRequest) (GitWorkspace, error) {
	var response GitWorkspace
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/git-workspaces", request, &response); err != nil {
		return GitWorkspace{}, err
	}
	return response, nil
}

func (c *Client) ListGitWorkspaces(ctx context.Context) (GitWorkspacesResponse, error) {
	var response GitWorkspacesResponse
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces", nil, &response); err != nil {
		return GitWorkspacesResponse{}, err
	}
	if response.Workspaces == nil {
		response.Workspaces = []GitWorkspace{}
	}
	return response, nil
}

func (c *Client) GetGitWorkspace(ctx context.Context, workspaceID string) (GitWorkspace, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return GitWorkspace{}, apperr.New("git.missing_workspace_id", "usage", 2, "--workspace-id is required")
	}
	var response GitWorkspace
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID), nil, &response); err != nil {
		return GitWorkspace{}, err
	}
	return response, nil
}

func (c *Client) GetGitWorkspaceByRoot(ctx context.Context, rootPath string) (GitWorkspace, error) {
	if strings.TrimSpace(rootPath) == "" {
		return GitWorkspace{}, apperr.New("git.missing_root_path", "usage", 2, "--root-path is required")
	}
	values := url.Values{}
	values.Set("root_path", rootPath)
	var response GitWorkspace
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces?"+values.Encode(), nil, &response); err != nil {
		return GitWorkspace{}, err
	}
	return response, nil
}

func (c *Client) DeleteGitWorkspace(ctx context.Context, workspaceID string) error {
	if strings.TrimSpace(workspaceID) == "" {
		return apperr.New("git.missing_workspace_id", "usage", 2, "--workspace-id is required")
	}
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/git-workspaces/"+url.PathEscape(workspaceID), nil)
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, nil)
}

func (c *Client) ReplaceGitTree(ctx context.Context, workspaceID string, request GitTreeReplaceRequest) error {
	if strings.TrimSpace(workspaceID) == "" {
		return apperr.New("git.missing_workspace_id", "usage", 2, "--workspace-id is required")
	}
	return c.doVaultJSON(ctx, http.MethodPost, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/tree", request, nil)
}

func (c *Client) ListGitTree(ctx context.Context, workspaceID, commitSHA string) (GitTreeResponse, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return GitTreeResponse{}, apperr.New("git.missing_workspace_id", "usage", 2, "--workspace-id is required")
	}
	values := url.Values{}
	values.Set("commit_sha", commitSHA)
	var response GitTreeResponse
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/tree?"+values.Encode(), nil, &response); err != nil {
		return GitTreeResponse{}, err
	}
	if response.Nodes == nil {
		response.Nodes = []GitTreeNode{}
	}
	return response, nil
}

func (c *Client) UpsertGitState(ctx context.Context, workspaceID string, request GitStateRequest) (GitState, error) {
	var response GitState
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/git-state", request, &response); err != nil {
		return GitState{}, err
	}
	return response, nil
}

func (c *Client) GetGitState(ctx context.Context, workspaceID string) (GitState, error) {
	var response GitState
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/git-state", nil, &response); err != nil {
		return GitState{}, err
	}
	return response, nil
}

func (c *Client) PutGitObjectPack(ctx context.Context, workspaceID string, request GitObjectPackRequest) (GitObjectPack, error) {
	var response GitObjectPack
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/object-packs", request, &response); err != nil {
		return GitObjectPack{}, err
	}
	return response, nil
}

func (c *Client) ListGitObjectPacks(ctx context.Context, workspaceID string) (GitObjectPacksResponse, error) {
	var response GitObjectPacksResponse
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/object-packs", nil, &response); err != nil {
		return GitObjectPacksResponse{}, err
	}
	if response.Packs == nil {
		response.Packs = []GitObjectPack{}
	}
	return response, nil
}

func (c *Client) GetGitObjectPack(ctx context.Context, workspaceID, packID string) (GitObjectPack, error) {
	var response GitObjectPack
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/object-packs/"+url.PathEscape(packID), nil, &response); err != nil {
		return GitObjectPack{}, err
	}
	return response, nil
}

func (c *Client) PutGitOverlayEntry(ctx context.Context, workspaceID string, request GitOverlayEntryRequest) (GitOverlayEntry, error) {
	var response GitOverlayEntry
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/overlay", request, &response); err != nil {
		return GitOverlayEntry{}, err
	}
	return response, nil
}

func (c *Client) GetGitOverlayEntry(ctx context.Context, workspaceID, relPath string) (GitOverlayEntry, error) {
	values := url.Values{}
	values.Set("path", relPath)
	var response GitOverlayEntry
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/overlay?"+values.Encode(), nil, &response); err != nil {
		return GitOverlayEntry{}, err
	}
	return response, nil
}

func (c *Client) ListGitOverlayEntries(ctx context.Context, workspaceID string) (GitOverlayEntriesResponse, error) {
	var response GitOverlayEntriesResponse
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/git-workspaces/"+url.PathEscape(workspaceID)+"/overlay", nil, &response); err != nil {
		return GitOverlayEntriesResponse{}, err
	}
	if response.Entries == nil {
		response.Entries = []GitOverlayEntry{}
	}
	return response, nil
}
