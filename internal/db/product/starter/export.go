package starter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	apistarter "github.com/tidbcloud/ti-cli/internal/api/starter"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	rootdb "github.com/tidbcloud/ti-cli/internal/db"
	"github.com/tidbcloud/ti-cli/internal/db/validate"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
)

const (
	defaultExportDownloadConcurrency = 3
	maxExportDownloadBatch           = 100
	exportMetadataFileName           = "metadata"
)

func (s Service) ListExportTasks(ctx context.Context, opts ListExportTasksOptions) (ListExportTasksResult, error) {
	clusterID, err := validateListExportTasksOptions(opts)
	if err != nil {
		return ListExportTasksResult{}, err
	}
	permission := operationPermission(opts.Dispatch, authz.StarterExportRead)
	client, err := s.starterClient(opts.Profile, permission, "list Starter export tasks")
	if err != nil {
		return ListExportTasksResult{}, err
	}
	if opts.Dispatch.Resolved == nil {
		if _, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, "BASIC", permission, "list Starter export tasks"); err != nil {
			return ListExportTasksResult{}, err
		}
	}
	response, err := client.ListExports(ctx, clusterID, apistarter.ListExportsOptions{
		PageSize:  opts.PageSize,
		PageToken: opts.PageToken,
		OrderBy:   opts.OrderBy,
	})
	if err != nil {
		return ListExportTasksResult{}, err
	}
	tasks := make([]rootdb.ExportTask, 0, len(response.Exports))
	for _, item := range response.Exports {
		tasks = append(tasks, exportTaskFromAPI(item))
	}
	return ListExportTasksResult{
		ExportTasks:   tasks,
		NextPageToken: response.NextPageToken,
		TotalSize:     response.TotalSize,
	}, nil
}

func (s Service) DownloadExportedData(ctx context.Context, opts DownloadExportedDataOptions) (DownloadExportedDataResult, error) {
	prepared, err := s.prepareExportDownload(ctx, opts)
	if err != nil {
		return DownloadExportedDataResult{}, err
	}
	if err := os.MkdirAll(prepared.outputPath, 0o755); err != nil {
		return DownloadExportedDataResult{}, apperr.Wrap("db.export_output_path", "usage", 2, fmt.Sprintf("cannot create output path %q", prepared.outputPath), err)
	}
	result := DownloadExportedDataResult{
		ClusterID:      prepared.clusterID,
		ExportID:       prepared.exportID,
		OutputPath:     prepared.outputPath,
		FileCount:      len(prepared.files),
		TotalSizeBytes: prepared.totalSize,
		Files:          make([]rootdb.DownloadedExportFile, 0, len(prepared.files)),
	}
	if len(prepared.files) == 0 {
		return result, nil
	}

	jobs := make(chan exportDownloadJob)
	var mu sync.Mutex
	var wait sync.WaitGroup
	for i := 0; i < prepared.concurrency; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for job := range jobs {
				file := s.downloadExportFile(ctx, prepared.outputPath, job)
				mu.Lock()
				result.Files = append(result.Files, file)
				switch file.Status {
				case "succeeded":
					result.Succeeded++
				case "skipped":
					result.Skipped++
				default:
					result.Failed++
				}
				mu.Unlock()
			}
		}()
	}

	for start := 0; start < len(prepared.files); {
		end := start + exportDownloadBatchSize(prepared.concurrency)
		if end > len(prepared.files) {
			end = len(prepared.files)
		}
		batch := prepared.files[start:end]
		names := make([]string, 0, len(batch))
		for _, file := range batch {
			names = append(names, file.Name)
		}
		urls, err := prepared.client.DownloadExportFiles(ctx, prepared.clusterID, prepared.exportID, apistarter.DownloadExportFilesRequest{FileNames: names})
		if err != nil {
			for _, file := range batch {
				jobs <- exportDownloadJob{name: file.Name, size: file.Size, err: err}
			}
			start = end
			continue
		}
		urlByName := make(map[string]string, len(urls.Files))
		for _, file := range urls.Files {
			urlByName[file.Name] = file.URL
		}
		for _, file := range batch {
			jobs <- exportDownloadJob{name: file.Name, size: file.Size, url: urlByName[file.Name]}
		}
		start = end
	}
	close(jobs)
	wait.Wait()

	if result.Failed > 0 {
		return result, downloadExportedDataError{Result: result}
	}
	return result, nil
}

func (s Service) DryRunDownloadExportedData(ctx context.Context, commandPath string, opts DownloadExportedDataOptions) (dryrun.Result, error) {
	prepared, err := s.prepareExportDownload(ctx, opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	names := make([]string, 0, len(prepared.files))
	for _, file := range prepared.files {
		names = append(names, file.Name)
	}
	return dryrun.New(
		commandPath,
		"download_exported_data",
		dryrun.RequestSummary{
			Method: "POST",
			Path:   "/v1beta1/clusters/" + prepared.clusterID + "/exports/" + prepared.exportID + "/files:download",
			Body:   map[string]any{"fileNames": names},
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		dryrun.Check{Name: "operation_permission", Status: "passed", Message: string(operationPermission(opts.Dispatch, authz.StarterExportRead))},
		dryrun.Check{Name: "export_files", Status: "passed", Message: fmt.Sprintf("%d files, %d bytes", len(prepared.files), prepared.totalSize)},
		dryrun.Check{Name: "output_path", Status: "passed", Message: prepared.outputPath},
	), nil
}

type preparedExportDownload struct {
	client      *apistarter.Client
	clusterID   string
	exportID    string
	outputPath  string
	concurrency int
	files       []apistarter.ExportFile
	totalSize   int64
}

func (s Service) prepareExportDownload(ctx context.Context, opts DownloadExportedDataOptions) (preparedExportDownload, error) {
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return preparedExportDownload{}, err
	}
	exportID, err := validate.ExportID(opts.ExportID)
	if err != nil {
		return preparedExportDownload{}, err
	}
	concurrency := opts.Concurrency
	if concurrency == 0 {
		concurrency = defaultExportDownloadConcurrency
	}
	workers, err := validate.Concurrency(concurrency)
	if err != nil {
		return preparedExportDownload{}, err
	}
	if _, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, "BASIC", authz.StarterExportRead, "download Starter exported data"); err != nil {
		return preparedExportDownload{}, err
	}
	client, err := s.starterClient(opts.Profile, operationPermission(opts.Dispatch, authz.StarterExportRead), "download Starter exported data")
	if err != nil {
		return preparedExportDownload{}, err
	}
	files, err := listAllExportFiles(ctx, client, clusterID, exportID)
	if err != nil {
		return preparedExportDownload{}, err
	}
	downloadable := make([]apistarter.ExportFile, 0, len(files))
	var total int64
	for _, file := range files {
		if file.Name == "" || file.Name == exportMetadataFileName {
			continue
		}
		if _, err := safeExportRelativePath(file.Name); err != nil {
			return preparedExportDownload{}, err
		}
		downloadable = append(downloadable, file)
		total += file.Size
	}
	outputPath, err := resolveExportOutputPath(opts.OutputPath)
	if err != nil {
		return preparedExportDownload{}, err
	}
	return preparedExportDownload{
		client:      client,
		clusterID:   clusterID,
		exportID:    exportID,
		outputPath:  outputPath,
		concurrency: workers,
		files:       downloadable,
		totalSize:   total,
	}, nil
}

func listAllExportFiles(ctx context.Context, client *apistarter.Client, clusterID, exportID string) ([]apistarter.ExportFile, error) {
	var files []apistarter.ExportFile
	pageToken := ""
	for {
		page, err := client.ListExportFiles(ctx, clusterID, exportID, apistarter.ListExportFilesOptions{PageToken: pageToken})
		if err != nil {
			return nil, err
		}
		files = append(files, page.Files...)
		if page.NextPageToken == "" {
			return files, nil
		}
		if page.NextPageToken == pageToken {
			return nil, apperr.New("db.export_list_cycle", "api", 1, "TiDB Cloud returned the same export file page token twice")
		}
		pageToken = page.NextPageToken
	}
}

type exportDownloadJob struct {
	name string
	size int64
	url  string
	err  error
}

func (s Service) downloadExportFile(ctx context.Context, outputPath string, job exportDownloadJob) rootdb.DownloadedExportFile {
	file := rootdb.DownloadedExportFile{Name: job.name, Size: job.size}
	rel, err := safeExportRelativePath(job.name)
	if err != nil {
		file.Status = "failed"
		file.Error = err.Error()
		return file
	}
	absPath := filepath.Join(outputPath, rel)
	file.Path = absPath
	if job.err != nil {
		file.Status = "failed"
		file.Error = job.err.Error()
		return file
	}
	if _, err := os.Stat(absPath); err == nil {
		file.Status = "skipped"
		file.Error = "file already exists"
		return file
	} else if !errors.Is(err, fs.ErrNotExist) {
		file.Status = "failed"
		file.Error = err.Error()
		return file
	}
	if strings.TrimSpace(job.url) == "" {
		file.Status = "failed"
		file.Error = "empty download url"
		return file
	}
	written, err := s.copyExportURL(ctx, outputPath, rel, job.url)
	if err != nil {
		file.Status = "failed"
		file.Error = err.Error()
		return file
	}
	file.Status = "succeeded"
	if written > 0 {
		file.Size = written
	}
	return file
}

func (s Service) copyExportURL(ctx context.Context, outputPath, rel, rawURL string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.fileHTTPClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	root, err := os.OpenRoot(outputPath)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if dir := filepath.Dir(rel); dir != "." {
		if err := mkdirAllInRoot(root, dir); err != nil {
			return 0, err
		}
	}
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return 0, fmt.Errorf("file already exists")
		}
		return 0, err
	}
	defer out.Close()
	written, err := io.Copy(out, resp.Body)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(filepath.Join(outputPath, rel))
		return 0, err
	}
	return written, nil
}

func (s Service) fileHTTPClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{}
}

func mkdirAllInRoot(root *os.Root, rel string) error {
	current := ""
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = filepath.Join(current, part)
		}
		if err := root.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func resolveExportOutputPath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", apperr.Wrap("db.export_output_path", "usage", 2, fmt.Sprintf("invalid --output-path %q", value), err)
	}
	return abs, nil
}

func safeExportRelativePath(fileName string) (string, error) {
	if fileName == "" || fileName == "." || fileName == ".." {
		return "", apperr.New("db.invalid_export_file_name", "api", 1, fmt.Sprintf("export file name %q must refer to a file", fileName))
	}
	if filepath.IsAbs(fileName) || looksWindowsAbs(fileName) {
		return "", apperr.New("db.invalid_export_file_name", "api", 1, fmt.Sprintf("export file name %q must be relative to the output path", fileName))
	}
	if strings.ContainsRune(fileName, 0) || strings.IndexFunc(fileName, unicode.IsControl) >= 0 {
		return "", apperr.New("db.invalid_export_file_name", "api", 1, fmt.Sprintf("export file name %q contains unsupported characters", fileName))
	}
	clean := filepath.Clean(fileName)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", apperr.New("db.invalid_export_file_name", "api", 1, fmt.Sprintf("export file name %q is outside the output path", fileName))
	}
	return clean, nil
}

func looksWindowsAbs(path string) bool {
	if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') {
		c := path[0]
		return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
	}
	return strings.HasPrefix(path, `\\`)
}

func exportDownloadBatchSize(concurrency int) int {
	batch := 2 * concurrency
	if batch > maxExportDownloadBatch {
		return maxExportDownloadBatch
	}
	if batch < 1 {
		return 1
	}
	return batch
}

type downloadExportedDataError struct {
	Result DownloadExportedDataResult
}

func (e downloadExportedDataError) Error() string {
	return fmt.Sprintf("%d exported file(s) failed to download", e.Result.Failed)
}

func (e downloadExportedDataError) StructuredResult() any {
	return e.Result
}

func (e downloadExportedDataError) Unwrap() error {
	return apperr.New("db.export_download_failed", "api", 1, e.Error())
}

func validateListExportTasksOptions(opts ListExportTasksOptions) (string, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return "", err
	}
	if err := validate.NonNegative("--page-size", opts.PageSize); err != nil {
		return "", err
	}
	return clusterID, nil
}

func exportTaskFromAPI(item apistarter.Export) rootdb.ExportTask {
	return rootdb.ExportTask{
		ID:           item.ID,
		Name:         item.Name,
		ClusterID:    item.ClusterID,
		DisplayName:  item.DisplayName,
		State:        item.State,
		TargetType:   item.TargetType,
		FileType:     item.FileType,
		CreatedBy:    item.CreatedBy,
		Reason:       item.Reason,
		CreateTime:   item.CreateTime,
		UpdateTime:   item.UpdateTime,
		CompleteTime: item.CompleteTime,
		SnapshotTime: item.SnapshotTime,
		ExpireTime:   item.ExpireTime,
	}
}
