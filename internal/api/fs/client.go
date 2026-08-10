package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type Client struct {
	api *api.Client
}

func New(client *api.Client) *Client {
	return &Client{api: client}
}

func (c *Client) CheckStatus(ctx context.Context, out any) error {
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/status", nil)
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, out)
}

type StatusResponse struct {
	Status          string         `json:"status,omitempty"`
	TenantID        string         `json:"tenant_id,omitempty"`
	Kind            string         `json:"kind,omitempty"`
	Version         string         `json:"version,omitempty"`
	InlineThreshold int64          `json:"inline_threshold,omitempty"`
	MaxUploadBytes  int64          `json:"max_upload_bytes,omitempty"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
}

type FileInfo struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
	Mtime int64  `json:"mtime,omitempty"`
}

type ListResponse struct {
	Entries []FileInfo `json:"entries"`
}

type StatResponse struct {
	Path       string `json:"path,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	IsDir      bool   `json:"is_dir"`
	Revision   int64  `json:"revision,omitempty"`
	Mtime      int64  `json:"mtime,omitempty"`
	Mode       int64  `json:"mode,omitempty"`
	HasMode    bool   `json:"has_mode,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Nlink      int64  `json:"nlink,omitempty"`
	Degraded   bool   `json:"degraded,omitempty"`
}

type StatMetadataResponse struct {
	Size         int64             `json:"size"`
	IsDir        bool              `json:"isdir"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Nlink        int64             `json:"nlink,omitempty"`
	Revision     int64             `json:"revision,omitempty"`
	Mtime        int64             `json:"mtime,omitempty"`
	ContentType  string            `json:"content_type,omitempty"`
	SemanticText string            `json:"semantic_text,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Degraded     bool              `json:"degraded,omitempty"`
}

type SearchResult struct {
	Path      string   `json:"path"`
	Name      string   `json:"name,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
	Score     *float64 `json:"score,omitempty"`
}

type WriteResponse struct {
	Revision int64 `json:"revision,omitempty"`
}

type WriteFileOptions struct {
	ExpectedRevision *int64
	Tags             map[string]string
	Description      string
}

type ProvisionRequest struct {
	PublicKey  string `json:"public_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

type ProvisionResponse struct {
	TenantID      string `json:"tenant_id"`
	APIKey        string `json:"api_key"`
	Status        string `json:"status"`
	CloudProvider string `json:"cloud_provider,omitempty"`
	RegionCode    string `json:"region_code,omitempty"`
	Region        string `json:"region,omitempty"`
}

type DeleteResponse struct {
	TenantID string `json:"tenant_id,omitempty"`
	Status   string `json:"status"`
}

type DeprovisionRequest struct {
	PublicKey  string `json:"public_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var response StatusResponse
	if err := c.CheckStatus(ctx, &response); err != nil {
		return StatusResponse{}, err
	}
	return response, nil
}

func (c *Client) WriteFile(ctx context.Context, remotePath string, data []byte) (WriteResponse, error) {
	return c.WriteFileWithOptions(ctx, remotePath, data, WriteFileOptions{})
}

func (c *Client) WriteFileWithOptions(ctx context.Context, remotePath string, data []byte, opts WriteFileOptions) (WriteResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPut, fsPath(remotePath), nil)
	if err != nil {
		return WriteResponse{}, err
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	if opts.ExpectedRevision != nil {
		req.Header.Set("X-Dat9-Expected-Revision", strconv.FormatInt(*opts.ExpectedRevision, 10))
	}
	if err := setTagHeaders(req, opts.Tags); err != nil {
		return WriteResponse{}, err
	}
	if strings.TrimSpace(opts.Description) != "" {
		req.Header.Set("X-Dat9-Description", opts.Description)
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return WriteResponse{}, err
	}
	defer res.Body.Close()
	if !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "application/json") {
		_, _ = io.Copy(io.Discard, res.Body)
		return WriteResponse{}, nil
	}
	var response WriteResponse
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return WriteResponse{}, nil
	}
	return response, nil
}

func (c *Client) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	return c.readFile(ctx, remotePath, 0, 0, false)
}

func (c *Client) ReadFileRange(ctx context.Context, remotePath string, offset, length int64) ([]byte, error) {
	if offset < 0 {
		return nil, apperr.New("fs.invalid_range", "usage", 2, "--offset must be non-negative")
	}
	if length < 0 {
		return nil, apperr.New("fs.invalid_range", "usage", 2, "--length must be non-negative")
	}
	if length == 0 {
		return []byte{}, nil
	}
	if offset > (int64(^uint64(0)>>1) - length + 1) {
		return nil, apperr.New("fs.invalid_range", "usage", 2, "--offset plus --length overflows")
	}
	return c.readFile(ctx, remotePath, offset, length, true)
}

func (c *Client) readFile(ctx context.Context, remotePath string, offset, length int64, ranged bool) ([]byte, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, fsPath(remotePath), nil)
	if err != nil {
		return nil, err
	}
	rangeHeader := ""
	if ranged {
		rangeHeader = "bytes=" + strconv.FormatInt(offset, 10) + "-" + strconv.FormatInt(offset+length-1, 10)
		req.Header.Set("Range", rangeHeader)
	}
	res, err := c.api.DoRawNoRedirect(req)
	if err != nil {
		var apiErr *api.Error
		if ranged && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return []byte{}, nil
		}
		return nil, err
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return c.readRedirectedFile(ctx, res, rangeHeader, offset, length, ranged)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, apperr.Wrap("api.read_response", "runtime", 1, "read ti fs response body", err)
	}
	if ranged && res.StatusCode == http.StatusOK {
		if offset >= int64(len(data)) {
			return []byte{}, nil
		}
		end := offset + length
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return append([]byte(nil), data[offset:end]...), nil
	}
	return data, nil
}

func (c *Client) readRedirectedFile(ctx context.Context, res *http.Response, rangeHeader string, offset, length int64, ranged bool) ([]byte, error) {
	defer res.Body.Close()
	location, err := res.Location()
	if err != nil {
		return nil, apperr.Wrap("api.invalid_redirect", "api", 1, "ti fs returned an invalid download redirect", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, apperr.Wrap("api.build_request", "runtime", 1, "build redirected ti fs download request", err)
	}
	req.Header.Set("User-Agent", c.api.UserAgent)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	redirected, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &api.Error{
			Code:     "api.network_error",
			Category: "api",
			ExitCode: 1,
			Message:  "API request failed: check network connectivity and try again",
			Cause:    err,
		}
	}
	defer redirected.Body.Close()
	if ranged && redirected.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return []byte{}, nil
	}
	if redirected.StatusCode < 200 || redirected.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(redirected.Body, 8*1024))
		message := "redirected ti fs download failed with HTTP " + strconv.Itoa(redirected.StatusCode)
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			message += ": " + trimmed
		}
		return nil, apperr.New("api.remote_error", "api", 1, message)
	}
	data, err := io.ReadAll(redirected.Body)
	if err != nil {
		return nil, apperr.Wrap("api.read_response", "runtime", 1, "read redirected ti fs response body", err)
	}
	if ranged && redirected.StatusCode == http.StatusOK {
		if offset >= int64(len(data)) {
			return []byte{}, nil
		}
		end := offset + length
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		return append([]byte(nil), data[offset:end]...), nil
	}
	return data, nil
}

func (c *Client) List(ctx context.Context, remotePath string) (ListResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, fsPathWithRawQuery(remotePath, "list=1"), nil)
	if err != nil {
		return ListResponse{}, err
	}
	var response ListResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListResponse{}, err
	}
	if response.Entries == nil {
		response.Entries = []FileInfo{}
	}
	return response, nil
}

func (c *Client) Stat(ctx context.Context, remotePath string) (StatResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodHead, fsPath(remotePath), nil)
	if err != nil {
		return StatResponse{}, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return StatResponse{}, err
	}
	defer res.Body.Close()
	return statFromHeaders(remotePath, res.Header), nil
}

func (c *Client) StatMetadata(ctx context.Context, remotePath string) (StatMetadataResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, fsPathWithRawQuery(remotePath, "stat=1"), nil)
	if err != nil {
		return StatMetadataResponse{}, err
	}
	var response StatMetadataResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return StatMetadataResponse{}, err
	}
	if response.Tags == nil {
		response.Tags = map[string]string{}
	}
	return response, nil
}

func (c *Client) DeleteFile(ctx context.Context, remotePath string, recursive bool) error {
	requestPath := fsPath(remotePath)
	if recursive {
		requestPath = fsPathWithRawQuery(remotePath, "recursive=1")
	}
	req, err := c.api.NewRequest(ctx, http.MethodDelete, requestPath, nil)
	if err != nil {
		return err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) CopyRemote(ctx context.Context, sourcePath, targetPath string) error {
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(targetPath, "copy"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Dat9-Copy-Source", sourcePath)
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Rename(ctx context.Context, sourcePath, targetPath string) error {
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(targetPath, "rename"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Dat9-Rename-Source", sourcePath)
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Mkdir(ctx context.Context, remotePath string, mode int64) error {
	query := "mkdir"
	if mode > 0 && mode != 0o755 {
		query = "mkdir&mode=" + url.QueryEscape(strconv.FormatInt(mode, 8))
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(remotePath, query), nil)
	if err != nil {
		return err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Chmod(ctx context.Context, remotePath string, mode int64) error {
	body := struct {
		Mode int64 `json:"mode"`
	}{Mode: mode}
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(remotePath, "chmod"), body)
	if err != nil {
		return err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Symlink(ctx context.Context, target, linkPath string) error {
	body := struct {
		Target string `json:"target"`
	}{Target: target}
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(linkPath, "symlink=1"), body)
	if err != nil {
		return err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Hardlink(ctx context.Context, sourcePath, linkPath string) error {
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(linkPath, "hardlink=1"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Dat9-Hardlink-Source", sourcePath)
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) Grep(ctx context.Context, remotePath, pattern string, limit int32) ([]SearchResult, error) {
	values := url.Values{}
	values.Set("grep", pattern)
	if limit > 0 {
		values.Set("limit", strconv.FormatInt(int64(limit), 10))
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

func (c *Client) Find(ctx context.Context, remotePath string, params url.Values) ([]SearchResult, error) {
	values := url.Values{}
	for key, list := range params {
		for _, value := range list {
			values.Add(key, value)
		}
	}
	values.Set("find", "")
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

func (c *Client) Provision(ctx context.Context, request ProvisionRequest) (ProvisionResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/provision", provisionBody(request))
	if err != nil {
		return ProvisionResponse{}, err
	}
	var response ProvisionResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ProvisionResponse{}, err
	}
	return response, nil
}

func (c *Client) DeleteTenant(ctx context.Context, request DeprovisionRequest) (DeleteResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/tenant", deprovisionBody(request))
	if err != nil {
		return DeleteResponse{}, err
	}
	var response DeleteResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return DeleteResponse{}, err
	}
	return response, nil
}

func provisionBody(request ProvisionRequest) any {
	if request.PublicKey == "" && request.PrivateKey == "" {
		return nil
	}
	return request
}

func deprovisionBody(request DeprovisionRequest) any {
	if request.PublicKey == "" && request.PrivateKey == "" {
		return nil
	}
	return request
}

func fsPath(remotePath string) string {
	return "/v1/fs" + encodeRemotePath(remotePath)
}

func fsPathWithRawQuery(remotePath, rawQuery string) string {
	requestPath := fsPath(remotePath)
	if rawQuery == "" {
		return requestPath
	}
	return requestPath + "?" + rawQuery
}

func encodeRemotePath(remotePath string) string {
	if remotePath == "" {
		return "/"
	}
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	parts := strings.Split(remotePath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	encoded := strings.Join(parts, "/")
	if encoded == "" {
		return "/"
	}
	return encoded
}

func statFromHeaders(remotePath string, headers http.Header) StatResponse {
	mode, hasMode := int64Header(headers, "X-Dat9-Mode")
	mtime, _ := int64Header(headers, "X-Dat9-Mtime")
	revision, _ := int64Header(headers, "X-Dat9-Revision")
	nlink, _ := int64Header(headers, "X-Dat9-Nlink")
	size, _ := int64Header(headers, "Content-Length")
	return StatResponse{
		Path:       remotePath,
		SizeBytes:  size,
		IsDir:      strings.EqualFold(headers.Get("X-Dat9-IsDir"), "true"),
		Revision:   revision,
		Mtime:      mtimeUnix(mtime),
		Mode:       mode,
		HasMode:    hasMode,
		ResourceID: headers.Get("X-Dat9-Resource-Id"),
		Nlink:      nlink,
	}
}

func setTagHeaders(req *http.Request, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if err := validateTags(tags); err != nil {
		return err
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		req.Header.Add("X-Dat9-Tag", key+"="+tags[key])
	}
	return nil
}

func int64Header(headers http.Header, name string) (int64, bool) {
	value := strings.TrimSpace(headers.Get(name))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func mtimeUnix(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value > time.Now().AddDate(100, 0, 0).Unix() {
		return 0
	}
	return value
}
