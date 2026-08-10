package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tiapi "github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const (
	DefaultSmallFileThreshold = 50_000
	DefaultMultipartPartSize  = 8 << 20
	MaxAdaptivePartSize       = 512 << 20
)

const (
	uploadMaxConcurrency = 16
	uploadMaxBufferBytes = 256 << 20
)

type UploadFileOptions struct {
	ExpectedRevision *int64
	Tags             map[string]string
	Description      string
}

type UploadResult struct {
	UploadID      string
	Mode          string
	PartSize      int64
	PartsTotal    int
	PartsUploaded int
	Summary       *UploadSummary
}

type UploadSummary struct {
	Type               string    `json:"type"`
	Mode               string    `json:"mode"`
	StartedAt          time.Time `json:"started_at"`
	FinishedAt         time.Time `json:"finished_at"`
	ElapsedSeconds     float64   `json:"elapsed_seconds"`
	RemotePath         string    `json:"remote_path"`
	TotalBytes         int64     `json:"total_bytes"`
	PartSizeBytes      int64     `json:"part_size_bytes"`
	TotalParts         int       `json:"total_parts"`
	UploadedParts      int       `json:"uploaded_parts"`
	Parallelism        int       `json:"parallelism"`
	QuerySeconds       float64   `json:"query_seconds,omitempty"`
	ChecksumSeconds    float64   `json:"checksum_seconds,omitempty"`
	InitiateSeconds    float64   `json:"initiate_seconds,omitempty"`
	ResumeSeconds      float64   `json:"resume_seconds,omitempty"`
	PresignSeconds     float64   `json:"presign_seconds,omitempty"`
	UploadSeconds      float64   `json:"upload_seconds,omitempty"`
	CompleteSeconds    float64   `json:"complete_seconds,omitempty"`
	DirectWriteSeconds float64   `json:"direct_write_seconds,omitempty"`
}

type UploadPlan struct {
	UploadID string    `json:"upload_id"`
	PartSize int64     `json:"part_size"`
	Parts    []PartURL `json:"parts"`
}

type PartURL struct {
	Number         int               `json:"number"`
	URL            string            `json:"url"`
	Size           int64             `json:"size"`
	ChecksumSHA256 string            `json:"checksum_sha256,omitempty"`
	ChecksumCRC32C string            `json:"checksum_crc32c,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	ExpiresAt      string            `json:"expires_at"`
}

type UploadMeta struct {
	UploadID   string `json:"upload_id"`
	Path       string `json:"path,omitempty"`
	TotalSize  int64  `json:"total_size,omitempty"`
	PartsTotal int    `json:"parts_total"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

type UploadPlanV2 struct {
	UploadID         string           `json:"upload_id"`
	Key              string           `json:"key,omitempty"`
	PartSize         int64            `json:"part_size"`
	TotalParts       int              `json:"total_parts"`
	ExpiresAt        string           `json:"expires_at,omitempty"`
	Resumable        bool             `json:"resumable"`
	ChecksumContract ChecksumContract `json:"checksum_contract"`
}

type ChecksumContract struct {
	Supported []string `json:"supported"`
	Required  bool     `json:"required"`
}

type PresignedPart struct {
	Number         int               `json:"number"`
	URL            string            `json:"url"`
	Size           int64             `json:"size"`
	ChecksumSHA256 string            `json:"checksum_sha256,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	ExpiresAt      time.Time         `json:"expires_at"`
}

type CompletePart struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

type PatchPlan struct {
	UploadID    string          `json:"upload_id"`
	PartSize    int64           `json:"part_size"`
	UploadParts []*PatchPartURL `json:"upload_parts"`
	CopiedParts []int           `json:"copied_parts"`
}

type AppendPlan struct {
	BaseSize int64 `json:"base_size"`
	PatchPlan
}

type PatchPartURL struct {
	Number      int               `json:"number"`
	URL         string            `json:"url"`
	Size        int64             `json:"size"`
	Headers     map[string]string `json:"headers,omitempty"`
	ExpiresAt   string            `json:"expires_at"`
	ReadURL     string            `json:"read_url,omitempty"`
	ReadHeaders map[string]string `json:"read_headers,omitempty"`
}

type patchReadPartFunc func(partNumber int, partSize int64, original []byte) ([]byte, error)

type ProgressFunc func(partNumber, totalParts int, bytesUploaded int64)

type uploadBufferPool struct {
	size int64
	ch   chan []byte
}

type uploadDurationRecorder struct {
	nanos atomic.Int64
}

func (r *uploadDurationRecorder) Add(d time.Duration) {
	if r != nil && d > 0 {
		r.nanos.Add(d.Nanoseconds())
	}
}

func (r *uploadDurationRecorder) Seconds() float64 {
	if r == nil {
		return 0
	}
	return time.Duration(r.nanos.Load()).Seconds()
}

func (c *Client) UploadFile(ctx context.Context, remotePath string, r io.ReaderAt, size int64, opts UploadFileOptions) (UploadResult, error) {
	if size <= 0 {
		return UploadResult{}, apperr.New("fs.invalid_upload_size", "usage", 2, "multipart upload requires a positive file size")
	}
	if err := validateTags(opts.Tags); err != nil {
		return UploadResult{}, err
	}
	summary := newUploadSummary(remotePath, size)
	result, err := c.uploadFileV2(ctx, remotePath, r, size, opts, summary, nil)
	if err == nil {
		result.Summary = finishUploadSummary(summary)
		return result, nil
	}
	if !isV2NotAvailable(err) {
		return UploadResult{}, err
	}
	summary = newUploadSummary(remotePath, size)
	result, err = c.uploadFileV1(ctx, remotePath, r, size, opts, summary, nil)
	if err != nil {
		return UploadResult{}, err
	}
	result.Summary = finishUploadSummary(summary)
	return result, nil
}

func (c *Client) InitiateUploadFromReader(ctx context.Context, remotePath string, r io.ReaderAt, size int64, opts UploadFileOptions) (UploadPlan, error) {
	checksums, err := computePartChecksums(r, size, DefaultMultipartPartSize)
	if err != nil {
		return UploadPlan{}, err
	}
	expectedRevision := int64(-1)
	if opts.ExpectedRevision != nil {
		expectedRevision = *opts.ExpectedRevision
	}
	return c.InitiateUpload(ctx, remotePath, size, checksums, expectedRevision, opts.Description)
}

func (c *Client) ResumeUpload(ctx context.Context, remotePath string, r io.ReaderAt, size int64) (UploadResult, error) {
	return c.ResumeUploadWithOptions(ctx, remotePath, r, size, UploadFileOptions{})
}

func (c *Client) ResumeUploadWithOptions(ctx context.Context, remotePath string, r io.ReaderAt, size int64, opts UploadFileOptions) (UploadResult, error) {
	if size <= 0 {
		return UploadResult{}, apperr.New("fs.invalid_upload_size", "usage", 2, "multipart upload resume requires a positive file size")
	}
	if err := validateTags(opts.Tags); err != nil {
		return UploadResult{}, err
	}
	summary := newUploadSummary(remotePath, size)
	summary.Mode = "resume_v1"
	queryStart := time.Now()
	upload, err := c.ActiveUpload(ctx, remotePath)
	if err != nil {
		return UploadResult{}, err
	}
	summary.QuerySeconds = time.Since(queryStart).Seconds()
	checksumStart := time.Now()
	checksums, err := computePartChecksums(r, size, DefaultMultipartPartSize)
	if err != nil {
		return UploadResult{}, err
	}
	summary.ChecksumSeconds = time.Since(checksumStart).Seconds()
	resumeStart := time.Now()
	plan, err := c.RequestResume(ctx, upload.UploadID, checksums)
	if err != nil {
		return UploadResult{}, err
	}
	summary.ResumeSeconds = time.Since(resumeStart).Seconds()
	summary.PartSizeBytes = partSize(plan.PartSize)
	summary.TotalParts = upload.PartsTotal
	summary.UploadedParts = len(plan.Parts)
	summary.Parallelism = boundedUploadParallelism(summary.PartSizeBytes, len(plan.Parts))
	uploadStart := time.Now()
	if err := c.UploadPlanParts(ctx, *plan, r); err != nil {
		return UploadResult{}, err
	}
	summary.UploadSeconds = time.Since(uploadStart).Seconds()
	completeStart := time.Now()
	if err := c.CompleteUploadWithOptions(ctx, plan.UploadID, CompleteUploadOptions{Tags: opts.Tags}); err != nil {
		return UploadResult{}, err
	}
	summary.CompleteSeconds = time.Since(completeStart).Seconds()
	return UploadResult{UploadID: plan.UploadID, Mode: "resume_v1", PartSize: partSize(plan.PartSize), PartsTotal: upload.PartsTotal, PartsUploaded: len(plan.Parts), Summary: finishUploadSummary(summary)}, nil
}

func (c *Client) InitiateUpload(ctx context.Context, remotePath string, size int64, checksums []string, expectedRevision int64, description string) (UploadPlan, error) {
	body := struct {
		Path             string   `json:"path"`
		TotalSize        int64    `json:"total_size"`
		PartChecksums    []string `json:"part_checksums"`
		ExpectedRevision *int64   `json:"expected_revision,omitempty"`
		Description      string   `json:"description,omitempty"`
	}{
		Path:             remotePath,
		TotalSize:        size,
		PartChecksums:    checksums,
		ExpectedRevision: expectedRevisionPtr(expectedRevision),
		Description:      description,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/uploads/initiate", body)
	if err != nil {
		return UploadPlan{}, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return UploadPlan{}, err
	}
	defer res.Body.Close()
	var plan UploadPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return UploadPlan{}, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs upload plan", err)
	}
	return plan, nil
}

func (c *Client) uploadFileV1(ctx context.Context, remotePath string, r io.ReaderAt, size int64, opts UploadFileOptions, summary *UploadSummary, progress ProgressFunc) (UploadResult, error) {
	if summary != nil {
		summary.Mode = "multipart_v1"
	}
	checksumStart := time.Now()
	checksums, err := computePartChecksums(r, size, DefaultMultipartPartSize)
	if err != nil {
		return UploadResult{}, err
	}
	if summary != nil {
		summary.ChecksumSeconds = time.Since(checksumStart).Seconds()
	}
	expectedRevision := int64(-1)
	if opts.ExpectedRevision != nil {
		expectedRevision = *opts.ExpectedRevision
	}
	initiateStart := time.Now()
	plan, err := c.InitiateUpload(ctx, remotePath, size, checksums, expectedRevision, opts.Description)
	if err != nil {
		return UploadResult{}, err
	}
	if summary != nil {
		summary.InitiateSeconds = time.Since(initiateStart).Seconds()
		summary.PartSizeBytes = partSize(plan.PartSize)
		summary.TotalParts = len(plan.Parts)
		summary.UploadedParts = len(plan.Parts)
		summary.Parallelism = boundedUploadParallelism(summary.PartSizeBytes, len(plan.Parts))
	}
	uploadStart := time.Now()
	if err := c.UploadPlanPartsWithProgress(ctx, plan, r, progress); err != nil {
		_ = c.AbortUpload(context.Background(), plan.UploadID)
		return UploadResult{}, err
	}
	if summary != nil {
		summary.UploadSeconds = time.Since(uploadStart).Seconds()
	}
	completeStart := time.Now()
	if err := c.CompleteUploadWithOptions(ctx, plan.UploadID, CompleteUploadOptions{Tags: opts.Tags}); err != nil {
		_ = c.AbortUpload(context.Background(), plan.UploadID)
		return UploadResult{}, err
	}
	if summary != nil {
		summary.CompleteSeconds = time.Since(completeStart).Seconds()
	}
	return UploadResult{UploadID: plan.UploadID, Mode: "multipart_v1", PartSize: partSize(plan.PartSize), PartsTotal: len(plan.Parts), PartsUploaded: len(plan.Parts)}, nil
}

func (c *Client) uploadFileV2(ctx context.Context, remotePath string, r io.ReaderAt, size int64, opts UploadFileOptions, summary *UploadSummary, progress ProgressFunc) (UploadResult, error) {
	initiateStart := time.Now()
	expectedRevision := int64(-1)
	if opts.ExpectedRevision != nil {
		expectedRevision = *opts.ExpectedRevision
	}
	plan, err := c.InitiateUploadV2(ctx, remotePath, size, expectedRevision, opts.Description)
	if err != nil {
		return UploadResult{}, err
	}
	if summary != nil {
		summary.Mode = "multipart_v2"
		summary.InitiateSeconds = time.Since(initiateStart).Seconds()
		summary.PartSizeBytes = partSize(plan.PartSize)
		summary.TotalParts = plan.TotalParts
		summary.UploadedParts = plan.TotalParts
		summary.Parallelism = boundedUploadParallelism(summary.PartSizeBytes, plan.TotalParts)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	parallelism := boundedUploadParallelism(plan.PartSize, plan.TotalParts)
	presignCh := make(chan PresignedPart, parallelism)
	presignErrCh := make(chan error, 1)
	presignRecorder := &uploadDurationRecorder{}
	go c.presignPipeline(ctx, plan, parallelism, presignCh, presignErrCh, presignRecorder)

	uploadStart := time.Now()
	parts, err := c.uploadPartsV2(ctx, plan, r, presignCh, presignErrCh, presignRecorder, progress)
	if err != nil {
		_ = c.AbortUploadV2(context.Background(), plan.UploadID)
		return UploadResult{}, err
	}
	if summary != nil {
		summary.UploadSeconds = time.Since(uploadStart).Seconds()
		summary.PresignSeconds = presignRecorder.Seconds()
	}

	completeStart := time.Now()
	if err := c.CompleteUploadV2(ctx, plan.UploadID, parts, opts.Tags); err != nil {
		_ = c.AbortUploadV2(context.Background(), plan.UploadID)
		return UploadResult{}, err
	}
	if summary != nil {
		summary.CompleteSeconds = time.Since(completeStart).Seconds()
	}
	return UploadResult{UploadID: plan.UploadID, Mode: "multipart_v2", PartSize: partSize(plan.PartSize), PartsTotal: plan.TotalParts, PartsUploaded: plan.TotalParts}, nil
}

func (c *Client) InitiateUploadV2(ctx context.Context, remotePath string, size int64, expectedRevision int64, description string) (*UploadPlanV2, error) {
	body := struct {
		Path             string `json:"path"`
		TotalSize        int64  `json:"total_size"`
		ExpectedRevision *int64 `json:"expected_revision,omitempty"`
		Description      string `json:"description,omitempty"`
	}{
		Path:             remotePath,
		TotalSize:        size,
		ExpectedRevision: expectedRevisionPtr(expectedRevision),
		Description:      description,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v2/uploads/initiate", body)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		if isV2NotAvailable(err) {
			return nil, errV2NotAvailable
		}
		return nil, err
	}
	defer res.Body.Close()
	var plan UploadPlanV2
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return nil, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs v2 upload plan", err)
	}
	return &plan, nil
}

func (c *Client) ActiveUpload(ctx context.Context, remotePath string) (UploadMeta, error) {
	values := url.Values{}
	values.Set("path", remotePath)
	values.Set("status", "UPLOADING")
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/uploads?"+values.Encode(), nil)
	if err != nil {
		return UploadMeta{}, err
	}
	var response struct {
		Uploads []UploadMeta `json:"uploads"`
	}
	if err := c.api.DoJSON(req, &response); err != nil {
		return UploadMeta{}, err
	}
	if len(response.Uploads) == 0 {
		return UploadMeta{}, apperr.New("fs.upload_resume_missing", "runtime", 1, fmt.Sprintf("no active upload exists for %q; run copy-file without --resume to start a fresh upload", remotePath))
	}
	return response.Uploads[0], nil
}

func (c *Client) RequestResume(ctx context.Context, uploadID string, checksums []string) (*UploadPlan, error) {
	plan, err := c.requestResumeByBody(ctx, uploadID, checksums)
	if err != nil {
		if shouldUseLegacyResume(err) {
			return c.requestResumeLegacy(ctx, uploadID, checksums)
		}
		return nil, err
	}
	return plan, nil
}

func (c *Client) requestResumeByBody(ctx context.Context, uploadID string, checksums []string) (*UploadPlan, error) {
	body := struct {
		PartChecksums []string `json:"part_checksums"`
	}{PartChecksums: checksums}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(uploadID)+"/resume", body)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var plan UploadPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return nil, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs upload resume plan", err)
	}
	return &plan, nil
}

func (c *Client) requestResumeLegacy(ctx context.Context, uploadID string, checksums []string) (*UploadPlan, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(uploadID)+"/resume", nil)
	if err != nil {
		return nil, err
	}
	if len(checksums) > 0 {
		req.Header.Set("X-Dat9-Part-Checksums", strings.Join(checksums, ","))
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var plan UploadPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return nil, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs legacy upload resume plan", err)
	}
	return &plan, nil
}

func (c *Client) CompleteUpload(ctx context.Context, uploadID string) error {
	return c.CompleteUploadWithOptions(ctx, uploadID, CompleteUploadOptions{})
}

type CompleteUploadOptions struct {
	Tags map[string]string
}

func (c *Client) CompleteUploadWithOptions(ctx context.Context, uploadID string, opts CompleteUploadOptions) error {
	var body any
	if opts.Tags != nil {
		if err := validateTags(opts.Tags); err != nil {
			return err
		}
		body = struct {
			Tags map[string]string `json:"tags"`
		}{Tags: opts.Tags}
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(uploadID)+"/complete", nil)
	if err != nil {
		return err
	}
	if body != nil {
		req, err = c.api.NewRequest(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(uploadID)+"/complete", body)
		if err != nil {
			return err
		}
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func (c *Client) CompleteUploadV2(ctx context.Context, uploadID string, parts []CompletePart, tags map[string]string) error {
	if err := validateTags(tags); err != nil {
		return err
	}
	body := struct {
		Parts []CompletePart    `json:"parts"`
		Tags  map[string]string `json:"tags,omitempty"`
	}{Parts: parts, Tags: tags}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v2/uploads/"+url.PathEscape(uploadID)+"/complete", body)
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

func (c *Client) AbortUpload(ctx context.Context, uploadID string) error {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/uploads/"+url.PathEscape(uploadID), nil)
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

func (c *Client) AbortUploadV2(ctx context.Context, uploadID string) error {
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v2/uploads/"+url.PathEscape(uploadID)+"/abort", nil)
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

func (c *Client) presignPipeline(ctx context.Context, plan *UploadPlanV2, batchSize int, presignCh chan<- PresignedPart, errCh chan<- error, recorder *uploadDurationRecorder) {
	defer close(presignCh)
	if batchSize < 1 {
		batchSize = 1
	}
	for start := 1; start <= plan.TotalParts; start += batchSize {
		end := start + batchSize - 1
		if end > plan.TotalParts {
			end = plan.TotalParts
		}
		entries := make([]struct {
			PartNumber int `json:"part_number"`
		}, 0, end-start+1)
		for i := start; i <= end; i++ {
			entries = append(entries, struct {
				PartNumber int `json:"part_number"`
			}{PartNumber: i})
		}
		body := struct {
			Parts any `json:"parts"`
		}{Parts: entries}
		req, err := c.api.NewRequest(ctx, http.MethodPost, "/v2/uploads/"+url.PathEscape(plan.UploadID)+"/presign-batch", body)
		if err != nil {
			sendUploadErr(ctx, errCh, fmt.Errorf("create presign batch request: %w", err))
			return
		}
		started := time.Now()
		res, err := c.api.DoRaw(req)
		if err != nil {
			sendUploadErr(ctx, errCh, fmt.Errorf("presign batch: %w", err))
			return
		}
		var response struct {
			Parts []PresignedPart `json:"parts"`
		}
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
			_ = res.Body.Close()
			sendUploadErr(ctx, errCh, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs v2 presign batch", err))
			return
		}
		_ = res.Body.Close()
		recorder.Add(time.Since(started))
		for _, part := range response.Parts {
			select {
			case presignCh <- part:
			case <-ctx.Done():
				sendUploadErr(context.Background(), errCh, ctx.Err())
				return
			}
		}
	}
}

func (c *Client) uploadPartsV2(ctx context.Context, plan *UploadPlanV2, r io.ReaderAt, presignCh <-chan PresignedPart, presignErrCh <-chan error, recorder *uploadDurationRecorder, progress ProgressFunc) ([]CompletePart, error) {
	parallelism := boundedUploadParallelism(plan.PartSize, plan.TotalParts)
	bufferPool := newUploadBufferPool(partSize(plan.PartSize), parallelism)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]CompletePart, plan.TotalParts)
	errCh := make(chan error, 1)
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup

	for part := range presignCh {
		select {
		case err := <-presignErrCh:
			cancel()
			wg.Wait()
			return nil, fmt.Errorf("presign pipeline: %w", err)
		default:
		}
		select {
		case err := <-errCh:
			cancel()
			wg.Wait()
			return nil, err
		default:
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func(p PresignedPart) {
			defer wg.Done()
			defer func() { <-sem }()
			buf, err := bufferPool.get(ctx)
			if err != nil {
				return
			}
			defer bufferPool.put(buf)
			data := buf[:p.Size]
			offset := int64(p.Number-1) * partSize(plan.PartSize)
			n, err := r.ReadAt(data, offset)
			if err != nil && err != io.EOF {
				sendUploadErr(ctx, errCh, fmt.Errorf("read v2 upload part %d: %w", p.Number, err))
				cancel()
				return
			}
			if int64(n) != p.Size {
				sendUploadErr(ctx, errCh, fmt.Errorf("short read for v2 upload part %d: got %d want %d", p.Number, n, p.Size))
				cancel()
				return
			}
			etag, err := uploadPresignedPartV2(ctx, p, data)
			if errors.Is(err, errPresignExpired) {
				presignStart := time.Now()
				fresh, presignErr := c.PresignOnePart(ctx, plan.UploadID, p.Number)
				recorder.Add(time.Since(presignStart))
				if presignErr != nil {
					sendUploadErr(ctx, errCh, fmt.Errorf("re-presign part %d: %w", p.Number, presignErr))
					cancel()
					return
				}
				etag, err = uploadPresignedPartV2(ctx, *fresh, data)
			}
			if err != nil {
				sendUploadErr(ctx, errCh, fmt.Errorf("v2 upload part %d: %w", p.Number, err))
				cancel()
				return
			}
			results[p.Number-1] = CompletePart{Number: p.Number, ETag: etag}
			if progress != nil {
				progress(p.Number, plan.TotalParts, p.Size)
			}
		}(part)
	}
	wg.Wait()

	select {
	case err := <-presignErrCh:
		return nil, fmt.Errorf("presign pipeline: %w", err)
	default:
	}
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	return results, nil
}

func (c *Client) PresignOnePart(ctx context.Context, uploadID string, partNumber int) (*PresignedPart, error) {
	body := struct {
		PartNumber int `json:"part_number"`
	}{PartNumber: partNumber}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v2/uploads/"+url.PathEscape(uploadID)+"/presign", body)
	if err != nil {
		return nil, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var part PresignedPart
	if err := json.NewDecoder(res.Body).Decode(&part); err != nil {
		return nil, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs v2 presigned part", err)
	}
	return &part, nil
}

func (c *Client) InitiateAppend(ctx context.Context, remotePath string, appendSize, requestedPartSize int64, expectedRevision int64) (AppendPlan, error) {
	body := struct {
		AppendSize       int64  `json:"append_size"`
		PartSize         int64  `json:"part_size,omitempty"`
		ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	}{
		AppendSize:       appendSize,
		PartSize:         requestedPartSize,
		ExpectedRevision: expectedRevisionPtr(expectedRevision),
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, fsPathWithRawQuery(remotePath, "append"), body)
	if err != nil {
		return AppendPlan{}, err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return AppendPlan{}, err
	}
	defer res.Body.Close()
	var plan AppendPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return AppendPlan{}, apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs append plan", err)
	}
	return plan, nil
}

func (c *Client) UploadPlanParts(ctx context.Context, plan UploadPlan, r io.ReaderAt) error {
	return c.UploadPlanPartsWithProgress(ctx, plan, r, nil)
}

func (c *Client) UploadPlanPartsWithProgress(ctx context.Context, plan UploadPlan, r io.ReaderAt, progress ProgressFunc) error {
	stdPartSize := partSize(plan.PartSize)
	parallelism := boundedUploadParallelism(stdPartSize, len(plan.Parts))
	bufferPool := newUploadBufferPool(stdPartSize, parallelism)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, parallelism)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for _, part := range plan.Parts {
		select {
		case err := <-errCh:
			cancel()
			wg.Wait()
			return err
		default:
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(p PartURL) {
			defer wg.Done()
			defer func() { <-sem }()
			if p.Size < 0 {
				sendUploadErr(ctx, errCh, fmt.Errorf("upload part %d has invalid size %d", p.Number, p.Size))
				cancel()
				return
			}
			buf, err := bufferPool.get(ctx)
			if err != nil {
				return
			}
			defer bufferPool.put(buf)
			data := buf[:p.Size]
			offset := int64(p.Number-1) * stdPartSize
			n, err := r.ReadAt(data, offset)
			if err != nil && err != io.EOF {
				sendUploadErr(ctx, errCh, fmt.Errorf("read upload part %d: %w", p.Number, err))
				cancel()
				return
			}
			if int64(n) != p.Size {
				sendUploadErr(ctx, errCh, fmt.Errorf("short read for upload part %d: got %d want %d", p.Number, n, p.Size))
				cancel()
				return
			}
			if err := uploadPresignedPart(ctx, p.URL, p.Headers, data, p.ChecksumCRC32C); err != nil {
				sendUploadErr(ctx, errCh, fmt.Errorf("upload part %d: %w", p.Number, err))
				cancel()
				return
			}
			if progress != nil {
				progress(p.Number, len(plan.Parts), p.Size)
			}
		}(part)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
}

func (c *Client) PatchFile(ctx context.Context, remotePath string, newSize int64, dirtyParts []int, readPart patchReadPartFunc, opts PatchFileOptions) error {
	body := struct {
		NewSize          int64  `json:"new_size"`
		DirtyParts       []int  `json:"dirty_parts"`
		PartSize         int64  `json:"part_size,omitempty"`
		ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	}{
		NewSize:          newSize,
		DirtyParts:       dirtyParts,
		PartSize:         opts.PartSize,
		ExpectedRevision: opts.ExpectedRevision,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPatch, fsPath(remotePath), body)
	if err != nil {
		return err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var plan PatchPlan
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		return apperr.Wrap("api.decode_response", "runtime", 1, "decode ti fs patch plan", err)
	}
	if err := c.UploadPatchParts(ctx, plan, readPart); err != nil {
		_ = c.AbortUpload(context.Background(), plan.UploadID)
		return err
	}
	if err := c.CompleteUpload(ctx, plan.UploadID); err != nil {
		_ = c.AbortUpload(context.Background(), plan.UploadID)
		return err
	}
	return nil
}

type PatchFileOptions struct {
	PartSize         int64
	ExpectedRevision *int64
}

func (c *Client) UploadPatchParts(ctx context.Context, plan PatchPlan, readPart patchReadPartFunc) error {
	const maxPatchConcurrency = 4
	sem := make(chan struct{}, maxPatchConcurrency)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for _, part := range plan.UploadParts {
		if part == nil {
			continue
		}
		select {
		case err := <-errCh:
			cancel()
			wg.Wait()
			return err
		default:
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(p *PatchPartURL) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := uploadPatchPart(ctx, p, readPart); err != nil {
				sendUploadErr(ctx, errCh, fmt.Errorf("part %d: %w", p.Number, err))
				cancel()
			}
		}(part)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return nil
}

func uploadPatchPart(ctx context.Context, part *PatchPartURL, readPart patchReadPartFunc) error {
	var original []byte
	if part.ReadURL != "" {
		data, err := readPresignedPart(ctx, part.ReadURL, part.ReadHeaders)
		if err != nil {
			return fmt.Errorf("read original patch part: %w", err)
		}
		original = data
	}
	data, err := readPart(part.Number, part.Size, original)
	if err != nil {
		return fmt.Errorf("build patch part: %w", err)
	}
	if int64(len(data)) != part.Size {
		return fmt.Errorf("patch part size mismatch: got %d want %d", len(data), part.Size)
	}
	if err := uploadPresignedPatchPart(ctx, part.URL, part.Headers, data); err != nil {
		return fmt.Errorf("upload patch part: %w", err)
	}
	return nil
}

func uploadPresignedPart(ctx context.Context, rawURL string, headers map[string]string, data []byte, checksumCRC32C string) error {
	return uploadPresignedPartWithChecksum(ctx, rawURL, headers, data, checksumCRC32C, true)
}

var errPresignExpired = errors.New("presigned URL expired")

func IsPresignExpired(err error) bool {
	return errors.Is(err, errPresignExpired)
}

func uploadPresignedPartV2(ctx context.Context, part PresignedPart, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.URL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	for k, v := range part.Headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(data))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, res.Body)
		return "", errPresignExpired
	}
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024))
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return res.Header.Get("ETag"), nil
}

func uploadPresignedPatchPart(ctx context.Context, rawURL string, headers map[string]string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	var wantsSHA256 bool
	for k, v := range headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		if strings.EqualFold(k, "x-amz-checksum-sha256") {
			wantsSHA256 = true
		}
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(data))
	if wantsSHA256 {
		sum := sha256.Sum256(data)
		req.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(sum[:]))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func uploadPresignedPartWithChecksum(ctx context.Context, rawURL string, headers map[string]string, data []byte, checksumCRC32C string, sendChecksum bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(data))
	if sendChecksum {
		if checksumCRC32C == "" {
			checksumCRC32C = computeCRC32C(data)
		}
		req.Header.Set("x-amz-checksum-crc32c", checksumCRC32C)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	return nil
}

func readPresignedPart(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if strings.EqualFold(k, "host") {
			continue
		}
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024))
		return nil, fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(res.Body)
}

func computePartChecksums(r io.ReaderAt, totalSize, partSizeBytes int64) ([]string, error) {
	if totalSize <= 0 {
		return nil, nil
	}
	partSizeBytes = partSize(partSizeBytes)
	parts := calcParts(totalSize, partSizeBytes)
	checksums := make([]string, len(parts))
	parallelism := checksumParallelism(partSizeBytes, len(parts))
	jobs := make(chan int, parallelism)
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				part := parts[idx]
				data := make([]byte, part.Size)
				offset := int64(part.Number-1) * partSizeBytes
				n, err := r.ReadAt(data, offset)
				if err != nil && err != io.EOF {
					sendUploadErr(ctx, errCh, fmt.Errorf("read checksum part %d: %w", part.Number, err))
					cancel()
					return
				}
				if int64(n) != part.Size {
					sendUploadErr(ctx, errCh, fmt.Errorf("short read for checksum part %d: got %d want %d", part.Number, n, part.Size))
					cancel()
					return
				}
				checksums[idx] = computeCRC32C(data)
			}
		}()
	}
	for i := range parts {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	return checksums, nil
}

func calcParts(totalSize, partSizeBytes int64) []PartURL {
	partSizeBytes = partSize(partSizeBytes)
	count := int((totalSize + partSizeBytes - 1) / partSizeBytes)
	parts := make([]PartURL, count)
	for i := 0; i < count; i++ {
		size := partSizeBytes
		if i == count-1 {
			size = totalSize - int64(i)*partSizeBytes
		}
		parts[i] = PartURL{Number: i + 1, Size: size}
	}
	return parts
}

func CalcAdaptivePartSize(totalSize int64) int64 {
	const align = 1 << 20
	partSizeBytes := (totalSize + 9999) / 10000
	partSizeBytes = ((partSizeBytes + align - 1) / align) * align
	if partSizeBytes < DefaultMultipartPartSize {
		partSizeBytes = DefaultMultipartPartSize
	}
	if partSizeBytes > MaxAdaptivePartSize {
		partSizeBytes = MaxAdaptivePartSize
	}
	return partSizeBytes
}

func boundedUploadParallelism(partSizeBytes int64, partCount int) int {
	parallelism := uploadParallelism(partSizeBytes)
	if partCount > 0 && parallelism > partCount {
		parallelism = partCount
	}
	if parallelism < 1 {
		return 1
	}
	return parallelism
}

func uploadParallelism(partSizeBytes int64) int {
	partSizeBytes = partSize(partSizeBytes)
	byMemory := int(uploadMaxBufferBytes / partSizeBytes)
	if byMemory < 1 {
		byMemory = 1
	}
	return min(uploadMaxConcurrency, byMemory)
}

func checksumParallelism(partSizeBytes int64, partCount int) int {
	partSizeBytes = partSize(partSizeBytes)
	byMemory := int(uploadMaxBufferBytes / partSizeBytes)
	if byMemory < 1 {
		byMemory = 1
	}
	return min(runtime.GOMAXPROCS(0), partCount, byMemory)
}

func newUploadBufferPool(bufferSize int64, count int) *uploadBufferPool {
	bufferSize = partSize(bufferSize)
	if count < 1 {
		count = 1
	}
	ch := make(chan []byte, count)
	for i := 0; i < count; i++ {
		ch <- make([]byte, bufferSize)
	}
	return &uploadBufferPool{size: bufferSize, ch: ch}
}

func (p *uploadBufferPool) get(ctx context.Context) ([]byte, error) {
	select {
	case buf := <-p.ch:
		return buf[:p.size], nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *uploadBufferPool) put(buf []byte) {
	if buf == nil || int64(cap(buf)) < p.size {
		return
	}
	p.ch <- buf[:p.size]
}

func partSize(value int64) int64 {
	if value <= 0 {
		return DefaultMultipartPartSize
	}
	return value
}

func newUploadSummary(remotePath string, size int64) *UploadSummary {
	return &UploadSummary{
		Type:       "upload_summary",
		StartedAt:  time.Now(),
		RemotePath: remotePath,
		TotalBytes: size,
	}
}

func finishUploadSummary(summary *UploadSummary) *UploadSummary {
	if summary == nil {
		return nil
	}
	summary.FinishedAt = time.Now()
	summary.ElapsedSeconds = summary.FinishedAt.Sub(summary.StartedAt).Seconds()
	return summary
}

func expectedRevisionPtr(revision int64) *int64 {
	if revision < 0 {
		return nil
	}
	value := revision
	return &value
}

var errV2NotAvailable = errors.New("v2 upload API not available")

func isV2NotAvailable(err error) bool {
	if errors.Is(err, errV2NotAvailable) {
		return true
	}
	var apiErr *tiapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusMethodNotAllowed || apiErr.Code == "api.contract_gap" {
		return true
	}
	if apiErr.StatusCode == http.StatusBadRequest {
		message := strings.ToLower(apiErr.Message + " " + apiErr.Body)
		return strings.Contains(message, "unknown upload action") || strings.Contains(message, "unknown v2")
	}
	return false
}

func shouldUseLegacyResume(err error) bool {
	var apiErr *tiapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(apiErr.Message + " " + apiErr.Body)
	return strings.Contains(message, "missing x-dat9-part-checksums header")
}

func validateTags(tags map[string]string) error {
	for k, v := range tags {
		if k == "" {
			return apperr.New("fs.invalid_tag", "usage", 2, "tag keys must be non-empty")
		}
		if !utf8.ValidString(k) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag key %q: contains invalid UTF-8", k))
		}
		if !utf8.ValidString(v) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag value for key %q: contains invalid UTF-8", k))
		}
		if containsTagControlChars(k) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag key %q: contains control characters", k))
		}
		if containsTagControlChars(v) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag value for key %q: contains control characters", k))
		}
		if k != strings.TrimSpace(k) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag key %q: must not have leading or trailing whitespace", k))
		}
		if v != strings.TrimSpace(v) {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag value for key %q: must not have leading or trailing whitespace", k))
		}
		if strings.Contains(k, "=") {
			return apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag key %q: contains '='", k))
		}
		if utf8.RuneCountInString(k) > 255 {
			return apperr.New("fs.invalid_tag", "usage", 2, "invalid tags: key exceeds 255 characters")
		}
		if utf8.RuneCountInString(v) > 255 {
			return apperr.New("fs.invalid_tag", "usage", 2, "invalid tags: value exceeds 255 characters")
		}
	}
	return nil
}

func containsTagControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func sendUploadErr(ctx context.Context, ch chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case ch <- err:
	case <-ctx.Done():
	default:
	}
}

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

func computeCRC32C(data []byte) string {
	checksum := crc32.Checksum(data, crc32cTable)
	raw := make([]byte, 4)
	binary.BigEndian.PutUint32(raw, checksum)
	return base64.StdEncoding.EncodeToString(raw)
}
