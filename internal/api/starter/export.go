package starter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultExportFilePageSize int32 = 1000

type Export struct {
	ID           string
	Name         string
	ClusterID    string
	DisplayName  string
	State        string
	TargetType   string
	FileType     string
	CreatedBy    string
	Reason       string
	CreateTime   string
	UpdateTime   string
	CompleteTime string
	SnapshotTime string
	ExpireTime   string
}

type ListExportsOptions struct {
	PageSize  int32
	PageToken string
	OrderBy   string
}

type ListExportsResponse struct {
	Exports       []Export
	NextPageToken string
	TotalSize     int64
}

func (c *Client) ListExports(ctx context.Context, clusterID string, opts ListExportsOptions) (ListExportsResponse, error) {
	requestPath := "/v1beta1/clusters/" + url.PathEscape(clusterID) + "/exports"
	query := url.Values{}
	if opts.PageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(int64(opts.PageSize), 10))
	}
	if opts.PageToken != "" {
		query.Set("pageToken", opts.PageToken)
	}
	if opts.OrderBy != "" {
		query.Set("orderBy", opts.OrderBy)
	}
	if encoded := query.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return ListExportsResponse{}, err
	}
	var response listExportsWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListExportsResponse{}, err
	}
	return response.toResponse(), nil
}

type ExportFile struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	URL  string `json:"url,omitempty"`
}

type ListExportFilesOptions struct {
	PageSize  int32
	PageToken string
}

type ListExportFilesResponse struct {
	Files         []ExportFile `json:"files"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

type DownloadExportFilesRequest struct {
	FileNames []string
}

type DownloadExportFilesResponse struct {
	Files []ExportFile `json:"files"`
}

func (c *Client) ListExportFiles(ctx context.Context, clusterID, exportID string, opts ListExportFilesOptions) (ListExportFilesResponse, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultExportFilePageSize
	}
	requestPath := "/v1beta1/clusters/" + url.PathEscape(clusterID) + "/exports/" + url.PathEscape(exportID) + "/files"
	query := url.Values{}
	query.Set("pageSize", strconv.FormatInt(int64(pageSize), 10))
	if opts.PageToken != "" {
		query.Set("pageToken", opts.PageToken)
	}
	requestPath += "?" + query.Encode()
	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return ListExportFilesResponse{}, err
	}
	var response listExportFilesWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListExportFilesResponse{}, err
	}
	return response.toResponse(), nil
}

func (c *Client) DownloadExportFiles(ctx context.Context, clusterID, exportID string, input DownloadExportFilesRequest) (DownloadExportFilesResponse, error) {
	body := downloadExportFilesWire{FileNames: input.FileNames}
	requestPath := "/v1beta1/clusters/" + url.PathEscape(clusterID) + "/exports/" + url.PathEscape(exportID) + "/files:download"
	req, err := c.api.NewRequest(ctx, http.MethodPost, requestPath, body)
	if err != nil {
		return DownloadExportFilesResponse{}, err
	}
	var response downloadExportFilesWireResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return DownloadExportFilesResponse{}, err
	}
	return DownloadExportFilesResponse{Files: response.toFiles()}, nil
}

type listExportFilesWire struct {
	Files            []exportFileWire `json:"files"`
	NextPageToken    string           `json:"nextPageToken"`
	NextPageTokenAlt string           `json:"next_page_token"`
}

func (r listExportFilesWire) toResponse() ListExportFilesResponse {
	next := r.NextPageToken
	if next == "" {
		next = r.NextPageTokenAlt
	}
	return ListExportFilesResponse{Files: exportFilesFromWire(r.Files), NextPageToken: next}
}

type downloadExportFilesWire struct {
	FileNames []string `json:"fileNames"`
}

type downloadExportFilesWireResponse struct {
	Files []exportFileWire `json:"files"`
}

func (r downloadExportFilesWireResponse) toFiles() []ExportFile {
	return exportFilesFromWire(r.Files)
}

type exportFileWire struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func exportFilesFromWire(files []exportFileWire) []ExportFile {
	out := make([]ExportFile, 0, len(files))
	for _, file := range files {
		out = append(out, ExportFile{Name: file.Name, Size: file.Size, URL: file.URL})
	}
	return out
}

type listExportsWire struct {
	Exports          []exportWire `json:"exports"`
	NextPageToken    string       `json:"nextPageToken"`
	NextPageTokenAlt string       `json:"next_page_token"`
	TotalSize        int64        `json:"totalSize"`
	TotalSizeAlt     int64        `json:"total_size"`
}

func (r listExportsWire) toResponse() ListExportsResponse {
	exports := make([]Export, 0, len(r.Exports))
	for _, item := range r.Exports {
		exports = append(exports, item.toExport())
	}
	next := r.NextPageToken
	if next == "" {
		next = r.NextPageTokenAlt
	}
	total := r.TotalSize
	if total == 0 {
		total = r.TotalSizeAlt
	}
	return ListExportsResponse{Exports: exports, NextPageToken: next, TotalSize: total}
}

type exportWire struct {
	Name          string            `json:"name"`
	ExportID      string            `json:"exportId"`
	ClusterID     string            `json:"clusterId"`
	DisplayName   string            `json:"displayName"`
	State         string            `json:"state"`
	CreatedBy     string            `json:"createdBy"`
	Reason        string            `json:"reason"`
	CreateTime    json.RawMessage   `json:"createTime"`
	UpdateTime    json.RawMessage   `json:"updateTime"`
	CompleteTime  json.RawMessage   `json:"completeTime"`
	SnapshotTime  json.RawMessage   `json:"snapshotTime"`
	ExpireTime    json.RawMessage   `json:"expireTime"`
	Target        exportTargetWire  `json:"target"`
	ExportOptions exportOptionsWire `json:"exportOptions"`
}

type exportTargetWire struct {
	Type string `json:"type"`
}

type exportOptionsWire struct {
	FileType string `json:"fileType"`
}

func (e exportWire) toExport() Export {
	id := e.ExportID
	if id == "" {
		id = exportIDFromName(e.Name)
	}
	clusterID := e.ClusterID
	if clusterID == "" {
		clusterID = clusterIDFromExportName(e.Name)
	}
	return Export{
		ID:           id,
		Name:         e.Name,
		ClusterID:    clusterID,
		DisplayName:  e.DisplayName,
		State:        e.State,
		TargetType:   e.Target.Type,
		FileType:     e.ExportOptions.FileType,
		CreatedBy:    e.CreatedBy,
		Reason:       e.Reason,
		CreateTime:   wireString(e.CreateTime),
		UpdateTime:   wireString(e.UpdateTime),
		CompleteTime: wireString(e.CompleteTime),
		SnapshotTime: wireString(e.SnapshotTime),
		ExpireTime:   wireString(e.ExpireTime),
	}
}

func exportIDFromName(name string) string {
	if idx := strings.LastIndex(name, "/exports/"); idx >= 0 {
		return name[idx+len("/exports/"):]
	}
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

func clusterIDFromExportName(name string) string {
	const prefix = "clusters/"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := name[len(prefix):]
	if idx := strings.Index(rest, "/"); idx >= 0 {
		return rest[:idx]
	}
	return rest
}

func wireString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}
