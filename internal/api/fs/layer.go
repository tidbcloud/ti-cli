package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

var ErrLayerCommitConflict = errors.New("fs layer commit conflict")

type LayerCommitConflictError struct {
	Commit FSLayerCommit
}

func (e *LayerCommitConflictError) Error() string {
	if e == nil {
		return ""
	}
	return "fs layer commit conflict"
}

func (e *LayerCommitConflictError) Is(target error) bool {
	return target == ErrLayerCommitConflict
}

type FSLayerCreateRequest struct {
	LayerID        string            `json:"layer_id,omitempty"`
	BaseRootPath   string            `json:"base_root_path"`
	Name           string            `json:"name,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	DurabilityMode string            `json:"durability_mode,omitempty"`
	ActorID        string            `json:"actor_id,omitempty"`
}

type FSLayer struct {
	LayerID        string            `json:"layer_id"`
	BaseRootPath   string            `json:"base_root_path"`
	Name           string            `json:"name"`
	Tags           map[string]string `json:"tags,omitempty"`
	State          string            `json:"state"`
	DurabilityMode string            `json:"durability_mode"`
	ActorID        string            `json:"actor_id"`
	DurableSeq     int64             `json:"durable_seq"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	SealedAt       *time.Time        `json:"sealed_at,omitempty"`
}

type FSLayerEntry struct {
	LayerID                string    `json:"layer_id"`
	Path                   string    `json:"path"`
	ParentPath             string    `json:"parent_path"`
	Name                   string    `json:"name"`
	Op                     string    `json:"op"`
	Kind                   string    `json:"kind"`
	BaseInodeID            string    `json:"base_inode_id"`
	BaseRevision           int64     `json:"base_revision"`
	StorageType            string    `json:"storage_type"`
	StorageRef             string    `json:"storage_ref"`
	StorageRefHash         string    `json:"storage_ref_hash"`
	StorageEncryptionMode  string    `json:"storage_encryption_mode"`
	StorageEncryptionKeyID string    `json:"storage_encryption_key_id"`
	ChecksumSHA256         string    `json:"checksum_sha256"`
	SizeBytes              int64     `json:"size_bytes"`
	Mode                   uint32    `json:"mode"`
	Content                []byte    `json:"content,omitempty"`
	ContentText            string    `json:"content_text,omitempty"`
	EntrySeq               int64     `json:"entry_seq"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type FSLayerEntryRequest struct {
	Path                   string `json:"path"`
	Op                     string `json:"op,omitempty"`
	Kind                   string `json:"kind,omitempty"`
	BaseInodeID            string `json:"base_inode_id,omitempty"`
	BaseRevision           int64  `json:"base_revision,omitempty"`
	StorageType            string `json:"storage_type,omitempty"`
	StorageRef             string `json:"storage_ref,omitempty"`
	StorageRefHash         string `json:"storage_ref_hash,omitempty"`
	StorageEncryptionMode  string `json:"storage_encryption_mode,omitempty"`
	StorageEncryptionKeyID string `json:"storage_encryption_key_id,omitempty"`
	Content                []byte `json:"content,omitempty"`
	ContentType            string `json:"content_type,omitempty"`
	ContentText            string `json:"content_text,omitempty"`
	ChecksumSHA256         string `json:"checksum_sha256,omitempty"`
	SizeBytes              int64  `json:"size_bytes,omitempty"`
	Mode                   uint32 `json:"mode,omitempty"`
}

type FSLayerCommit struct {
	Status    string                  `json:"status"`
	LayerID   string                  `json:"layer_id"`
	Applied   int                     `json:"applied,omitempty"`
	Conflicts []FSLayerCommitConflict `json:"conflicts,omitempty"`
}

type FSLayerCommitConflict struct {
	Path         string `json:"path"`
	Reason       string `json:"reason"`
	BaseRevision int64  `json:"base_revision,omitempty"`
	WantRevision int64  `json:"want_revision,omitempty"`
}

type FSLayerCheckpointRequest struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	Label        string `json:"label,omitempty"`
}

type FSLayerCheckpoint struct {
	CheckpointID string    `json:"checkpoint_id"`
	LayerID      string    `json:"layer_id"`
	DurableSeq   int64     `json:"durable_seq"`
	Label        string    `json:"label"`
	CreatedAt    time.Time `json:"created_at"`
}

type FSLayerEvent struct {
	EventID   string    `json:"event_id"`
	LayerID   string    `json:"layer_id"`
	Seq       int64     `json:"seq"`
	ActorID   string    `json:"actor_id"`
	Op        string    `json:"op"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
}

func (c *Client) CreateFSLayer(ctx context.Context, request FSLayerCreateRequest) (FSLayer, error) {
	var response FSLayer
	if err := c.doLayerJSON(ctx, http.MethodPost, "/v1/layers", request, &response); err != nil {
		return FSLayer{}, err
	}
	return response, nil
}

func (c *Client) ListFSLayers(ctx context.Context) ([]FSLayer, error) {
	var response struct {
		Layers []FSLayer `json:"layers"`
	}
	if err := c.doLayerJSON(ctx, http.MethodGet, "/v1/layers", nil, &response); err != nil {
		return nil, err
	}
	if response.Layers == nil {
		response.Layers = []FSLayer{}
	}
	return response.Layers, nil
}

func (c *Client) GetFSLayer(ctx context.Context, layerID string) (FSLayer, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayer{}, err
	}
	var response FSLayer
	if err := c.doLayerJSON(ctx, http.MethodGet, "/v1/layers/"+url.PathEscape(layerID), nil, &response); err != nil {
		return FSLayer{}, err
	}
	return response, nil
}

func (c *Client) DiffFSLayer(ctx context.Context, layerID string) ([]FSLayerEntry, error) {
	return c.fetchFSLayerDiff(ctx, layerID, nil, false)
}

func (c *Client) DiffFSLayerAtSeq(ctx context.Context, layerID string, maxSeq int64) ([]FSLayerEntry, error) {
	if maxSeq < 0 {
		return nil, apperr.New("fs.invalid_layer_sequence", "usage", 2, "--max-seq must be non-negative")
	}
	return c.fetchFSLayerDiff(ctx, layerID, &maxSeq, false)
}

func (c *Client) ReplayFSLayer(ctx context.Context, layerID string) ([]FSLayerEntry, error) {
	return c.fetchFSLayerDiff(ctx, layerID, nil, true)
}

func (c *Client) ReplayFSLayerAtSeq(ctx context.Context, layerID string, maxSeq int64) ([]FSLayerEntry, error) {
	if maxSeq < 0 {
		return nil, apperr.New("fs.invalid_layer_sequence", "usage", 2, "--max-seq must be non-negative")
	}
	return c.fetchFSLayerDiff(ctx, layerID, &maxSeq, true)
}

func (c *Client) fetchFSLayerDiff(ctx context.Context, layerID string, maxSeq *int64, replay bool) ([]FSLayerEntry, error) {
	if err := requireLayerID(layerID); err != nil {
		return nil, err
	}
	values := url.Values{}
	if maxSeq != nil {
		values.Set("max_seq", strconv.FormatInt(*maxSeq, 10))
	}
	if replay {
		values.Set("replay", "1")
	}
	requestPath := "/v1/layers/" + url.PathEscape(layerID) + "/diff"
	if encoded := values.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	var response struct {
		Entries []FSLayerEntry `json:"entries"`
	}
	if err := c.doLayerJSON(ctx, http.MethodGet, requestPath, nil, &response); err != nil {
		return nil, err
	}
	if response.Entries == nil {
		response.Entries = []FSLayerEntry{}
	}
	return response.Entries, nil
}

func (c *Client) UpsertFSLayerEntry(ctx context.Context, layerID string, request FSLayerEntryRequest) (FSLayerEntry, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayerEntry{}, err
	}
	var response FSLayerEntry
	if err := c.doLayerJSON(ctx, http.MethodPost, "/v1/layers/"+url.PathEscape(layerID)+"/entries", request, &response); err != nil {
		return FSLayerEntry{}, err
	}
	return response, nil
}

func (c *Client) UploadFSLayerFile(ctx context.Context, layerID, remotePath string, body io.Reader, size int64, baseRevision int64, mode uint32, hasMode bool) (FSLayerEntry, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayerEntry{}, err
	}
	if strings.TrimSpace(remotePath) == "" {
		return FSLayerEntry{}, apperr.New("fs.missing_layer_path", "usage", 2, "layer file path is required")
	}
	if size < 0 {
		return FSLayerEntry{}, apperr.New("fs.invalid_layer_file_size", "usage", 2, "layer file size must be non-negative")
	}
	values := url.Values{}
	values.Set("path", remotePath)
	values.Set("size", strconv.FormatInt(size, 10))
	if baseRevision > 0 {
		values.Set("base_revision", strconv.FormatInt(baseRevision, 10))
	}
	if hasMode {
		values.Set("mode", fmt.Sprintf("%o", mode&0o777))
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/layers/"+url.PathEscape(layerID)+"/objects?"+values.Encode(), nil)
	if err != nil {
		return FSLayerEntry{}, err
	}
	req.Body = io.NopCloser(body)
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := c.api.DoRaw(req)
	if err != nil {
		return FSLayerEntry{}, err
	}
	defer res.Body.Close()
	var response FSLayerEntry
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return FSLayerEntry{}, apperr.Wrap("api.decode_response", "api", 1, "decode fs layer object entry", err)
	}
	return response, nil
}

func (c *Client) ReadFSLayerFile(ctx context.Context, layerID, remotePath string, maxSeq *int64) ([]byte, error) {
	if err := requireLayerID(layerID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(remotePath) == "" {
		return nil, apperr.New("fs.missing_layer_path", "usage", 2, "layer file path is required")
	}
	values := url.Values{}
	values.Set("path", remotePath)
	if maxSeq != nil {
		if *maxSeq < 0 {
			return nil, apperr.New("fs.invalid_layer_sequence", "usage", 2, "--max-seq must be non-negative")
		}
		values.Set("max_seq", strconv.FormatInt(*maxSeq, 10))
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/layers/"+url.PathEscape(layerID)+"/objects?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, apperr.Wrap("api.read_response", "runtime", 1, "read fs layer file response body", err)
	}
	return data, nil
}

func (c *Client) GetFSLayerEntry(ctx context.Context, layerID, remotePath string) (FSLayerEntry, error) {
	return c.getFSLayerEntry(ctx, layerID, remotePath, nil)
}

func (c *Client) GetFSLayerEntryAtSeq(ctx context.Context, layerID, remotePath string, maxSeq int64) (FSLayerEntry, error) {
	if maxSeq < 0 {
		return FSLayerEntry{}, apperr.New("fs.invalid_layer_sequence", "usage", 2, "--max-seq must be non-negative")
	}
	return c.getFSLayerEntry(ctx, layerID, remotePath, &maxSeq)
}

func (c *Client) getFSLayerEntry(ctx context.Context, layerID, remotePath string, maxSeq *int64) (FSLayerEntry, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayerEntry{}, err
	}
	if strings.TrimSpace(remotePath) == "" {
		return FSLayerEntry{}, apperr.New("fs.missing_layer_path", "usage", 2, "layer entry path is required")
	}
	values := url.Values{}
	values.Set("path", remotePath)
	if maxSeq != nil {
		values.Set("max_seq", strconv.FormatInt(*maxSeq, 10))
	}
	var response FSLayerEntry
	if err := c.doLayerJSON(ctx, http.MethodGet, "/v1/layers/"+url.PathEscape(layerID)+"/entries?"+values.Encode(), nil, &response); err != nil {
		return FSLayerEntry{}, err
	}
	return response, nil
}

func (c *Client) CheckpointFSLayer(ctx context.Context, layerID string, request FSLayerCheckpointRequest) (FSLayerCheckpoint, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayerCheckpoint{}, err
	}
	var response FSLayerCheckpoint
	if err := c.doLayerJSON(ctx, http.MethodPost, "/v1/layers/"+url.PathEscape(layerID)+"/checkpoints", request, &response); err != nil {
		return FSLayerCheckpoint{}, err
	}
	return response, nil
}

func (c *Client) GetFSLayerCheckpoint(ctx context.Context, checkpointID string) (FSLayerCheckpoint, error) {
	if strings.TrimSpace(checkpointID) == "" {
		return FSLayerCheckpoint{}, apperr.New("fs.missing_layer_checkpoint_id", "usage", 2, "--checkpoint-id is required")
	}
	var response FSLayerCheckpoint
	if err := c.doLayerJSON(ctx, http.MethodGet, "/v1/layer-checkpoints/"+url.PathEscape(checkpointID), nil, &response); err != nil {
		return FSLayerCheckpoint{}, err
	}
	return response, nil
}

func (c *Client) ListFSLayerEvents(ctx context.Context, layerID string, since int64) ([]FSLayerEvent, error) {
	if err := requireLayerID(layerID); err != nil {
		return nil, err
	}
	if since < 0 {
		return nil, apperr.New("fs.invalid_layer_sequence", "usage", 2, "--since must be non-negative")
	}
	requestPath := "/v1/layers/" + url.PathEscape(layerID) + "/events"
	if since > 0 {
		values := url.Values{}
		values.Set("since", strconv.FormatInt(since, 10))
		requestPath += "?" + values.Encode()
	}
	var response struct {
		Events []FSLayerEvent `json:"events"`
	}
	if err := c.doLayerJSON(ctx, http.MethodGet, requestPath, nil, &response); err != nil {
		return nil, err
	}
	if response.Events == nil {
		response.Events = []FSLayerEvent{}
	}
	return response.Events, nil
}

func (c *Client) RollbackFSLayer(ctx context.Context, layerID string) error {
	return c.postFSLayerAction(ctx, layerID, "rollback")
}

func (c *Client) CommitFSLayer(ctx context.Context, layerID string) (FSLayerCommit, error) {
	if err := requireLayerID(layerID); err != nil {
		return FSLayerCommit{}, err
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/layers/"+url.PathEscape(layerID)+"/commit", map[string]any{})
	if err != nil {
		return FSLayerCommit{}, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			var response FSLayerCommit
			if decodeErr := json.Unmarshal([]byte(apiErr.Body), &response); decodeErr == nil && (response.Status != "" || len(response.Conflicts) > 0) {
				return response, &LayerCommitConflictError{Commit: response}
			}
		}
		return FSLayerCommit{}, err
	}
	defer res.Body.Close()
	var response FSLayerCommit
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return FSLayerCommit{}, apperr.Wrap("api.decode_response", "api", 1, "decode fs layer commit", err)
	}
	return response, nil
}

func (c *Client) postFSLayerAction(ctx context.Context, layerID, action string) error {
	if err := requireLayerID(layerID); err != nil {
		return err
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/layers/"+url.PathEscape(layerID)+"/"+url.PathEscape(action), map[string]any{})
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, nil)
}

func (c *Client) GrepWithLayer(ctx context.Context, remotePath, pattern string, limit int32, layerID string) ([]SearchResult, error) {
	values := url.Values{}
	values.Set("grep", pattern)
	if limit > 0 {
		values.Set("limit", strconv.FormatInt(int64(limit), 10))
	}
	if strings.TrimSpace(layerID) != "" {
		values.Set("layer", strings.TrimSpace(layerID))
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, fsPathWithRawQuery(remotePath, values.Encode()), nil)
	if err != nil {
		return nil, err
	}
	var response []SearchResult
	if err := c.api.DoJSON(req, &response); err != nil {
		return nil, err
	}
	if response == nil {
		response = []SearchResult{}
	}
	return response, nil
}

func (c *Client) doLayerJSON(ctx context.Context, method, requestPath string, body any, out any) error {
	req, err := c.api.NewRequest(ctx, method, requestPath, body)
	if err != nil {
		return err
	}
	if err := c.api.DoJSON(req, out); err != nil {
		return err
	}
	return nil
}

func requireLayerID(layerID string) error {
	if strings.TrimSpace(layerID) == "" {
		return apperr.New("fs.missing_layer_id", "usage", 2, "--layer-id is required")
	}
	return nil
}
