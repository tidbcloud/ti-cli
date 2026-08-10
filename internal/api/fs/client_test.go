package fs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/authz"
)

func TestProvisionAndDeleteTenant(t *testing.T) {
	var sawProvision bool
	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/provision":
			sawProvision = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode provision body: %v", err)
			}
			if body["public_key"] != "public" || body["private_key"] != "private" {
				t.Fatalf("unexpected provision body: %#v", body)
			}
			if _, ok := body["tidbcloud_spending_limit"]; ok {
				t.Fatalf("unexpected provision body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(ProvisionResponse{
				TenantID: "tenant-1",
				APIKey:   "fs-secret",
				Status:   "active",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenant":
			sawDelete = true
			var body DeprovisionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delete body: %v", err)
			}
			if body.PublicKey != "public" || body.PrivateKey != "private" {
				t.Fatalf("unexpected delete body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(DeleteResponse{Status: "deleting"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	provision, err := client.Provision(context.Background(), ProvisionRequest{
		PublicKey:  "public",
		PrivateKey: "private",
	})
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}
	if provision.TenantID != "tenant-1" || provision.APIKey != "fs-secret" {
		t.Fatalf("unexpected provision response: %#v", provision)
	}

	deleted, err := client.DeleteTenant(context.Background(), DeprovisionRequest{
		PublicKey:  "public",
		PrivateKey: "private",
	})
	if err != nil {
		t.Fatalf("DeleteTenant failed: %v", err)
	}
	if deleted.Status != "deleting" {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}
	if !sawProvision || !sawDelete {
		t.Fatalf("expected provision and delete to be called")
	}
}

func TestStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/status" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(StatusResponse{
			Status:   "ok",
			TenantID: "tenant-1",
			Kind:     "live",
		})
	}))
	defer server.Close()

	status, err := testClient(t, server.URL).Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.Status != "ok" || status.TenantID != "tenant-1" || status.Kind != "live" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestDataPlaneMethods(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/fs/workspace/read me.txt":
			if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Fatalf("Content-Type = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if string(body) != "hello" {
				t.Fatalf("unexpected write body %q", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(WriteResponse{Revision: 7})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/fs/workspace/conditional.txt":
			if got := r.Header.Get("X-Dat9-Expected-Revision"); got != "13" {
				t.Fatalf("X-Dat9-Expected-Revision = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if string(body) != "conditional" {
				t.Fatalf("unexpected conditional write body %q", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(WriteResponse{Revision: 14})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/fs/workspace/tagged.txt":
			if got := r.Header.Values("X-Dat9-Tag"); strings.Join(got, ",") != "owner=alice,topic=ti" {
				t.Fatalf("X-Dat9-Tag = %v", got)
			}
			if got := r.Header.Get("X-Dat9-Description"); got != "demo file" {
				t.Fatalf("X-Dat9-Description = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(WriteResponse{Revision: 15})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/read me.txt" && r.URL.RawQuery == "":
			_, _ = w.Write([]byte("raw bytes"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/range.txt" && r.URL.RawQuery == "":
			if got := r.Header.Get("Range"); got != "bytes=2-5" {
				t.Fatalf("Range = %q", got)
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("cdef"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/inline.txt" && r.URL.RawQuery == "":
			if got := r.Header.Get("Range"); got != "bytes=2-4" {
				t.Fatalf("Range = %q", got)
			}
			_, _ = w.Write([]byte("abcdef"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace" && r.URL.Query().Get("list") == "1":
			_ = json.NewEncoder(w).Encode(ListResponse{Entries: []FileInfo{{Name: "read me.txt", Size: 9, IsDir: false, Mtime: 1700000000}}})
		case r.Method == http.MethodHead && r.URL.Path == "/v1/fs/workspace/read me.txt":
			w.Header().Set("Content-Length", "9")
			w.Header().Set("X-Dat9-Revision", "11")
			w.Header().Set("X-Dat9-Mtime", "1700000000")
			w.Header().Set("X-Dat9-Mode", "420")
			w.Header().Set("X-Dat9-IsDir", "false")
			w.Header().Set("X-Dat9-Nlink", "1")
			w.Header().Set("X-Dat9-Resource-Id", "resource-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/read me.txt" && r.URL.Query().Get("stat") == "1":
			_ = json.NewEncoder(w).Encode(StatMetadataResponse{
				Size:        9,
				IsDir:       false,
				ResourceID:  "resource-1",
				Nlink:       1,
				Revision:    12,
				Mtime:       1700000001,
				ContentType: "text/plain",
				Tags:        map[string]string{"kind": "doc"},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/fs/workspace/read me.txt" && r.URL.Query().Get("recursive") == "1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/copy.txt" && hasRawQueryKey(r.URL, "copy"):
			if got := r.Header.Get("X-Dat9-Copy-Source"); got != "/workspace/read me.txt" {
				t.Fatalf("copy source = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/moved.txt" && hasRawQueryKey(r.URL, "rename"):
			if got := r.Header.Get("X-Dat9-Rename-Source"); got != "/workspace/read me.txt" {
				t.Fatalf("rename source = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/newdir" && hasRawQueryKey(r.URL, "mkdir") && r.URL.Query().Get("mode") == "700":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/read me.txt" && hasRawQueryKey(r.URL, "chmod"):
			var body struct {
				Mode int64 `json:"mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode chmod body: %v", err)
			}
			if body.Mode != 0o600 {
				t.Fatalf("chmod mode = %#o", body.Mode)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/link.txt" && r.URL.Query().Get("symlink") == "1":
			var body struct {
				Target string `json:"target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode symlink body: %v", err)
			}
			if body.Target != "../read me.txt" {
				t.Fatalf("symlink target = %q", body.Target)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/hard.txt" && r.URL.Query().Get("hardlink") == "1":
			if got := r.Header.Get("X-Dat9-Hardlink-Source"); got != "/workspace/read me.txt" {
				t.Fatalf("hardlink source = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace" && r.URL.Query().Get("grep") == "needle" && r.URL.Query().Get("limit") == "5":
			_ = json.NewEncoder(w).Encode([]SearchResult{{Path: "/workspace/read me.txt", Name: "read me.txt", SizeBytes: 9}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace" && hasRawQueryKey(r.URL, "find"):
			if r.URL.Query().Get("name") != "*.txt" || r.URL.Query().Get("type") != "file" || r.URL.Query().Get("minsize") != "1" {
				t.Fatalf("unexpected find query %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]SearchResult{{Path: "/workspace/read me.txt", Name: "read me.txt", SizeBytes: 9}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	ctx := context.Background()
	written, err := client.WriteFile(ctx, "/workspace/read me.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if written.Revision != 7 {
		t.Fatalf("unexpected write response: %#v", written)
	}
	expectedRevision := int64(13)
	conditional, err := client.WriteFileWithOptions(ctx, "/workspace/conditional.txt", []byte("conditional"), WriteFileOptions{ExpectedRevision: &expectedRevision})
	if err != nil {
		t.Fatalf("WriteFileWithOptions failed: %v", err)
	}
	if conditional.Revision != 14 {
		t.Fatalf("unexpected conditional write response: %#v", conditional)
	}
	tagged, err := client.WriteFileWithOptions(ctx, "/workspace/tagged.txt", []byte("tagged"), WriteFileOptions{
		Tags:        map[string]string{"topic": "ti", "owner": "alice"},
		Description: "demo file",
	})
	if err != nil {
		t.Fatalf("WriteFileWithOptions tags failed: %v", err)
	}
	if tagged.Revision != 15 {
		t.Fatalf("unexpected tagged write response: %#v", tagged)
	}
	data, err := client.ReadFile(ctx, "/workspace/read me.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "raw bytes" {
		t.Fatalf("unexpected read bytes %q", data)
	}
	ranged, err := client.ReadFileRange(ctx, "/workspace/range.txt", 2, 4)
	if err != nil {
		t.Fatalf("ReadFileRange failed: %v", err)
	}
	if string(ranged) != "cdef" {
		t.Fatalf("unexpected ranged bytes %q", ranged)
	}
	inlineRange, err := client.ReadFileRange(ctx, "/workspace/inline.txt", 2, 3)
	if err != nil {
		t.Fatalf("ReadFileRange inline fallback failed: %v", err)
	}
	if string(inlineRange) != "cde" {
		t.Fatalf("unexpected inline ranged bytes %q", inlineRange)
	}
	list, err := client.List(ctx, "/workspace")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list.Entries) != 1 || list.Entries[0].Name != "read me.txt" {
		t.Fatalf("unexpected list response: %#v", list)
	}
	stat, err := client.Stat(ctx, "/workspace/read me.txt")
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if stat.SizeBytes != 9 || stat.Revision != 11 || stat.ResourceID != "resource-1" {
		t.Fatalf("unexpected stat: %#v", stat)
	}
	metadata, err := client.StatMetadata(ctx, "/workspace/read me.txt")
	if err != nil {
		t.Fatalf("StatMetadata failed: %v", err)
	}
	if metadata.ContentType != "text/plain" || metadata.Tags["kind"] != "doc" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if err := client.DeleteFile(ctx, "/workspace/read me.txt", true); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}
	if err := client.CopyRemote(ctx, "/workspace/read me.txt", "/workspace/copy.txt"); err != nil {
		t.Fatalf("CopyRemote failed: %v", err)
	}
	if err := client.Rename(ctx, "/workspace/read me.txt", "/workspace/moved.txt"); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	if err := client.Mkdir(ctx, "/workspace/newdir", 0o700); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}
	if err := client.Chmod(ctx, "/workspace/read me.txt", 0o600); err != nil {
		t.Fatalf("Chmod failed: %v", err)
	}
	if err := client.Symlink(ctx, "../read me.txt", "/workspace/link.txt"); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}
	if err := client.Hardlink(ctx, "/workspace/read me.txt", "/workspace/hard.txt"); err != nil {
		t.Fatalf("Hardlink failed: %v", err)
	}
	grep, err := client.Grep(ctx, "/workspace", "needle", 5)
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if len(grep) != 1 || grep[0].Path != "/workspace/read me.txt" {
		t.Fatalf("unexpected grep response: %#v", grep)
	}
	params := url.Values{}
	params.Set("name", "*.txt")
	params.Set("type", "file")
	params.Set("minsize", "1")
	find, err := client.Find(ctx, "/workspace", params)
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if len(find) != 1 || find[0].Name != "read me.txt" {
		t.Fatalf("unexpected find response: %#v", find)
	}
	if len(calls) != 18 {
		t.Fatalf("expected 18 calls, got %d: %#v", len(calls), calls)
	}
}

func TestReadFileRedirectsToPresignedURLWithoutBearer(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/large.bin":
			if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
				t.Fatalf("Authorization = %q", got)
			}
			http.Redirect(w, r, server.URL+"/signed/large.bin", http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/signed/large.bin":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned download should not receive ti auth, got %q", got)
			}
			_, _ = w.Write([]byte("large bytes"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	data, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "large bytes" {
		t.Fatalf("unexpected redirected data %q", data)
	}
}

func TestReadFileRangeRedirectPreservesRangeWithoutBearer(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/fs/workspace/large.bin":
			if got := r.Header.Get("Authorization"); got != "Bearer fs-secret" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := r.Header.Get("Range"); got != "bytes=2-5" {
				t.Fatalf("initial Range = %q", got)
			}
			http.Redirect(w, r, server.URL+"/signed/large.bin", http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/signed/large.bin":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned download should not receive ti auth, got %q", got)
			}
			if got := r.Header.Get("Range"); got != "bytes=2-5" {
				t.Fatalf("redirected Range = %q", got)
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("cdef"))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	data, err := client.ReadFileRange(context.Background(), "/workspace/large.bin", 2, 4)
	if err != nil {
		t.Fatalf("ReadFileRange failed: %v", err)
	}
	if string(data) != "cdef" {
		t.Fatalf("unexpected redirected range data %q", data)
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := api.New(api.Options{
		Endpoint: endpoints.Endpoint{
			Service:    endpoints.ServiceFS,
			BaseURL:    baseURL,
			Provider:   "aws",
			RegionCode: "us-east-1",
		},
		ProfileName: "test",
		Permission:  authz.FSVolumeCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(client)
}

func testBearerClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := api.NewBearerClient("test", "fs-secret", endpoints.Endpoint{
		Service:    endpoints.ServiceFS,
		BaseURL:    baseURL,
		Provider:   "aws",
		RegionCode: "us-east-1",
	}, authz.FSFileRead, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return New(client)
}

func hasRawQueryKey(u *url.URL, key string) bool {
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == key || strings.HasPrefix(part, key+"=") {
			return true
		}
	}
	return false
}
