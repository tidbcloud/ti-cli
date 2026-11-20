package starter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadExportedDataRefusesPlainHTTPURLs(t *testing.T) {
	const insecureURL = "http://bucket.example.com/a.csv?X-Amz-Signature=secret-signature"
	for name, payloadURL := range map[string]func(*http.Request) string{
		"direct":   func(*http.Request) string { return insecureURL },
		"redirect": func(r *http.Request) string { return fileURL(r, "/payload/a.csv") },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
					_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter"}`))
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
					_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","size":5}]}`))
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files:download"):
					_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","url":"` + payloadURL(r) + `"}]}`))
				case r.URL.Path == "/payload/a.csv":
					http.Redirect(w, r, insecureURL, http.StatusFound)
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
			if err == nil {
				t.Fatal("DownloadExportedData succeeded with a plain http URL")
			}
			if result.Failed != 1 || len(result.Files) != 1 {
				t.Fatalf("unexpected result: %#v", result)
			}
			fileErr := result.Files[0].Error
			if !strings.Contains(fileErr, "not https") {
				t.Fatalf("file error = %q, want an https refusal", fileErr)
			}
			if strings.Contains(fileErr, "secret-signature") {
				t.Fatalf("file error leaks the presigned signature: %q", fileErr)
			}
			if _, statErr := os.Stat(filepath.Join(out, "a.csv")); !os.IsNotExist(statErr) {
				t.Fatalf("refused download still wrote a file: %v", statErr)
			}
		})
	}
}

func TestDownloadExportedDataMalformedURLDoesNotLeak(t *testing.T) {
	const malformedURL = "https://bucket.example.com/a%zz.csv?X-Amz-Signature=secret-signature"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/clusters/cluster-1":
			_, _ = w.Write([]byte(`{"clusterId":"cluster-1","displayName":"demo","servicePlan":"Starter"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","size":5}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files:download"):
			_, _ = w.Write([]byte(`{"files":[{"name":"a.csv","url":"` + malformedURL + `"}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).DownloadExportedData(context.Background(), DownloadExportedDataOptions{
		Profile:    testProfile(),
		ClusterID:  "cluster-1",
		ExportID:   "export-1",
		OutputPath: t.TempDir(),
	})
	if err == nil || result.Failed != 1 || len(result.Files) != 1 {
		t.Fatalf("DownloadExportedData = %#v, %v; want one failed file", result, err)
	}
	fileErr := result.Files[0].Error
	if !strings.Contains(fileErr, "malformed") {
		t.Fatalf("file error = %q, want a malformed URL refusal", fileErr)
	}
	if strings.Contains(fileErr, "secret-signature") || strings.Contains(fileErr, "a%zz") {
		t.Fatalf("file error leaks the URL: %q", fileErr)
	}
	if strings.Contains(err.Error(), "secret-signature") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}
