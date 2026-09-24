package starter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rootdb "github.com/tidbcloud/ti-cli/internal/db"
)

func TestListExportTasks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta1/clusters/cluster-1" {
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","servicePlan":"Starter"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta1/clusters/cluster-1/exports" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("pageSize") != "1" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"exports":[{"exportId":"export-1","clusterId":"cluster-1","displayName":"nightly","state":"SUCCEEDED","target":{"type":"LOCAL"},"exportOptions":{"fileType":"SQL"},"createTime":"2026-01-02T03:04:05Z"}],
			"nextPageToken":"token-2",
			"totalSize":1
		}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).ListExportTasks(context.Background(), ListExportTasksOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		PageSize:  1,
	})
	if err != nil {
		t.Fatalf("ListExportTasks: %v", err)
	}
	if len(result.ExportTasks) != 1 || result.ExportTasks[0].ID != "export-1" || result.ExportTasks[0].TargetType != "LOCAL" || result.NextPageToken != "token-2" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if human := result.Human(); !strings.Contains(human, "nightly") || !strings.Contains(human, "token-2") {
		t.Fatalf("unexpected text output:\n%s", human)
	}
}

func TestDownloadExportedDataWritesRelativeFiles(t *testing.T) {
	var downloaded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter","state":"ACTIVE"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1/exports/export-1/files":
			_, _ = w.Write([]byte(`{"files":[{"name":"metadata","size":8},{"name":"DUMP/a.csv","size":5}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/clusters/cluster-1/exports/export-1/files:download":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			names, _ := body["fileNames"].([]any)
			if len(names) != 1 || names[0] != "DUMP/a.csv" {
				t.Fatalf("unexpected download body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"DUMP/a.csv","size":5,"url":"` + fileURL(r, "/payload/a.csv") + `"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/payload/a.csv":
			downloaded = append(downloaded, r.URL.Path)
			_, _ = w.Write([]byte("hello"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	out := t.TempDir()
	result, err := testService(server.URL).DownloadExportedData(context.Background(), DownloadExportedDataOptions{
		Profile:    testProfile(),
		ClusterID:  "cluster-1",
		ExportID:   "export-1",
		OutputPath: out,
	})
	if err != nil {
		t.Fatalf("DownloadExportedData: %v", err)
	}
	if result.FileCount != 1 || result.Succeeded != 1 || result.Failed != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(out, "DUMP", "a.csv"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("downloaded file = %q, %v", got, err)
	}
	if len(downloaded) != 1 {
		t.Fatalf("payload fetches = %#v", downloaded)
	}
}

func TestDownloadExportedDataSkipsExistingFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","size":5}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files:download"):
			_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","url":"` + fileURL(r, "/payload/a.csv") + `"}]}`))
		case r.URL.Path == "/payload/a.csv":
			t.Fatal("existing file should not be fetched")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "a.csv"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := testService(server.URL).DownloadExportedData(context.Background(), DownloadExportedDataOptions{
		Profile:    testProfile(),
		ClusterID:  "cluster-1",
		ExportID:   "export-1",
		OutputPath: out,
	})
	if err != nil {
		t.Fatalf("DownloadExportedData: %v", err)
	}
	if result.Skipped != 1 || result.Succeeded != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(out, "a.csv"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing file mutated: %q, %v", got, err)
	}
}

func TestDryRunDownloadExportedDataListsFilesWithoutDownloading(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","size":5}]}`))
		case r.Method == http.MethodPost:
			posts++
			t.Fatalf("dry-run posted %s", r.URL.Path)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).DryRunDownloadExportedData(context.Background(), "ti db download-exported-data", DownloadExportedDataOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		ExportID:  "clusters/cluster-1/exports/export-1",
	})
	if err != nil {
		t.Fatalf("DryRunDownloadExportedData: %v", err)
	}
	if !result.DryRun || result.Request.Method != "POST" || posts != 0 {
		t.Fatalf("unexpected dry-run: %#v posts=%d", result, posts)
	}
	body, _ := result.Request.Body.(map[string]any)
	names, _ := body["fileNames"].([]string)
	if len(names) != 1 || names[0] != "a.csv" {
		t.Fatalf("unexpected planned files: %#v", result.Request.Body)
	}
}

func TestDownloadExportedDataRejectsPathEscape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`{"files":[{"name":"../secret","size":1}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	_, err := testService(server.URL).DownloadExportedData(context.Background(), DownloadExportedDataOptions{
		Profile:   testProfile(),
		ClusterID: "cluster-1",
		ExportID:  "export-1",
	})
	if err == nil || !strings.Contains(err.Error(), "outside the output path") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func fileURL(r *http.Request, path string) string {
	return "http://" + r.Host + path
}

func TestDownloadExportedDataResultText(t *testing.T) {
	text := rootdb.DownloadExportedDataResult{
		ClusterID:  "c1",
		ExportID:   "e1",
		OutputPath: "/tmp/out",
		FileCount:  1,
		Succeeded:  1,
		Files:      []rootdb.DownloadedExportFile{{Name: "a.csv", Size: 5, Status: "succeeded", Path: "/tmp/out/a.csv"}},
	}.Human()
	if !strings.Contains(text, "a.csv") || !strings.Contains(text, "succeeded") {
		t.Fatalf("unexpected text:\n%s", text)
	}
}
