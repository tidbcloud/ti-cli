package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tidbcloud/ti-cli/internal/api"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type CopyFileOptions struct {
	Profile       *config.Profile
	FromLocal     string
	ToLocal       string
	FromRemote    string
	ToRemote      string
	FromStdin     bool
	ToStdout      bool
	LayerID       string
	Overwrite     bool
	CreateParents bool
	Append        bool
	Recursive     bool
	Resume        bool
	Tags          map[string]string
	Description   string
}

type ReadFileOptions struct {
	Profile *config.Profile
	Path    string
	Range   bool
	Offset  int64
	Length  int64
}

type ListFilesOptions struct {
	Profile *config.Profile
	Path    string
}

type DescribeFileOptions struct {
	Profile *config.Profile
	Path    string
}

type MoveFileOptions struct {
	Profile    *config.Profile
	FromRemote string
	ToRemote   string
	Overwrite  bool
}

type DeleteFileOptions struct {
	Profile   *config.Profile
	Path      string
	Recursive bool
}

type CreateDirectoryOptions struct {
	Profile *config.Profile
	Path    string
	Mode    string
}

type ChmodFileOptions struct {
	Profile *config.Profile
	Path    string
	Mode    string
}

type SymlinkFileOptions struct {
	Profile *config.Profile
	Target  string
	Link    string
}

type HardlinkFileOptions struct {
	Profile *config.Profile
	Source  string
	Link    string
}

type SearchFileContentOptions struct {
	Profile *config.Profile
	Path    string
	Pattern string
	Limit   int32
	LayerID string
}

type FindFilesOptions struct {
	Profile         *config.Profile
	Path            string
	FileNamePattern string
	ResourceType    string
	Tag             string
	LayerID         string
	Newer           string
	Older           string
	MinSizeBytes    int64
	MaxSizeBytes    int64
	Limit           int32
}

type FileOperationResult struct {
	Operation        string `json:"operation"`
	SourcePath       string `json:"source_path,omitempty"`
	TargetPath       string `json:"target_path,omitempty"`
	BytesTransferred int64  `json:"bytes_transferred,omitempty"`
	FilesTransferred int64  `json:"files_transferred,omitempty"`
	PartsUploaded    int    `json:"parts_uploaded,omitempty"`
	UploadMode       string `json:"upload_mode,omitempty"`
	Revision         int64  `json:"revision,omitempty"`
	Status           string `json:"status"`
}

type FileEntry struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	IsDir     bool   `json:"is_dir"`
	Mtime     int64  `json:"mtime,omitempty"`
}

type ListFilesResult struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

type DescribeFileResult struct {
	Path         string            `json:"path"`
	SizeBytes    int64             `json:"size_bytes"`
	IsDir        bool              `json:"is_dir"`
	Revision     int64             `json:"revision,omitempty"`
	Mtime        int64             `json:"mtime,omitempty"`
	Mode         int64             `json:"mode,omitempty"`
	HasMode      bool              `json:"has_mode,omitempty"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Nlink        int64             `json:"nlink,omitempty"`
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

type SearchFilesResult struct {
	Path    string         `json:"path"`
	Results []SearchResult `json:"results"`
}

func (s Service) CopyFile(ctx context.Context, opts CopyFileOptions) (FileOperationResult, error) {
	return s.drive9CopyFile(ctx, opts)
}

func (s Service) ReadFile(ctx context.Context, opts ReadFileOptions) ([]byte, error) {
	return s.drive9ReadFile(ctx, opts)
}

func appendLocalFileToRemote(ctx context.Context, client *apifs.Client, localPath string, target string) (FileOperationResult, error) {
	file, size, err := openLocalRegularFile(localPath)
	if err != nil {
		return FileOperationResult{}, err
	}
	defer file.Close()
	if size == 0 {
		return FileOperationResult{Operation: "append_file", SourcePath: localPath, TargetPath: target, BytesTransferred: 0, Status: "appended"}, nil
	}
	stat, err := client.Stat(ctx, target)
	if err != nil && !isNotFound(err) {
		return FileOperationResult{}, err
	}
	if isNotFound(err) {
		expectedRevision := int64(0)
		return uploadLocalFileToRemote(ctx, client, localPath, target, &expectedRevision, "append_file", "appended", nil, "")
	}
	if stat.IsDir {
		return FileOperationResult{}, apperr.New("fs.target_is_directory", "usage", 2, fmt.Sprintf("remote target %q is a directory", target))
	}

	plan, err := client.InitiateAppend(ctx, target, size, apifs.CalcAdaptivePartSize(stat.SizeBytes+size), stat.Revision)
	if err != nil {
		if shouldRewriteAppend(err) {
			return appendLocalFileByRewrite(ctx, client, localPath, target, stat.Revision)
		}
		return FileOperationResult{}, err
	}
	var offset int64
	err = client.UploadPatchParts(ctx, plan.PatchPlan, func(partNumber int, partSize int64, original []byte) ([]byte, error) {
		if int64(len(original)) > partSize {
			return nil, fmt.Errorf("original part data length %d exceeds final part size %d", len(original), partSize)
		}
		data := make([]byte, partSize)
		copy(data, original)
		need := int(partSize) - len(original)
		if need > 0 {
			n, readErr := file.ReadAt(data[len(original):], offset)
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			if n != need {
				return nil, fmt.Errorf("short append read for part %d: got %d want %d", partNumber, n, need)
			}
			offset += int64(n)
		}
		return data, nil
	})
	if err != nil {
		_ = client.AbortUpload(context.Background(), plan.UploadID)
		return FileOperationResult{}, err
	}
	if offset != size {
		_ = client.AbortUpload(context.Background(), plan.UploadID)
		return FileOperationResult{}, apperr.New("fs.append_size_mismatch", "runtime", 1, fmt.Sprintf("append source size mismatch: %d bytes remaining", size-offset))
	}
	if err := client.CompleteUpload(ctx, plan.UploadID); err != nil {
		_ = client.AbortUpload(context.Background(), plan.UploadID)
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "append_file", SourcePath: localPath, TargetPath: target, BytesTransferred: size, Status: "appended", PartsUploaded: len(plan.UploadParts), UploadMode: "append_patch"}, nil
}

func appendLocalFileByRewrite(ctx context.Context, client *apifs.Client, localPath, target string, expectedRevision int64) (FileOperationResult, error) {
	appendData, err := os.ReadFile(localPath)
	if err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.read_local_file", "runtime", 1, fmt.Sprintf("read local file %q", localPath), err)
	}
	existing, err := client.ReadFile(ctx, target)
	if err != nil {
		return FileOperationResult{}, err
	}
	data := make([]byte, 0, len(existing)+len(appendData))
	data = append(data, existing...)
	data = append(data, appendData...)
	written, err := client.WriteFileWithOptions(ctx, target, data, apifs.WriteFileOptions{ExpectedRevision: &expectedRevision})
	if err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "append_file", SourcePath: localPath, TargetPath: target, BytesTransferred: int64(len(appendData)), Revision: written.Revision, Status: "appended"}, nil
}

func resumeLocalFileToRemote(ctx context.Context, client *apifs.Client, localPath, target string, tags map[string]string) (FileOperationResult, error) {
	file, size, err := openLocalRegularFile(localPath)
	if err != nil {
		return FileOperationResult{}, err
	}
	defer file.Close()
	result, err := client.ResumeUploadWithOptions(ctx, target, file, size, apifs.UploadFileOptions{Tags: tags})
	if err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: localPath, TargetPath: target, BytesTransferred: size, FilesTransferred: 1, Status: "resumed", PartsUploaded: result.PartsUploaded, UploadMode: result.Mode}, nil
}

func uploadLocalFileToRemote(ctx context.Context, client *apifs.Client, localPath, target string, expectedRevision *int64, operation, status string, tags map[string]string, description string) (FileOperationResult, error) {
	file, size, err := openLocalRegularFile(localPath)
	if err != nil {
		return FileOperationResult{}, err
	}
	defer file.Close()
	if size >= apifs.DefaultSmallFileThreshold {
		result, err := client.UploadFile(ctx, target, file, size, apifs.UploadFileOptions{ExpectedRevision: expectedRevision, Tags: tags, Description: description})
		if err != nil {
			return FileOperationResult{}, err
		}
		return FileOperationResult{Operation: operation, SourcePath: localPath, TargetPath: target, BytesTransferred: size, FilesTransferred: 1, Status: status, PartsUploaded: result.PartsUploaded, UploadMode: result.Mode}, nil
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.read_local_file", "runtime", 1, fmt.Sprintf("read local file %q", localPath), err)
	}
	written, err := client.WriteFileWithOptions(ctx, target, data, apifs.WriteFileOptions{ExpectedRevision: expectedRevision, Tags: tags, Description: description})
	if err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: operation, SourcePath: localPath, TargetPath: target, BytesTransferred: int64(len(data)), Revision: written.Revision, Status: status}, nil
}

func openLocalRegularFile(localPath string) (*os.File, int64, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, 0, apperr.Wrap("fs.open_local_file", "runtime", 1, fmt.Sprintf("open local file %q", localPath), err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local file %q", localPath), err)
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, 0, apperr.New("fs.source_is_directory", "usage", 2, fmt.Sprintf("local source %q is a directory; use --recursive", localPath))
	}
	return file, info.Size(), nil
}

func resumeRemoteFileToLocal(ctx context.Context, client *apifs.Client, source, target string) (FileOperationResult, error) {
	local, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileOperationResult{}, apperr.New("fs.resume_target_missing", "usage", 2, fmt.Sprintf("local target %q does not exist; omit --resume for a fresh download", target))
		}
		return FileOperationResult{}, apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local file %q", target), err)
	}
	if local.IsDir() {
		return FileOperationResult{}, apperr.New("fs.resume_target_directory", "usage", 2, fmt.Sprintf("local target %q is a directory", target))
	}
	stat, err := client.Stat(ctx, source)
	if err != nil {
		return FileOperationResult{}, err
	}
	if stat.IsDir {
		return FileOperationResult{}, apperr.New("fs.source_is_directory", "usage", 2, fmt.Sprintf("remote source %q is a directory; use --recursive", source))
	}
	offset := local.Size()
	if offset > stat.SizeBytes {
		return FileOperationResult{}, apperr.New("fs.resume_target_too_large", "usage", 2, fmt.Sprintf("local target %q is larger than remote source %q", target, source))
	}
	if offset == stat.SizeBytes {
		return FileOperationResult{Operation: "copy_file", SourcePath: source, TargetPath: target, BytesTransferred: 0, Status: "already_complete"}, nil
	}
	data, err := client.ReadFileRange(ctx, source, offset, stat.SizeBytes-offset)
	if err != nil {
		return FileOperationResult{}, err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.open_local_file", "runtime", 1, fmt.Sprintf("open local file %q", target), err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.write_local_file", "runtime", 1, fmt.Sprintf("append local file %q", target), err)
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: source, TargetPath: target, BytesTransferred: int64(len(data)), Status: "resumed"}, nil
}

func copyLocalTreeToRemote(ctx context.Context, client *apifs.Client, sourceRoot, targetRoot string, overwrite bool) (FileOperationResult, error) {
	info, err := os.Stat(sourceRoot)
	if err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local path %q", sourceRoot), err)
	}
	if !info.IsDir() {
		if err := ensureRemoteTargetCanWrite(ctx, client, targetRoot, overwrite); err != nil {
			return FileOperationResult{}, err
		}
		data, err := os.ReadFile(sourceRoot)
		if err != nil {
			return FileOperationResult{}, apperr.Wrap("fs.read_local_file", "runtime", 1, fmt.Sprintf("read local file %q", sourceRoot), err)
		}
		written, err := client.WriteFile(ctx, targetRoot, data)
		if err != nil {
			return FileOperationResult{}, err
		}
		return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: int64(len(data)), Revision: written.Revision, Status: "copied"}, nil
	}
	var bytesTransferred int64
	var filesTransferred int64
	err = filepath.WalkDir(sourceRoot, func(localPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, localPath)
		if err != nil {
			return err
		}
		if rel == "." {
			return ensureRemoteDirectory(ctx, client, targetRoot)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return apperr.New("fs.recursive_symlink_unsupported", "usage", 2, fmt.Sprintf("recursive copy does not support symlinks yet: %s", localPath))
		}
		remotePath, err := remoteJoin(targetRoot, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return ensureRemoteDirectory(ctx, client, remotePath)
		}
		if err := ensureRemoteTargetCanWrite(ctx, client, remotePath, overwrite); err != nil {
			return err
		}
		data, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		if _, err := client.WriteFile(ctx, remotePath, data); err != nil {
			return err
		}
		bytesTransferred += int64(len(data))
		filesTransferred++
		return nil
	})
	if err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: bytesTransferred, FilesTransferred: filesTransferred, Status: "copied"}, nil
}

func copyRemoteTreeToLocal(ctx context.Context, client *apifs.Client, sourceRoot, targetRoot string, overwrite, createParents, resume bool) (FileOperationResult, error) {
	stat, err := client.Stat(ctx, sourceRoot)
	if err != nil {
		return FileOperationResult{}, err
	}
	if !stat.IsDir {
		if createParents {
			if err := os.MkdirAll(filepath.Dir(targetRoot), 0o755); err != nil {
				return FileOperationResult{}, apperr.Wrap("fs.create_local_parent", "runtime", 1, fmt.Sprintf("create parent directories for %q", targetRoot), err)
			}
		}
		if resume {
			return resumeRemoteFileToLocal(ctx, client, sourceRoot, targetRoot)
		}
		if err := ensureLocalTargetCanWrite(targetRoot, overwrite, createParents); err != nil {
			return FileOperationResult{}, err
		}
		data, err := client.ReadFile(ctx, sourceRoot)
		if err != nil {
			return FileOperationResult{}, err
		}
		if err := os.WriteFile(targetRoot, data, 0o644); err != nil {
			return FileOperationResult{}, apperr.Wrap("fs.write_local_file", "runtime", 1, fmt.Sprintf("write local file %q", targetRoot), err)
		}
		return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: int64(len(data)), Status: "copied"}, nil
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return FileOperationResult{}, apperr.Wrap("fs.create_local_directory", "runtime", 1, fmt.Sprintf("create local directory %q", targetRoot), err)
	}
	var bytesTransferred int64
	var filesTransferred int64
	if err := walkRemoteTree(ctx, client, sourceRoot, func(remotePath string, entry apifs.FileInfo) error {
		rel := strings.TrimPrefix(strings.TrimPrefix(remotePath, sourceRoot), "/")
		localPath := filepath.Join(targetRoot, filepath.FromSlash(rel))
		if entry.IsDir {
			return os.MkdirAll(localPath, 0o755)
		}
		if err := ensureLocalTargetCanWrite(localPath, overwrite, true); err != nil {
			return err
		}
		data, err := client.ReadFile(ctx, remotePath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			return err
		}
		bytesTransferred += int64(len(data))
		filesTransferred++
		return nil
	}); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: bytesTransferred, FilesTransferred: filesTransferred, Status: "copied"}, nil
}

func copyRemoteTreeToRemote(ctx context.Context, client *apifs.Client, sourceRoot, targetRoot string, overwrite bool) (FileOperationResult, error) {
	stat, err := client.Stat(ctx, sourceRoot)
	if err != nil {
		return FileOperationResult{}, err
	}
	if !stat.IsDir {
		if err := ensureRemoteTargetCanWrite(ctx, client, targetRoot, overwrite); err != nil {
			return FileOperationResult{}, err
		}
		if err := client.CopyRemote(ctx, sourceRoot, targetRoot); err != nil {
			return FileOperationResult{}, err
		}
		return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: stat.SizeBytes, Status: "copied"}, nil
	}
	if err := ensureRemoteDirectory(ctx, client, targetRoot); err != nil {
		return FileOperationResult{}, err
	}
	var filesTransferred int64
	var bytesTransferred int64
	if err := walkRemoteTree(ctx, client, sourceRoot, func(remotePath string, entry apifs.FileInfo) error {
		rel := strings.TrimPrefix(strings.TrimPrefix(remotePath, sourceRoot), "/")
		targetPath, err := remoteJoin(targetRoot, rel)
		if err != nil {
			return err
		}
		if entry.IsDir {
			return ensureRemoteDirectory(ctx, client, targetPath)
		}
		if err := ensureRemoteTargetCanWrite(ctx, client, targetPath, overwrite); err != nil {
			return err
		}
		if err := client.CopyRemote(ctx, remotePath, targetPath); err != nil {
			return err
		}
		filesTransferred++
		bytesTransferred += entry.Size
		return nil
	}); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: sourceRoot, TargetPath: targetRoot, BytesTransferred: bytesTransferred, FilesTransferred: filesTransferred, Status: "copied"}, nil
}

func (s Service) ListFiles(ctx context.Context, opts ListFilesOptions) (ListFilesResult, error) {
	return s.drive9ListFiles(ctx, opts)
}

func (s Service) DescribeFile(ctx context.Context, opts DescribeFileOptions) (DescribeFileResult, error) {
	return s.drive9DescribeFile(ctx, opts)
}

func (s Service) MoveFile(ctx context.Context, opts MoveFileOptions) (FileOperationResult, error) {
	return s.drive9MoveFile(ctx, opts)
}

func (s Service) DeleteFile(ctx context.Context, opts DeleteFileOptions) (FileOperationResult, error) {
	return s.drive9DeleteFile(ctx, opts)
}

func (s Service) CreateDirectory(ctx context.Context, opts CreateDirectoryOptions) (FileOperationResult, error) {
	return s.drive9CreateDirectory(ctx, opts)
}

func (s Service) ChmodFile(ctx context.Context, opts ChmodFileOptions) (FileOperationResult, error) {
	return s.drive9ChmodFile(ctx, opts)
}

func (s Service) SymlinkFile(ctx context.Context, opts SymlinkFileOptions) (FileOperationResult, error) {
	return s.drive9SymlinkFile(ctx, opts)
}

func (s Service) HardlinkFile(ctx context.Context, opts HardlinkFileOptions) (FileOperationResult, error) {
	return s.drive9HardlinkFile(ctx, opts)
}

func (s Service) SearchFileContent(ctx context.Context, opts SearchFileContentOptions) (SearchFilesResult, error) {
	return s.drive9SearchFileContent(ctx, opts)
}

func (s Service) FindFiles(ctx context.Context, opts FindFilesOptions) (SearchFilesResult, error) {
	return s.drive9FindFiles(ctx, opts)
}

func (s Service) dataClient(profile *config.Profile, permission authz.Permission, action string) (*apifs.Client, error) {
	if profile == nil {
		return nil, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	endpoint, err := s.resolveFS(profile)
	if err != nil {
		return nil, err
	}
	return s.bearerClient(profile, endpoint, permission, action)
}

func normalizeRemotePath(value string) (string, error) {
	if value == "" {
		return "", apperr.New("fs.missing_remote_path", "usage", 2, "remote path is required")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", apperr.New("fs.invalid_remote_path", "usage", 2, "remote path must not contain control characters")
		}
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	hasTrailingSlash := strings.HasSuffix(value, "/")
	cleaned := path.Clean(value)
	if cleaned == "." {
		cleaned = "/"
	}
	if hasTrailingSlash && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned, nil
}

func defaultRemotePath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/"
	}
	return value
}

func parseMode(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 8, 64)
	if err != nil || parsed < 0 {
		return 0, apperr.New("fs.invalid_mode", "usage", 2, "--mode must be an octal value such as 0755")
	}
	return parsed, nil
}

func parseRequiredMode(value, flagName string) (int64, error) {
	parsed, err := parseMode(value)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(value) == "" {
		return 0, apperr.New("fs.missing_mode", "usage", 2, flagName+" is required")
	}
	if parsed > 0o7777 {
		return 0, apperr.New("fs.invalid_mode", "usage", 2, flagName+" must be an octal permission value such as 0644")
	}
	return parsed, nil
}

func validateCopyMetadataFlags(opts CopyFileOptions) error {
	hasMetadata := len(opts.Tags) > 0 || strings.TrimSpace(opts.Description) != ""
	if !hasMetadata {
		return nil
	}
	if opts.Recursive {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--tag and --description cannot be combined with --recursive")
	}
	if opts.Append {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--tag and --description cannot be combined with --append")
	}
	if opts.Resume && strings.TrimSpace(opts.Description) != "" {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--description cannot be combined with --resume")
	}
	if strings.TrimSpace(opts.ToRemote) == "" {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--tag and --description are only supported for uploads to --to-remote")
	}
	if strings.TrimSpace(opts.FromRemote) != "" {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--tag and --description are only supported for local or stdin uploads")
	}
	if opts.LayerID != "" {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "--tag and --description cannot be combined with --layer-id")
	}
	return nil
}

func ParseFileTags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag %q; expected key=value", raw))
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, apperr.New("fs.invalid_tag", "usage", 2, fmt.Sprintf("invalid tag %q; key is empty", raw))
		}
		if _, exists := out[key]; exists {
			return nil, apperr.New("fs.duplicate_tag", "usage", 2, fmt.Sprintf("duplicate tag %q", key))
		}
		out[key] = value
	}
	return out, nil
}

func ensureRemoteTargetCanWrite(ctx context.Context, client *apifs.Client, remotePath string, overwrite bool) error {
	if overwrite {
		return nil
	}
	_, err := client.Stat(ctx, remotePath)
	if err == nil {
		return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("remote target %q already exists; pass --overwrite to replace it", remotePath))
	}
	if isNotFound(err) {
		return nil
	}
	return err
}

func ensureRemoteDirectory(ctx context.Context, client *apifs.Client, remotePath string) error {
	stat, err := client.Stat(ctx, remotePath)
	if err == nil {
		if stat.IsDir {
			return nil
		}
		return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("remote target %q already exists and is not a directory", remotePath))
	}
	if !isNotFound(err) {
		return err
	}
	if err := client.Mkdir(ctx, remotePath, 0o755); err != nil {
		if !isConflict(err) {
			return err
		}
		stat, statErr := client.Stat(ctx, remotePath)
		if statErr != nil {
			return statErr
		}
		if stat.IsDir {
			return nil
		}
		return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("remote target %q already exists and is not a directory", remotePath))
	}
	return nil
}

func remoteJoin(root, rel string) (string, error) {
	root, err := normalizeRemotePath(root)
	if err != nil {
		return "", err
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == "." || rel == "" {
		return root, nil
	}
	return normalizeRemotePath(path.Join(root, rel))
}

func walkRemoteTree(ctx context.Context, client *apifs.Client, root string, visit func(remotePath string, entry apifs.FileInfo) error) error {
	root = strings.TrimSuffix(root, "/")
	if root == "" {
		root = "/"
	}
	response, err := client.List(ctx, root)
	if err != nil {
		return err
	}
	for _, entry := range response.Entries {
		child, err := remoteJoin(root, entry.Name)
		if err != nil {
			return err
		}
		if err := visit(child, entry); err != nil {
			return err
		}
		if entry.IsDir {
			if err := walkRemoteTree(ctx, client, child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureLocalTargetCanWrite(localPath string, overwrite, createParents bool) error {
	if strings.TrimSpace(localPath) == "" {
		return apperr.New("fs.missing_local_path", "usage", 2, "local path is required")
	}
	if createParents {
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return apperr.Wrap("fs.create_local_parent", "runtime", 1, fmt.Sprintf("create parent directories for %q", localPath), err)
		}
	}
	if overwrite {
		return nil
	}
	if _, err := os.Stat(localPath); err == nil {
		return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("local target %q already exists; pass --overwrite to replace it", localPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local file %q", localPath), err)
	}
	return nil
}

func isNotFound(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func isConflict(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

func shouldRewriteAppend(err error) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(apiErr.Message + " " + apiErr.Body)
	return strings.Contains(message, "file is not s3-stored") ||
		strings.Contains(message, "s3 not configured") ||
		strings.Contains(message, "unknown post action")
}

func shouldFallbackStat(err error) bool {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "api.contract_gap" || apiErr.Code == "api.decode_response" || apiErr.StatusCode == http.StatusBadRequest
}

func describeFromMetadata(remotePath string, metadata apifs.StatMetadataResponse) DescribeFileResult {
	return DescribeFileResult{
		Path:         remotePath,
		SizeBytes:    metadata.Size,
		IsDir:        metadata.IsDir,
		Revision:     metadata.Revision,
		Mtime:        metadata.Mtime,
		ResourceID:   metadata.ResourceID,
		Nlink:        metadata.Nlink,
		ContentType:  metadata.ContentType,
		SemanticText: metadata.SemanticText,
		Tags:         metadata.Tags,
		Degraded:     metadata.Degraded,
	}
}

func describeFromStat(stat apifs.StatResponse, degraded bool) DescribeFileResult {
	return DescribeFileResult{
		Path:       stat.Path,
		SizeBytes:  stat.SizeBytes,
		IsDir:      stat.IsDir,
		Revision:   stat.Revision,
		Mtime:      stat.Mtime,
		Mode:       stat.Mode,
		HasMode:    stat.HasMode,
		ResourceID: stat.ResourceID,
		Nlink:      stat.Nlink,
		Degraded:   degraded,
	}
}

func searchResults(results []apifs.SearchResult) []SearchResult {
	out := make([]SearchResult, 0, len(results))
	for _, result := range results {
		out = append(out, SearchResult{Path: result.Path, Name: result.Name, SizeBytes: result.SizeBytes, Score: result.Score})
	}
	return out
}

func (r FileOperationResult) Human() string {
	lines := []string{
		"Operation: " + r.Operation,
		"Status: " + r.Status,
	}
	if r.SourcePath != "" {
		lines = append(lines, "Source: "+r.SourcePath)
	}
	if r.TargetPath != "" {
		lines = append(lines, "Target: "+r.TargetPath)
	}
	if r.BytesTransferred > 0 {
		lines = append(lines, fmt.Sprintf("Bytes: %d", r.BytesTransferred))
	}
	if r.FilesTransferred > 0 {
		lines = append(lines, fmt.Sprintf("Files: %d", r.FilesTransferred))
	}
	if r.PartsUploaded > 0 {
		lines = append(lines, fmt.Sprintf("Parts: %d", r.PartsUploaded))
	}
	if r.UploadMode != "" {
		lines = append(lines, "Upload mode: "+r.UploadMode)
	}
	return strings.Join(lines, "\n")
}

func (r ListFilesResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "NAME\tTYPE\tSIZE\tMTIME")
	for _, entry := range r.Entries {
		kind := "file"
		if entry.IsDir {
			kind = "dir"
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%d\t%d\n", entry.Name, kind, entry.SizeBytes, entry.Mtime)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r DescribeFileResult) Human() string {
	lines := []string{
		"Path: " + r.Path,
		fmt.Sprintf("Size: %d", r.SizeBytes),
		fmt.Sprintf("Directory: %t", r.IsDir),
	}
	if r.Revision != 0 {
		lines = append(lines, fmt.Sprintf("Revision: %d", r.Revision))
	}
	if r.ContentType != "" {
		lines = append(lines, "Content type: "+r.ContentType)
	}
	return strings.Join(lines, "\n")
}

func (r SearchFilesResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "PATH\tNAME\tSIZE\tSCORE")
	for _, result := range r.Results {
		score := ""
		if result.Score != nil {
			score = fmt.Sprintf("%.4f", *result.Score)
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%d\t%s\n", result.Path, result.Name, result.SizeBytes, score)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
