package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type shortUploadReader struct {
	data          []byte
	seq           *strings.Reader
	shortOffset   int64
	mu            sync.Mutex
	readsByOffset map[int64]int
}

func newShortUploadReader(data string, shortOffset int64) *shortUploadReader {
	return &shortUploadReader{
		data:          []byte(data),
		seq:           strings.NewReader(data),
		shortOffset:   shortOffset,
		readsByOffset: map[int64]int{},
	}
}

func (r *shortUploadReader) Read(p []byte) (int, error) {
	return r.seq.Read(p)
}

func (r *shortUploadReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	r.readsByOffset[off]++
	readCount := r.readsByOffset[off]
	r.mu.Unlock()

	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	if off == r.shortOffset && readCount == 2 {
		n := copy(p[:len(p)-1], r.data[off:])
		return n, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestUploadBufferPoolRestoresFullLengthOnPut(t *testing.T) {
	pool := newUploadBufferPool(8, 1)

	buf, err := pool.get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(buf) != 8 {
		t.Fatalf("initial len = %d, want 8", len(buf))
	}

	pool.put(buf[:3])
	buf, err = pool.get(context.Background())
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if len(buf) != 8 {
		t.Fatalf("restored len = %d, want 8", len(buf))
	}
}

func TestUploadBufferPoolPutDropsForeignShortBuffer(t *testing.T) {
	pool := newUploadBufferPool(8, 1)
	buf, err := pool.get(context.Background())
	if err != nil {
		t.Fatalf("initial get: %v", err)
	}

	pool.put(make([]byte, 4))
	pool.put(buf[:3])

	buf, err = pool.get(context.Background())
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if len(buf) != 8 {
		t.Fatalf("restored len = %d, want 8", len(buf))
	}
}

func TestUploadBufferPoolGetHonorsContextCancel(t *testing.T) {
	pool := newUploadBufferPool(4, 1)
	buf, err := pool.get(context.Background())
	if err != nil {
		t.Fatalf("initial get: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled get error = %v, want %v", err, context.Canceled)
	}

	pool.put(buf)
}

func TestBoundedUploadParallelismCapsAtPartCount(t *testing.T) {
	if got := boundedUploadParallelism(DefaultMultipartPartSize, 2); got != 2 {
		t.Fatalf("parallelism = %d, want 2", got)
	}
	if got := boundedUploadParallelism(DefaultMultipartPartSize, 0); got != uploadParallelism(DefaultMultipartPartSize) {
		t.Fatalf("parallelism for unknown part count = %d, want %d", got, uploadParallelism(DefaultMultipartPartSize))
	}
}

func TestUploadFileInitiatesUploadsPartsAndCompletes(t *testing.T) {
	payload := strings.Repeat("upload-payload-", 4096)
	var initiated bool
	var uploaded bool
	var completed bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/initiate":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/initiate":
			initiated = true
			var body struct {
				Path             string   `json:"path"`
				TotalSize        int64    `json:"total_size"`
				PartChecksums    []string `json:"part_checksums"`
				ExpectedRevision *int64   `json:"expected_revision,omitempty"`
				Description      string   `json:"description,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode initiate body: %v", err)
			}
			if body.Path != "/workspace/upload.bin" || body.TotalSize != int64(len(payload)) || len(body.PartChecksums) != 1 || body.PartChecksums[0] == "" {
				t.Fatalf("unexpected initiate body: %#v", body)
			}
			if body.ExpectedRevision == nil || *body.ExpectedRevision != 5 {
				t.Fatalf("expected revision = %#v", body.ExpectedRevision)
			}
			_ = json.NewEncoder(w).Encode(UploadPlan{
				UploadID: "upload-1",
				PartSize: DefaultMultipartPartSize,
				Parts: []PartURL{{
					Number:  1,
					URL:     server.URL + "/parts/1",
					Size:    int64(len(payload)),
					Headers: map[string]string{"X-Upload-Token": "part"},
				}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/parts/1":
			uploaded = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			if got := r.Header.Get("X-Upload-Token"); got != "part" {
				t.Fatalf("X-Upload-Token = %q", got)
			}
			if got := r.Header.Get("x-amz-checksum-crc32c"); got == "" {
				t.Fatalf("missing crc32c checksum header")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read upload body: %v", err)
			}
			if string(body) != payload {
				t.Fatalf("unexpected upload body length %d", len(body))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/upload-1/complete":
			completed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	expectedRevision := int64(5)
	result, err := testClient(t, server.URL).UploadFile(context.Background(), "/workspace/upload.bin", strings.NewReader(payload), int64(len(payload)), UploadFileOptions{ExpectedRevision: &expectedRevision, Description: "fallback"})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	if !initiated || !uploaded || !completed || result.UploadID != "upload-1" || result.Mode != "multipart_v1" || result.PartsUploaded != 1 {
		t.Fatalf("unexpected upload result: %#v initiated=%t uploaded=%t completed=%t", result, initiated, uploaded, completed)
	}
}

func TestUploadFileUsesV2PresignRetryAndCompleteMetadata(t *testing.T) {
	payload := strings.Repeat("v2-payload-", 4096)
	var firstPartAttempts int
	var freshUploaded bool
	var completed bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/initiate":
			var body struct {
				Path             string `json:"path"`
				TotalSize        int64  `json:"total_size"`
				ExpectedRevision *int64 `json:"expected_revision,omitempty"`
				Description      string `json:"description,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode v2 initiate body: %v", err)
			}
			if body.Path != "/workspace/v2.bin" || body.TotalSize != int64(len(payload)) || body.Description != "artifact" {
				t.Fatalf("unexpected v2 initiate body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(UploadPlanV2{
				UploadID:   "v2-1",
				PartSize:   int64(len(payload)),
				TotalParts: 1,
				Resumable:  true,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-1/presign-batch":
			var body struct {
				Parts []struct {
					PartNumber int `json:"part_number"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode presign batch: %v", err)
			}
			if len(body.Parts) != 1 || body.Parts[0].PartNumber != 1 {
				t.Fatalf("unexpected presign batch body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"parts": []PresignedPart{{
					Number: 1,
					URL:    server.URL + "/v2-presigned/expired",
					Size:   int64(len(payload)),
				}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v2-presigned/expired":
			firstPartAttempts++
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-1/presign":
			var body struct {
				PartNumber int `json:"part_number"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode presign one: %v", err)
			}
			if body.PartNumber != 1 {
				t.Fatalf("unexpected presign one body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(PresignedPart{
				Number: 1,
				URL:    server.URL + "/v2-presigned/fresh",
				Size:   int64(len(payload)),
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v2-presigned/fresh":
			freshUploaded = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read v2 part body: %v", err)
			}
			if string(body) != payload {
				t.Fatalf("unexpected v2 part body length %d", len(body))
			}
			w.Header().Set("ETag", `"etag-1"`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-1/complete":
			completed = true
			var body struct {
				Parts []CompletePart    `json:"parts"`
				Tags  map[string]string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			if len(body.Parts) != 1 || body.Parts[0].Number != 1 || body.Parts[0].ETag != `"etag-1"` || body.Tags["kind"] != "demo" {
				t.Fatalf("unexpected v2 complete body: %#v", body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	result, err := testClient(t, server.URL).UploadFile(context.Background(), "/workspace/v2.bin", strings.NewReader(payload), int64(len(payload)), UploadFileOptions{
		Description: "artifact",
		Tags:        map[string]string{"kind": "demo"},
	})
	if err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	if firstPartAttempts != 1 || !freshUploaded || !completed || result.Mode != "multipart_v2" || result.Summary == nil || result.Summary.Mode != "multipart_v2" {
		t.Fatalf("unexpected v2 upload result: %#v firstPartAttempts=%d freshUploaded=%t completed=%t", result, firstPartAttempts, freshUploaded, completed)
	}
}

func TestUploadFileV2MultiPartUsesPlanPartSize(t *testing.T) {
	payload := "abcdefghijkl"
	var mu sync.Mutex
	uploadedParts := map[int]string{}
	var presignReq struct {
		Parts []struct {
			PartNumber int `json:"part_number"`
		} `json:"parts"`
	}
	var completeReq struct {
		Parts []CompletePart `json:"parts"`
	}
	var progressCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/initiate":
			var body struct {
				Path      string `json:"path"`
				TotalSize int64  `json:"total_size"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode initiate body: %v", err)
			}
			if body.Path != "/workspace/v2-multi.bin" || body.TotalSize != int64(len(payload)) {
				t.Fatalf("unexpected initiate body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(UploadPlanV2{
				UploadID:   "v2-multi",
				PartSize:   5,
				TotalParts: 3,
				Resumable:  true,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-multi/presign-batch":
			if err := json.NewDecoder(r.Body).Decode(&presignReq); err != nil {
				t.Fatalf("decode presign body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"parts": []PresignedPart{
					{Number: 1, URL: server.URL + "/v2parts/1", Size: 5},
					{Number: 2, URL: server.URL + "/v2parts/2", Size: 5},
					{Number: 3, URL: server.URL + "/v2parts/3", Size: 2},
				},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v2parts/"):
			var partNumber int
			if _, err := fmt.Sscanf(r.URL.Path, "/v2parts/%d", &partNumber); err != nil {
				t.Fatalf("parse part path: %v", err)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read part body: %v", err)
			}
			mu.Lock()
			uploadedParts[partNumber] = string(body)
			mu.Unlock()
			w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, partNumber))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-multi/complete":
			if err := json.NewDecoder(r.Body).Decode(&completeReq); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	result, err := testClient(t, server.URL).uploadFileV2(context.Background(), "/workspace/v2-multi.bin", strings.NewReader(payload), int64(len(payload)), UploadFileOptions{}, &UploadSummary{}, func(int, int, int64) {
		progressCalls.Add(1)
	})
	if err != nil {
		t.Fatalf("uploadFileV2 failed: %v", err)
	}
	if result.Mode != "multipart_v2" || result.PartsUploaded != 3 || result.PartSize != 5 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(presignReq.Parts) != 3 || presignReq.Parts[0].PartNumber != 1 || presignReq.Parts[1].PartNumber != 2 || presignReq.Parts[2].PartNumber != 3 {
		t.Fatalf("presign batch parts = %#v, want 1,2,3", presignReq.Parts)
	}
	if uploadedParts[1] != "abcde" || uploadedParts[2] != "fghij" || uploadedParts[3] != "kl" {
		t.Fatalf("uploaded parts = %#v", uploadedParts)
	}
	if len(completeReq.Parts) != 3 || completeReq.Parts[0].ETag != `"etag-1"` || completeReq.Parts[1].ETag != `"etag-2"` || completeReq.Parts[2].ETag != `"etag-3"` {
		t.Fatalf("complete parts = %#v", completeReq.Parts)
	}
	if progressCalls.Load() != 3 {
		t.Fatalf("progress calls = %d, want 3", progressCalls.Load())
	}
}

func TestUploadFileV2CarriesExpectedRevision(t *testing.T) {
	expectedRevision := int64(27)
	var gotExpected *int64
	var completeCalled bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/initiate":
			var body struct {
				ExpectedRevision *int64 `json:"expected_revision"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode initiate body: %v", err)
			}
			gotExpected = body.ExpectedRevision
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(UploadPlanV2{UploadID: "v2-cas", PartSize: 4, TotalParts: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-cas/presign-batch":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"parts": []PresignedPart{{Number: 1, URL: server.URL + "/v2parts/1", Size: 4, ExpiresAt: time.Now().Add(time.Minute)}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v2parts/1":
			w.Header().Set("ETag", `"etag-cas"`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/v2-cas/complete":
			completeCalled = true
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	if _, err := testClient(t, server.URL).UploadFile(context.Background(), "/workspace/cas.bin", strings.NewReader("data"), 4, UploadFileOptions{ExpectedRevision: &expectedRevision}); err != nil {
		t.Fatalf("UploadFile failed: %v", err)
	}
	if gotExpected == nil || *gotExpected != expectedRevision {
		t.Fatalf("expected_revision = %#v, want %d", gotExpected, expectedRevision)
	}
	if !completeCalled {
		t.Fatal("complete was not called")
	}
}

func TestUploadFileErrorsOnShortPartReadAndAborts(t *testing.T) {
	var partUploaded bool
	var completed bool
	var aborted bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/uploads/initiate":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/initiate":
			_ = json.NewEncoder(w).Encode(UploadPlan{
				UploadID: "short-read",
				PartSize: 8,
				Parts: []PartURL{{
					Number: 1,
					URL:    server.URL + "/parts/1",
					Size:   8,
				}},
			})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/parts/"):
			partUploaded = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/short-read/complete":
			completed = true
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/uploads/short-read":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	_, err := testClient(t, server.URL).UploadFile(context.Background(), "/workspace/short.bin", newShortUploadReader("12345678", 0), 8, UploadFileOptions{})
	if err == nil || !strings.Contains(err.Error(), "short read") {
		t.Fatalf("error = %v, want short read", err)
	}
	if partUploaded || completed || !aborted {
		t.Fatalf("partUploaded=%t completed=%t aborted=%t, want no upload/no complete/abort", partUploaded, completed, aborted)
	}
}

func TestUploadFileRejectsInvalidTagsBeforeRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, err := testClient(t, server.URL).UploadFile(context.Background(), "/workspace/invalid-tags.bin", strings.NewReader("data"), 4, UploadFileOptions{
		Tags: map[string]string{"owner": string([]byte{0xff})},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("error = %v, want invalid UTF-8", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestCompleteUploadHelpersRejectInvalidTagsBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Client) error
	}{
		{
			name: "v1",
			run: func(c *Client) error {
				return c.CompleteUploadWithOptions(context.Background(), "legacy-invalid-tags", CompleteUploadOptions{Tags: map[string]string{"owner": string([]byte{0xff})}})
			},
		},
		{
			name: "v2",
			run: func(c *Client) error {
				return c.CompleteUploadV2(context.Background(), "v2-invalid-tags", []CompletePart{{Number: 1, ETag: `"etag-1"`}}, map[string]string{"owner": string([]byte{0xff})})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.NotFound(w, r)
			}))
			defer server.Close()

			err := tc.run(testClient(t, server.URL))
			if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
				t.Fatalf("error = %v, want invalid UTF-8", err)
			}
			if requests != 0 {
				t.Fatalf("requests = %d, want 0", requests)
			}
		})
	}
}

func TestValidateTagsMatchesDrive9Constraints(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{name: "key too long", tags: map[string]string{strings.Repeat("k", 256): "v"}, want: "key exceeds 255 characters"},
		{name: "value too long", tags: map[string]string{"owner": strings.Repeat("v", 256)}, want: "value exceeds 255 characters"},
		{name: "key contains equals", tags: map[string]string{"owner=id": "alice"}, want: "contains '='"},
		{name: "key contains control chars", tags: map[string]string{"owner\n": "alice"}, want: "contains control characters"},
		{name: "key has leading or trailing whitespace", tags: map[string]string{" owner ": "alice"}, want: "leading or trailing whitespace"},
		{name: "value contains control chars", tags: map[string]string{"owner": "alice\t"}, want: "contains control characters"},
		{name: "value has leading or trailing whitespace", tags: map[string]string{"owner": " alice "}, want: "leading or trailing whitespace"},
		{name: "key contains invalid utf8", tags: map[string]string{string([]byte{0xff}): "alice"}, want: "invalid UTF-8"},
		{name: "value contains invalid utf8", tags: map[string]string{"owner": string([]byte{0xff})}, want: "invalid UTF-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateTags(tc.tags); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateTags(%v) err = %v, want %q", tc.tags, err, tc.want)
			}
		})
	}
}

func TestResumeUploadRequestsActiveUploadAndMissingParts(t *testing.T) {
	payload := strings.Repeat("resume-payload-", 4096)
	var uploaded bool
	var completed bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/uploads":
			if r.URL.Query().Get("path") != "/workspace/resume.bin" || r.URL.Query().Get("status") != "UPLOADING" {
				t.Fatalf("unexpected query %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"uploads": []UploadMeta{{UploadID: "upload-1", Path: "/workspace/resume.bin", PartsTotal: 1, Status: "UPLOADING"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/upload-1/resume":
			var body struct {
				PartChecksums []string `json:"part_checksums"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode resume body: %v", err)
			}
			if len(body.PartChecksums) != 1 || body.PartChecksums[0] == "" {
				t.Fatalf("unexpected resume body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(UploadPlan{
				UploadID: "upload-1",
				PartSize: DefaultMultipartPartSize,
				Parts: []PartURL{{
					Number: 1,
					URL:    server.URL + "/parts/1",
					Size:   int64(len(payload)),
				}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/parts/1":
			uploaded = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read upload body: %v", err)
			}
			if string(body) != payload {
				t.Fatalf("unexpected upload body length %d", len(body))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/upload-1/complete":
			completed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	result, err := testClient(t, server.URL).ResumeUpload(context.Background(), "/workspace/resume.bin", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("ResumeUpload failed: %v", err)
	}
	if !uploaded || !completed || result.UploadID != "upload-1" || result.PartsUploaded != 1 {
		t.Fatalf("unexpected resume result: %#v uploaded=%t completed=%t", result, uploaded, completed)
	}
}

func TestResumeUploadFallsBackToLegacyChecksumHeader(t *testing.T) {
	payload := "legacy-resume-payload"
	var resumeCalls int
	var uploaded bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/uploads":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"uploads": []UploadMeta{{UploadID: "legacy-1", Path: "/workspace/legacy.bin", PartsTotal: 1, Status: "UPLOADING"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/legacy-1/resume":
			resumeCalls++
			if resumeCalls == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("missing X-Dat9-Part-Checksums header"))
				return
			}
			if got := r.Header.Get("X-Dat9-Part-Checksums"); got == "" {
				t.Fatalf("missing legacy checksum header")
			}
			_ = json.NewEncoder(w).Encode(UploadPlan{
				UploadID: "legacy-1",
				PartSize: DefaultMultipartPartSize,
				Parts: []PartURL{{
					Number: 1,
					URL:    server.URL + "/legacy/1",
					Size:   int64(len(payload)),
				}},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/legacy/1":
			uploaded = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read upload body: %v", err)
			}
			if string(body) != payload {
				t.Fatalf("unexpected upload body %q", body)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/legacy-1/complete":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	result, err := testClient(t, server.URL).ResumeUpload(context.Background(), "/workspace/legacy.bin", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("ResumeUpload failed: %v", err)
	}
	if resumeCalls != 2 || !uploaded || result.PartsUploaded != 1 {
		t.Fatalf("unexpected legacy resume result: %#v calls=%d uploaded=%t", result, resumeCalls, uploaded)
	}
}

func TestInitiateAppendAndUploadPatchParts(t *testing.T) {
	var patchUploaded bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/workspace/log.txt" && hasRawQueryKey(r.URL, "append"):
			var body struct {
				AppendSize       int64  `json:"append_size"`
				PartSize         int64  `json:"part_size"`
				ExpectedRevision *int64 `json:"expected_revision,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode append body: %v", err)
			}
			if body.AppendSize != 3 || body.PartSize != 8 || body.ExpectedRevision == nil || *body.ExpectedRevision != 12 {
				t.Fatalf("unexpected append body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(AppendPlan{
				BaseSize: 3,
				PatchPlan: PatchPlan{
					UploadID: "append-1",
					PartSize: 8,
					UploadParts: []*PatchPartURL{{
						Number:      1,
						URL:         server.URL + "/patch/1",
						Size:        6,
						Headers:     map[string]string{"X-Upload-Token": "append"},
						ReadURL:     server.URL + "/read/1",
						ReadHeaders: map[string]string{"Range": "bytes=0-2"},
					}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/read/1":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned read should not receive ti auth, got %q", got)
			}
			if got := r.Header.Get("Range"); got != "bytes=0-2" {
				t.Fatalf("Range = %q", got)
			}
			_, _ = w.Write([]byte("abc"))
		case r.Method == http.MethodPut && r.URL.Path == "/patch/1":
			patchUploaded = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			if got := r.Header.Get("X-Upload-Token"); got != "append" {
				t.Fatalf("X-Upload-Token = %q", got)
			}
			if got := r.Header.Get("x-amz-checksum-crc32c"); got != "" {
				t.Fatalf("append patch upload should not add unsigned crc32c checksum header, got %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read patch body: %v", err)
			}
			if string(body) != "abcdef" {
				t.Fatalf("unexpected patch body %q", body)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	plan, err := client.InitiateAppend(context.Background(), "/workspace/log.txt", 3, 8, 12)
	if err != nil {
		t.Fatalf("InitiateAppend failed: %v", err)
	}
	if plan.UploadID != "append-1" || plan.BaseSize != 3 {
		t.Fatalf("unexpected append plan: %#v", plan)
	}
	if err := client.UploadPatchParts(context.Background(), plan.PatchPlan, func(partNumber int, partSize int64, original []byte) ([]byte, error) {
		if partNumber != 1 || partSize != 6 || string(original) != "abc" {
			t.Fatalf("unexpected patch callback args: part=%d size=%d original=%q", partNumber, partSize, original)
		}
		return append(append([]byte(nil), original...), []byte("def")...), nil
	}); err != nil {
		t.Fatalf("UploadPatchParts failed: %v", err)
	}
	if !patchUploaded {
		t.Fatalf("expected patch upload")
	}
}

func TestPatchFileUploadsDirtyPartsAndCompletes(t *testing.T) {
	var patchUploaded bool
	var completed bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/fs/workspace/large.bin":
			var body struct {
				NewSize          int64  `json:"new_size"`
				DirtyParts       []int  `json:"dirty_parts"`
				PartSize         int64  `json:"part_size"`
				ExpectedRevision *int64 `json:"expected_revision,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode patch body: %v", err)
			}
			if body.NewSize != 6 || body.PartSize != 6 || len(body.DirtyParts) != 1 || body.DirtyParts[0] != 1 || body.ExpectedRevision == nil || *body.ExpectedRevision != 9 {
				t.Fatalf("unexpected patch body: %#v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(PatchPlan{
				UploadID: "patch-1",
				PartSize: 6,
				UploadParts: []*PatchPartURL{{
					Number:      1,
					URL:         server.URL + "/patch/1",
					Size:        6,
					Headers:     map[string]string{"X-Upload-Token": "patch", "x-amz-checksum-sha256": "placeholder"},
					ReadURL:     server.URL + "/read/1",
					ReadHeaders: map[string]string{"Range": "bytes=0-5"},
				}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/read/1":
			if got := r.Header.Get("Range"); got != "bytes=0-5" {
				t.Fatalf("Range = %q", got)
			}
			_, _ = w.Write([]byte("abcdef"))
		case r.Method == http.MethodPut && r.URL.Path == "/patch/1":
			patchUploaded = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Fatalf("presigned upload should not receive ti auth, got %q", got)
			}
			if got := r.Header.Get("X-Upload-Token"); got != "patch" {
				t.Fatalf("X-Upload-Token = %q", got)
			}
			if got := r.Header.Get("x-amz-checksum-sha256"); got == "" || got == "placeholder" {
				t.Fatalf("expected recomputed sha256 checksum, got %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read patch body: %v", err)
			}
			if string(body) != "abXYef" {
				t.Fatalf("unexpected patch body %q", body)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads/patch-1/complete":
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	expectedRevision := int64(9)
	err := testClient(t, server.URL).PatchFile(context.Background(), "/workspace/large.bin", 6, []int{1}, func(partNumber int, partSize int64, original []byte) ([]byte, error) {
		if partNumber != 1 || partSize != 6 || string(original) != "abcdef" {
			t.Fatalf("unexpected callback args part=%d size=%d original=%q", partNumber, partSize, original)
		}
		return []byte("abXYef"), nil
	}, PatchFileOptions{PartSize: 6, ExpectedRevision: &expectedRevision})
	if err != nil {
		t.Fatalf("PatchFile failed: %v", err)
	}
	if !patchUploaded || !completed {
		t.Fatalf("expected patch upload and complete, uploaded=%t completed=%t", patchUploaded, completed)
	}
}
