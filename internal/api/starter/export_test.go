package starter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListExports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1/exports" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("pageSize") != "2" || r.URL.Query().Get("pageToken") != "token-1" || r.URL.Query().Get("orderBy") != "create_time desc" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"exports":[{
				"name":"clusters/cluster-1/exports/export-1",
				"displayName":"nightly",
				"state":"SUCCEEDED",
				"createdBy":"user-1",
				"createTime":"2026-01-02T03:04:05Z",
				"completeTime":null,
				"target":{"type":"LOCAL","accessKeyId":"secret"},
				"exportOptions":{"fileType":"CSV"}
			}],
			"nextPageToken":"token-2",
			"totalSize":3
		}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListExports(context.Background(), "cluster-1", ListExportsOptions{
		PageSize:  2,
		PageToken: "token-1",
		OrderBy:   "create_time desc",
	})
	if err != nil {
		t.Fatalf("ListExports: %v", err)
	}
	if response.NextPageToken != "token-2" || response.TotalSize != 3 || len(response.Exports) != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	got := response.Exports[0]
	if got.ID != "export-1" || got.ClusterID != "cluster-1" || got.DisplayName != "nightly" || got.State != "SUCCEEDED" || got.TargetType != "LOCAL" || got.FileType != "CSV" || got.CreateTime != "2026-01-02T03:04:05Z" || got.CompleteTime != "" {
		t.Fatalf("unexpected export: %#v", got)
	}
}

func TestListExportFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1/exports/export-1/files" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("pageSize"); got != "1000" {
			t.Fatalf("pageSize = %q", got)
		}
		if got := r.URL.Query().Get("pageToken"); got != "token-1" {
			t.Fatalf("pageToken = %q", got)
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"DUMP/a.csv","size":12}],"nextPageToken":"token-2"}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.ListExportFiles(context.Background(), "cluster-1", "export-1", ListExportFilesOptions{PageToken: "token-1"})
	if err != nil {
		t.Fatalf("ListExportFiles: %v", err)
	}
	if response.NextPageToken != "token-2" || len(response.Files) != 1 || response.Files[0].Name != "DUMP/a.csv" || response.Files[0].Size != 12 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestDownloadExportFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta1/clusters/cluster-1/exports/export-1/files:download" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		names, _ := payload["fileNames"].([]any)
		if len(names) != 1 || names[0] != "DUMP/a.csv" {
			t.Fatalf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"DUMP/a.csv","size":12,"url":"https://example.test/a.csv"}]}`))
	}))
	defer server.Close()

	client := New(newTestAPIClient(t, server.URL))
	response, err := client.DownloadExportFiles(context.Background(), "cluster-1", "export-1", DownloadExportFilesRequest{FileNames: []string{"DUMP/a.csv"}})
	if err != nil {
		t.Fatalf("DownloadExportFiles: %v", err)
	}
	if len(response.Files) != 1 || !strings.HasSuffix(response.Files[0].URL, "/a.csv") {
		t.Fatalf("unexpected response: %#v", response)
	}
}
