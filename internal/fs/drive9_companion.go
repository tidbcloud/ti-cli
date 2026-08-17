package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
	"github.com/tidbcloud/ti-cli/internal/fswrap"
)

const (
	defaultFSReadyWaitTimeout      = 10 * time.Minute
	defaultFSReadyWaitPollInterval = 5 * time.Second
)

type drive9StatMetadata struct {
	Path         string            `json:"path,omitempty"`
	Size         int64             `json:"size"`
	IsDir        bool              `json:"isdir"`
	ResourceID   string            `json:"resource_id,omitempty"`
	Nlink        int64             `json:"nlink,omitempty"`
	Revision     int64             `json:"revision,omitempty"`
	Mtime        *int64            `json:"mtime,omitempty"`
	Mode         int64             `json:"mode,omitempty"`
	HasMode      bool              `json:"has_mode,omitempty"`
	ContentType  string            `json:"content_type,omitempty"`
	SemanticText string            `json:"semantic_text,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Degraded     bool              `json:"degraded,omitempty"`
}

type drive9CommandResult struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
	Stdout    string `json:"stdout,omitempty"`
}

func (s Service) drive9Runner() fswrap.Runner {
	return fswrap.Runner{
		HomeDir:       s.HomeDir,
		CompanionPath: s.CompanionPath,
		Resolver:      s.Resolver,
		Stdin:         s.Stdin,
		Stdout:        s.Stdout,
		Stderr:        s.Stderr,
		Debug:         s.Debug,
		DebugWriter:   s.DebugWriter,
	}
}

func (s Service) drive9Run(ctx context.Context, profile *config.Profile, args []string, capture bool) (fswrap.Result, error) {
	return s.drive9Runner().Run(ctx, fswrap.RunOptions{
		Profile:         profile,
		Args:            args,
		CaptureStdout:   capture,
		IncludeFSAPIKey: true,
	})
}

func (s Service) drive9RunTransientRetry(ctx context.Context, profile *config.Profile, args []string, capture bool) (fswrap.Result, error) {
	var lastResult fswrap.Result
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.drive9Run(ctx, profile, args, capture)
		if err == nil {
			return result, nil
		}
		lastResult = result
		lastErr = err
		if !isTransientDrive9Error(err) {
			return result, err
		}
		if attempt == 2 {
			break
		}
		if err := sleepDrive9Retry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return lastResult, lastErr
}

func (s Service) drive9RunIdempotentDelete(ctx context.Context, profile *config.Profile, args []string) error {
	var lastErr error
	sawTransient := false
	for attempt := 0; attempt < 3; attempt++ {
		_, err := s.drive9Run(ctx, profile, args, false)
		if err == nil {
			return nil
		}
		if sawTransient && isDrive9NotFound(err) {
			return nil
		}
		lastErr = err
		if !isTransientDrive9Error(err) {
			return err
		}
		sawTransient = true
		if attempt == 2 {
			break
		}
		if err := sleepDrive9Retry(ctx, attempt); err != nil {
			return err
		}
	}
	return lastErr
}

func sleepDrive9Retry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s Service) importFileSystemToken(ctx context.Context, opts ImportFileSystemTokenOptions, persist bool) (ImportFileSystemTokenResult, error) {
	if opts.Profile == nil {
		return ImportFileSystemTokenResult{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	tokenID, err := fscred.FileSystemIDFromToken(opts.Token)
	if err != nil {
		return ImportFileSystemTokenResult{}, err
	}
	if asserted := strings.TrimSpace(opts.FileSystemID); asserted != "" {
		asserted, err = fscred.ValidateFileSystemID(asserted)
		if err != nil {
			return ImportFileSystemTokenResult{}, err
		}
		if asserted != tokenID {
			return ImportFileSystemTokenResult{}, apperr.New("fs.token_file_system_mismatch", "authentication", 3, fmt.Sprintf("FS token belongs to file system %q, not %q", tokenID, asserted))
		}
	}
	placement, err := region.ParsePlacementCode(opts.Profile.PlacementRegionCode)
	if err != nil {
		return ImportFileSystemTokenResult{}, err
	}
	selected := *opts.Profile
	selected.FSResourceName = tokenID
	selected.FSTenantID = tokenID
	selected.FSPlacementRegionCode = placement.Code
	selected.FSCloudProvider = placement.Provider
	selected.FSRegionCode = placement.NativeCode
	selected.FSAPIKey = opts.Token
	validationHome, err := os.MkdirTemp("", "ti-fs-token-validation-*")
	if err != nil {
		return ImportFileSystemTokenResult{}, apperr.Wrap("fs.token_validation", "runtime", 1, "prepare temporary FS token validation state", err)
	}
	defer os.RemoveAll(validationHome)
	runner := s.drive9Runner()
	runner.HomeDir = validationHome
	if _, err := runner.Run(ctx, fswrap.RunOptions{
		Profile:         &selected,
		ResourceName:    tokenID,
		Args:            []string{"fs", "stat", "--output", "json", ":/"},
		CaptureStdout:   true,
		IncludeFSAPIKey: true,
	}); err != nil {
		return ImportFileSystemTokenResult{}, err
	}
	status := "validated"
	stored := false
	if persist {
		homeDir, err := s.homeDir()
		if err != nil {
			return ImportFileSystemTokenResult{}, err
		}
		if _, err := fscred.StoreCredential(homeDir, opts.Profile, tokenID, placement.Code, opts.Token, opts.Replace); err != nil {
			return ImportFileSystemTokenResult{}, err
		}
		status = "imported"
		stored = true
	}
	return ImportFileSystemTokenResult{FileSystemID: tokenID, RegionCode: placement.Code, CredentialsStored: stored, Status: status}, nil
}

func (s Service) waitUntilFileSystemReady(ctx context.Context, homeDir string, profile *config.Profile, fileSystemID string) error {
	selected, _, err := fscred.ResolveCredential(homeDir, profile, fscred.ResolveCredentialOptions{FileSystemID: fileSystemID, FileSystemIDExplicit: true, TokenRequired: true})
	if err != nil {
		return err
	}
	timeout := s.fsReadyWaitTimeout()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(s.fsReadyWaitPollInterval())
	defer ticker.Stop()

	for {
		_, err := s.drive9Run(waitCtx, selected, []string{"fs", "stat", "--output", "json", ":/"}, true)
		if err == nil {
			return nil
		}
		if waitErr := fsReadyWaitContextError(ctx, waitCtx, fileSystemID, timeout); waitErr != nil {
			return waitErr
		}
		if !isDrive9ReadinessError(err) {
			return apperr.Wrap(
				"fs.ready_wait_failed",
				"runtime",
				1,
				fmt.Sprintf("ti fs resource %q was provisioned and its credentials were stored, but its Drive9 data plane readiness check failed", fileSystemID),
				err,
			)
		}

		select {
		case <-waitCtx.Done():
			return fsReadyWaitContextError(ctx, waitCtx, fileSystemID, timeout)
		case <-ticker.C:
		}
	}
}

func fsReadyWaitContextError(parent, waitCtx context.Context, name string, timeout time.Duration) error {
	if parent.Err() != nil {
		return apperr.Wrap(
			"fs.ready_wait_canceled",
			"runtime",
			1,
			fmt.Sprintf("waiting for ti fs resource %q to become ready was canceled; the resource and its local credentials were not deleted", name),
			parent.Err(),
		)
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return apperr.New(
			"fs.ready_wait_timeout",
			"runtime",
			1,
			fmt.Sprintf("ti fs resource %q did not become ready within %s; the resource and its local credentials were not deleted", name, timeout),
		)
	}
	return nil
}

func (s Service) fsReadyWaitTimeout() time.Duration {
	if s.FSReadyWaitTimeout > 0 {
		return s.FSReadyWaitTimeout
	}
	return defaultFSReadyWaitTimeout
}

func (s Service) fsReadyWaitPollInterval() time.Duration {
	if s.FSReadyWaitPollInterval > 0 {
		return s.FSReadyWaitPollInterval
	}
	return defaultFSReadyWaitPollInterval
}

func isDrive9ReadinessError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "storage backend unavailable") ||
		strings.Contains(message, "http 503") ||
		strings.Contains(message, "provision") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "network connectivity") ||
		strings.Contains(message, ": eof") ||
		strings.Contains(message, "i/o timeout") ||
		strings.Contains(message, "timeout awaiting response headers") ||
		strings.Contains(message, "unexpected eof")
}

func (s Service) drive9CheckFileSystem(ctx context.Context, opts CheckFileSystemOptions) (CheckResult, error) {
	if opts.Profile == nil {
		return CheckResult{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	checks := []Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("ti fs credentials for profile namespace %q loaded", profileName(opts.Profile))},
	}
	resource := fscred.Credential{FileSystemID: opts.Profile.FSTenantID, RegionCode: opts.Profile.FSPlacementRegionCode, HasLocalToken: strings.TrimSpace(opts.Profile.FSAPIKey) != "", APIKey: opts.Profile.FSAPIKey}
	if resource.FileSystemID == "" || !resource.HasLocalToken {
		checks = append(checks, Check{Name: "fs_resource_credentials", Status: "warning", Message: "ti fs file system ID or token is missing"})
	} else {
		checks = append(checks, Check{Name: "fs_resource_credentials", Status: "passed", Message: resource.FileSystemID})
	}
	endpoint, err := s.resolveFS(opts.Profile)
	if err != nil {
		checks = append(checks, Check{Name: "endpoint_selection", Status: "failed", Message: apperr.MessageFor(err)})
		return checkResult(opts.Profile, resource, nil, nil, checks), nil
	}
	checks = append(checks, Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)})
	if _, err := s.drive9Runner().CompanionInfo(ctx, opts.Profile); err != nil {
		checks = append(checks, Check{Name: "companion_binary", Status: "failed", Message: apperr.MessageFor(err)})
		return checkResult(opts.Profile, resource, &endpoint, nil, checks), nil
	}
	checks = append(checks, Check{Name: "companion_binary", Status: "passed", Message: "ti-drive9"})
	if !resource.HasLocalToken {
		checks = append(checks, Check{Name: "remote_status", Status: "warning", Message: "remote status requires an FS token; create or import local credentials first"})
		return checkResult(opts.Profile, resource, &endpoint, nil, checks), nil
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "stat", "--output", "json", ":/"}, true); err != nil {
		checks = append(checks, Check{Name: "remote_status", Status: "failed", Message: apperr.MessageFor(err)})
		return checkResult(opts.Profile, resource, &endpoint, nil, checks), nil
	}
	remote := apifs.StatusResponse{Status: "reachable", TenantID: resource.FileSystemID, Kind: "ti fs"}
	checks = append(checks, Check{Name: "remote_status", Status: "passed", Message: "reachable"})
	return checkResult(opts.Profile, resource, &endpoint, &remote, checks), nil
}

func (s Service) drive9CopyFile(ctx context.Context, opts CopyFileOptions) (FileOperationResult, error) {
	args, source, target, err := drive9CopyArgs(opts)
	if err != nil {
		return FileOperationResult{}, err
	}
	if opts.CreateParents && strings.TrimSpace(opts.ToLocal) != "" {
		if err := os.MkdirAll(filepath.Dir(opts.ToLocal), 0o755); err != nil {
			return FileOperationResult{}, apperr.Wrap("fs.create_local_parent", "runtime", 1, fmt.Sprintf("create parent directories for %q", opts.ToLocal), err)
		}
	}
	var runErr error
	if opts.FromStdin || opts.ToStdout || opts.Append {
		_, runErr = s.drive9Run(ctx, opts.Profile, args, false)
	} else {
		_, runErr = s.drive9RunTransientRetry(ctx, opts.Profile, args, false)
	}
	if runErr != nil {
		return FileOperationResult{}, runErr
	}
	status := "copied"
	if opts.Append {
		status = "appended"
	}
	if opts.Resume {
		status = "resumed"
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: source, TargetPath: target, Status: status}, nil
}

func (s Service) drive9ReadFile(ctx context.Context, opts ReadFileOptions) ([]byte, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return nil, err
	}
	args := []string{"fs", "cat"}
	if opts.Range {
		args = append(args, "--offset", strconv.FormatInt(opts.Offset, 10), "--length", strconv.FormatInt(opts.Length, 10))
	}
	args = append(args, drive9Remote(remotePath))
	result, err := s.drive9RunTransientRetry(ctx, opts.Profile, args, true)
	if err != nil {
		return nil, err
	}
	return result.Stdout, nil
}

func (s Service) drive9ListFiles(ctx context.Context, opts ListFilesOptions) (ListFilesResult, error) {
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.Path))
	if err != nil {
		return ListFilesResult{}, err
	}
	result, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "ls", "-l", drive9Remote(remotePath)}, true)
	if err != nil {
		return ListFilesResult{}, err
	}
	return ListFilesResult{Path: remotePath, Entries: parseDrive9LS(result.Stdout)}, nil
}

func (s Service) drive9DescribeFile(ctx context.Context, opts DescribeFileOptions) (DescribeFileResult, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return DescribeFileResult{}, err
	}
	result, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "stat", "--output", "json", drive9Remote(remotePath)}, true)
	if err != nil {
		return DescribeFileResult{}, err
	}
	var metadata drive9StatMetadata
	if err := json.Unmarshal(result.Stdout, &metadata); err != nil {
		return DescribeFileResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs stat response", err)
	}
	mtime := int64(0)
	if metadata.Mtime != nil {
		mtime = *metadata.Mtime
	}
	return DescribeFileResult{
		Path:         remotePath,
		SizeBytes:    metadata.Size,
		IsDir:        metadata.IsDir,
		Revision:     metadata.Revision,
		Mtime:        mtime,
		Mode:         metadata.Mode,
		HasMode:      metadata.HasMode || metadata.Mode != 0,
		ResourceID:   metadata.ResourceID,
		Nlink:        metadata.Nlink,
		ContentType:  metadata.ContentType,
		SemanticText: metadata.SemanticText,
		Tags:         metadata.Tags,
		Degraded:     metadata.Degraded,
	}, nil
}

func (s Service) drive9MoveFile(ctx context.Context, opts MoveFileOptions) (FileOperationResult, error) {
	source, err := normalizeRemotePath(opts.FromRemote)
	if err != nil {
		return FileOperationResult{}, err
	}
	target, err := normalizeRemotePath(opts.ToRemote)
	if err != nil {
		return FileOperationResult{}, err
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "mv", drive9Remote(source), drive9Remote(target)}, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "move_file", SourcePath: source, TargetPath: target, Status: "moved"}, nil
}

func (s Service) drive9DeleteFile(ctx context.Context, opts DeleteFileOptions) (FileOperationResult, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return FileOperationResult{}, err
	}
	args := []string{"fs", "rm"}
	if opts.Recursive {
		args = append(args, "--recursive")
	}
	args = append(args, drive9Remote(remotePath))
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "delete_file", TargetPath: remotePath, Status: "deleted"}, nil
}

func (s Service) drive9CreateDirectory(ctx context.Context, opts CreateDirectoryOptions) (FileOperationResult, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return FileOperationResult{}, err
	}
	if strings.TrimSpace(opts.Mode) != "" {
		if _, err := parseMode(opts.Mode); err != nil {
			return FileOperationResult{}, err
		}
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "mkdir", drive9Remote(remotePath)}, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "create_directory", TargetPath: remotePath, Status: "created"}, nil
}

func (s Service) drive9ChmodFile(ctx context.Context, opts ChmodFileOptions) (FileOperationResult, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return FileOperationResult{}, err
	}
	if _, err := parseRequiredMode(opts.Mode, "--mode"); err != nil {
		return FileOperationResult{}, err
	}
	if _, err := s.drive9RunTransientRetry(ctx, opts.Profile, []string{"fs", "chmod", opts.Mode, drive9Remote(remotePath)}, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "chmod_file", TargetPath: remotePath, Status: "updated"}, nil
}

func (s Service) drive9SymlinkFile(ctx context.Context, opts SymlinkFileOptions) (FileOperationResult, error) {
	if strings.TrimSpace(opts.Target) == "" {
		return FileOperationResult{}, apperr.New("fs.missing_symlink_target", "usage", 2, "--target is required")
	}
	link, err := normalizeRemotePath(opts.Link)
	if err != nil {
		return FileOperationResult{}, err
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "symlink", opts.Target, drive9Remote(link)}, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "create_symlink", SourcePath: opts.Target, TargetPath: link, Status: "created"}, nil
}

func (s Service) drive9HardlinkFile(ctx context.Context, opts HardlinkFileOptions) (FileOperationResult, error) {
	source, err := normalizeRemotePath(opts.Source)
	if err != nil {
		return FileOperationResult{}, err
	}
	link, err := normalizeRemotePath(opts.Link)
	if err != nil {
		return FileOperationResult{}, err
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "hardlink", drive9Remote(source), drive9Remote(link)}, false); err != nil {
		return FileOperationResult{}, err
	}
	return FileOperationResult{Operation: "create_hardlink", SourcePath: source, TargetPath: link, Status: "created"}, nil
}

func (s Service) drive9SearchFileContent(ctx context.Context, opts SearchFileContentOptions) (SearchFilesResult, error) {
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.Path))
	if err != nil {
		return SearchFilesResult{}, err
	}
	if strings.TrimSpace(opts.Pattern) == "" {
		return SearchFilesResult{}, apperr.New("fs.missing_pattern", "usage", 2, "--pattern is required")
	}
	args := []string{"fs", "grep"}
	if strings.TrimSpace(opts.LayerID) != "" {
		args = append(args, "--layer", strings.TrimSpace(opts.LayerID))
	}
	args = append(args, opts.Pattern, drive9Remote(remotePath))
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return SearchFilesResult{}, err
	}
	return SearchFilesResult{Path: remotePath, Results: parseDrive9Paths(result.Stdout, opts.Limit)}, nil
}

func (s Service) drive9FindFiles(ctx context.Context, opts FindFilesOptions) (SearchFilesResult, error) {
	if strings.TrimSpace(opts.ResourceType) != "" {
		return SearchFilesResult{}, apperr.New("fs.unsupported_find_flag", "usage", 2, "--resource-type is not supported by the Drive9 companion CLI")
	}
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.Path))
	if err != nil {
		return SearchFilesResult{}, err
	}
	args := []string{"fs", "find", drive9Remote(remotePath)}
	if opts.FileNamePattern != "" {
		args = append(args, "-name", opts.FileNamePattern)
	}
	if opts.Tag != "" {
		args = append(args, "-tag", opts.Tag)
	}
	if opts.LayerID != "" {
		args = append(args, "--layer", opts.LayerID)
	}
	if opts.Newer != "" {
		args = append(args, "-newer", opts.Newer)
	}
	if opts.Older != "" {
		args = append(args, "-older", opts.Older)
	}
	if opts.MinSizeBytes > 0 {
		args = append(args, "-size", "+"+strconv.FormatInt(opts.MinSizeBytes, 10))
	}
	if opts.MaxSizeBytes > 0 {
		args = append(args, "-size", "-"+strconv.FormatInt(opts.MaxSizeBytes, 10))
	}
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return SearchFilesResult{}, err
	}
	return SearchFilesResult{Path: remotePath, Results: parseDrive9Paths(result.Stdout, opts.Limit)}, nil
}

func (s Service) drive9CreateLayer(ctx context.Context, opts CreateLayerOptions) (LayerResult, error) {
	baseRoot, err := normalizeRemotePath(opts.BaseRootPath)
	if err != nil {
		return LayerResult{}, err
	}
	args := []string{"fs", "layer", "create", "--json"}
	appendFlagValue(&args, "--id", opts.LayerID)
	appendFlagValue(&args, "--name", opts.LayerName)
	appendFlagValue(&args, "--durability", opts.DurabilityMode)
	appendFlagValue(&args, "--actor", opts.ActorID)
	for _, tag := range opts.Tags {
		appendFlagValue(&args, "--tag", tag)
	}
	args = append(args, drive9Remote(baseRoot))
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return LayerResult{}, err
	}
	var layer apifs.FSLayer
	if err := json.Unmarshal(result.Stdout, &layer); err != nil {
		return LayerResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs layer response", err)
	}
	return LayerResult{FSLayer: layer}, nil
}

func (s Service) drive9ListLayers(ctx context.Context, opts ListLayersOptions) (LayerListResult, error) {
	result, err := s.drive9RunTransientRetry(ctx, opts.Profile, []string{"fs", "layer", "list", "--json"}, true)
	if err != nil {
		return LayerListResult{}, err
	}
	var out LayerListResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return LayerListResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs layer list response", err)
	}
	return out, nil
}

func (s Service) drive9DescribeLayer(ctx context.Context, opts DescribeLayerOptions) (LayerResult, error) {
	result, err := s.drive9RunTransientRetry(ctx, opts.Profile, []string{"fs", "layer", "status", "--json", strings.TrimSpace(opts.LayerID)}, true)
	if err != nil {
		return LayerResult{}, err
	}
	var layer apifs.FSLayer
	if err := json.Unmarshal(result.Stdout, &layer); err != nil {
		return LayerResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs layer response", err)
	}
	return LayerResult{FSLayer: layer}, nil
}

func (s Service) drive9DiffLayer(ctx context.Context, opts LayerEntriesOptions) (LayerEntriesResult, error) {
	layerID := strings.TrimSpace(opts.LayerID)
	result, err := s.drive9RunTransientRetry(ctx, opts.Profile, []string{"fs", "layer", "diff", "--json", layerID}, true)
	if err != nil {
		return LayerEntriesResult{}, err
	}
	var out LayerEntriesResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return LayerEntriesResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs layer diff response", err)
	}
	out.LayerID = layerID
	if opts.MaxSeq > 0 {
		filtered := out.Entries[:0]
		for _, entry := range out.Entries {
			if entry.EntrySeq <= opts.MaxSeq {
				filtered = append(filtered, entry)
			}
		}
		out.Entries = filtered
	}
	return out, nil
}

func (s Service) drive9CreateLayerCheckpoint(ctx context.Context, opts CreateLayerCheckpointOptions) (LayerCheckpointResult, error) {
	args := []string{"fs", "layer", "checkpoint", "--json"}
	appendFlagValue(&args, "--id", opts.CheckpointID)
	appendFlagValue(&args, "--label", opts.Label)
	args = append(args, strings.TrimSpace(opts.LayerID))
	result, err := s.drive9RunTransientRetry(ctx, opts.Profile, args, true)
	if err != nil {
		return LayerCheckpointResult{}, err
	}
	var checkpoint apifs.FSLayerCheckpoint
	if err := json.Unmarshal(result.Stdout, &checkpoint); err != nil {
		return LayerCheckpointResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs layer checkpoint response", err)
	}
	return LayerCheckpointResult{FSLayerCheckpoint: checkpoint}, nil
}

func (s Service) drive9RollbackLayer(ctx context.Context, opts LayerActionOptions) (LayerActionResult, error) {
	layerID := strings.TrimSpace(opts.LayerID)
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "layer", "rollback", layerID}, true); err != nil {
		return LayerActionResult{}, err
	}
	return LayerActionResult{Operation: "rollback_layer", LayerID: layerID, Status: "rolled_back"}, nil
}

func (s Service) drive9CommitLayer(ctx context.Context, opts LayerActionOptions) (LayerCommitResult, error) {
	layerID := strings.TrimSpace(opts.LayerID)
	result, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "layer", "commit", layerID}, true)
	if err != nil {
		return LayerCommitResult{}, err
	}
	return LayerCommitResult{FSLayerCommit: parseDrive9LayerCommit(layerID, result.Stdout)}, nil
}

func (s Service) drive9GitCloneWorkspace(ctx context.Context, opts GitWorkspaceCloneOptions) (GitWorkspaceCloneResult, error) {
	args := []string{"git", "clone", "--fast"}
	if opts.Blobless {
		args = append(args, "--blobless")
	}
	appendFlagValue(&args, "--hydrate", opts.HydrateMode)
	args = append(args, opts.RepoURL, opts.TargetPath)
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return GitWorkspaceCloneResult{}, err
	}
	return GitWorkspaceCloneResult{Operation: "clone_git_workspace", TargetPath: opts.TargetPath}, nil
}

func (s Service) drive9GitHydrateWorkspace(ctx context.Context, opts GitWorkspaceHydrateOptions) (GitHydrateResult, error) {
	args := []string{"git", "hydrate"}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}
	args = append(args, opts.TargetPath)
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return GitHydrateResult{}, err
	}
	return GitHydrateResult{Operation: "hydrate_git_workspace", TargetPath: opts.TargetPath}, nil
}

func (s Service) drive9GitAddWorktree(ctx context.Context, opts GitWorktreeAddOptions) (GitWorkspaceCloneResult, error) {
	args := []string{"git", "worktree", "add", "--fast"}
	if opts.BranchName != "" {
		args = append(args, "-b", opts.BranchName)
	}
	if opts.Detach {
		args = append(args, "--detach")
	}
	if opts.Blobless {
		args = append(args, "--blobless")
	}
	appendFlagValue(&args, "--hydrate", opts.HydrateMode)
	args = append(args, opts.BasePath, opts.WorktreePath)
	if opts.CommitISH != "" {
		args = append(args, opts.CommitISH)
	}
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return GitWorkspaceCloneResult{}, err
	}
	return GitWorkspaceCloneResult{Operation: "add_git_worktree", TargetPath: opts.WorktreePath}, nil
}

func (s Service) drive9GitRemoveWorktree(ctx context.Context, opts GitWorktreeRemoveOptions) (GitWorktreeRemoveResult, error) {
	args := []string{"git", "worktree", "remove", "--fast"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, opts.WorktreePath)
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return GitWorktreeRemoveResult{}, err
	}
	return GitWorktreeRemoveResult{Operation: "remove_git_worktree", RemotePath: opts.WorktreePath, Status: "removed"}, nil
}

func (s Service) drive9CreateJournal(ctx context.Context, opts JournalCreateOptions) (JournalResult, error) {
	args := []string{"journal", "new", "--json"}
	appendFlagValue(&args, "--id", opts.JournalID)
	appendFlagValue(&args, "--kind", opts.JournalKind)
	appendFlagValue(&args, "--title", opts.Title)
	if opts.Actor != "" {
		appendFlagValue(&args, "--meta", "actor="+opts.Actor)
	}
	for _, label := range opts.Labels {
		appendFlagValue(&args, "--meta", label)
	}
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return JournalResult{}, err
	}
	var journal apifs.Journal
	if err := json.Unmarshal(result.Stdout, &journal); err != nil {
		return JournalResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs journal response", err)
	}
	return JournalResult(journal), nil
}

func (s Service) drive9AppendJournalEntries(ctx context.Context, opts JournalAppendOptions) (JournalAppendResult, error) {
	journalID, err := requireJournalID(opts.JournalID)
	if err != nil {
		return JournalAppendResult{}, err
	}
	args := []string{"journal", "append"}
	appendFlagValue(&args, "--idempotency-key", opts.IdempotencyKey)
	appendFlagValue(&args, "--type", opts.EntryType)
	appendFlagValue(&args, "--source", opts.Source)
	for _, subject := range opts.Subjects {
		appendFlagValue(&args, "--subject", subject)
	}
	if opts.JSONArray {
		args = append(args, "--json-array")
	}
	args = append(args, journalID)
	stdin, err := journalAppendStdin(opts)
	if err != nil {
		return JournalAppendResult{}, err
	}
	runner := s.drive9Runner()
	runner.Stdin = bytes.NewReader(stdin)
	result, err := runner.Run(ctx, fswrap.RunOptions{
		Profile:         opts.Profile,
		Args:            args,
		CaptureStdout:   true,
		IncludeFSAPIKey: true,
	})
	if err != nil {
		return JournalAppendResult{}, err
	}
	var out apifs.JournalAppendResponse
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return JournalAppendResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs journal append response", err)
	}
	return JournalAppendResult(out), nil
}

func (s Service) drive9ReadJournalEntries(ctx context.Context, opts JournalReadOptions) (JournalEntriesResult, error) {
	journalID, err := requireJournalID(opts.JournalID)
	if err != nil {
		return JournalEntriesResult{}, err
	}
	args := []string{"journal", "cat"}
	if opts.AfterSeq > 0 {
		args = append(args, "--after", strconv.FormatInt(opts.AfterSeq, 10))
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	args = append(args, journalID)
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return JournalEntriesResult{}, err
	}
	var entries []apifs.JournalEntry
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry apifs.JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return JournalEntriesResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs journal entry", err)
		}
		entries = append(entries, entry)
	}
	if entries == nil {
		entries = []apifs.JournalEntry{}
	}
	return JournalEntriesResult{Entries: entries}, nil
}

func (s Service) drive9SearchJournal(ctx context.Context, opts JournalSearchOptions) (JournalSearchResult, error) {
	args := []string{"journal", "find", "--json"}
	appendFlagValue(&args, "--type", opts.EntryType)
	appendFlagValue(&args, "--kind", opts.JournalKind)
	appendFlagValue(&args, "--actor", opts.Actor)
	appendFlagValue(&args, "--status", opts.Status)
	appendFlagValue(&args, "--since", opts.Since)
	appendFlagValue(&args, "--until", opts.Until)
	appendFlagValue(&args, "--cursor", opts.Cursor)
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	if opts.IncludeEntries {
		args = append(args, "--entries")
	}
	for _, subject := range opts.Subjects {
		appendFlagValue(&args, "--subject", subject)
	}
	for _, label := range opts.Labels {
		appendFlagValue(&args, "--meta", label)
	}
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return JournalSearchResult{}, err
	}
	matches := []apifs.JournalSearchMatch{}
	for _, line := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var match apifs.JournalSearchMatch
		if err := json.Unmarshal([]byte(line), &match); err != nil {
			return JournalSearchResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs journal search response", err)
		}
		matches = append(matches, match)
	}
	return JournalSearchResult{Matches: matches}, nil
}

func (s Service) drive9VerifyJournal(ctx context.Context, opts JournalVerifyOptions) (JournalVerifyResult, error) {
	journalID, err := requireJournalID(opts.JournalID)
	if err != nil {
		return JournalVerifyResult{}, err
	}
	result, err := s.drive9Run(ctx, opts.Profile, []string{"journal", "verify", "--json", journalID}, true)
	if err != nil {
		return JournalVerifyResult{}, err
	}
	var out apifs.JournalVerifyResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return JournalVerifyResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs journal verify response", err)
	}
	return JournalVerifyResult(out), nil
}

func (s Service) drive9CreateVaultSecret(ctx context.Context, opts VaultCreateSecretOptions) (VaultSecretResult, error) {
	name, err := validateVaultSecretName(opts.SecretName)
	if err != nil {
		return VaultSecretResult{}, err
	}
	args := []string{"vault", "set", name}
	args = append(args, opts.Fields...)
	runner := s.drive9Runner()
	if opts.Stdin != nil {
		runner.Stdin = opts.Stdin
	}
	if _, err := runner.Run(ctx, fswrap.RunOptions{Profile: opts.Profile, Args: args, IncludeFSAPIKey: true}); err != nil {
		return VaultSecretResult{}, err
	}
	return VaultSecretResult{Secret: apifs.VaultSecret{Name: name}, Status: "created"}, nil
}

func (s Service) drive9ReplaceVaultSecret(ctx context.Context, opts VaultReplaceSecretOptions) (VaultSecretResult, error) {
	if strings.TrimSpace(opts.FromDirectory) == "" {
		return VaultSecretResult{}, apperr.New("vault.missing_from_directory", "usage", 2, "--from-directory is required")
	}
	name, err := parseVaultPath(opts.SecretPath)
	if err != nil {
		return VaultSecretResult{}, err
	}
	fields, err := drive9VaultSetFieldsFromDirectory(opts.FromDirectory)
	if err != nil {
		return VaultSecretResult{}, err
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"vault", "rm", name}, false); err != nil && !isDrive9NotFound(err) {
		return VaultSecretResult{}, err
	}
	args := append([]string{"vault", "set", name}, fields...)
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return VaultSecretResult{}, err
	}
	return VaultSecretResult{Secret: apifs.VaultSecret{Name: name}, Status: "replaced"}, nil
}

func (s Service) drive9ReadVaultSecret(ctx context.Context, opts VaultReadSecretOptions) (any, error) {
	name, err := validateVaultSecretName(opts.SecretName)
	if err != nil {
		return nil, err
	}
	ref := name
	if opts.Field != "" {
		ref += "/" + opts.Field
	}
	format := strings.TrimSpace(opts.Format)
	if format == "" {
		format = "json"
	}
	args := []string{"vault", "get", ref}
	switch format {
	case "json":
		args = append(args, "--json")
	case "env":
		args = append(args, "--env")
	case "raw":
	default:
		return nil, apperr.New("vault.invalid_format", "usage", 2, "--format must be json, raw, or env")
	}
	result, err := s.drive9Runner().Run(ctx, fswrap.RunOptions{Profile: opts.Profile, Args: args, CaptureStdout: true, IncludeFSAPIKey: true, VaultToken: opts.VaultToken})
	if err != nil {
		return nil, err
	}
	if format == "raw" {
		return bytes.TrimSuffix(result.Stdout, []byte("\n")), nil
	}
	if format == "env" {
		return result.Stdout, nil
	}
	if opts.Field != "" {
		var fields map[string]string
		if err := json.Unmarshal(result.Stdout, &fields); err != nil {
			return nil, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs-vault secret", err)
		}
		return VaultReadSecretResult{SecretName: name, Field: opts.Field, Value: fields[opts.Field]}, nil
	}
	var fields map[string]string
	if err := json.Unmarshal(result.Stdout, &fields); err != nil {
		return nil, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs-vault secret", err)
	}
	return VaultReadSecretResult{SecretName: name, Fields: fields}, nil
}

func (s Service) drive9ListVaultSecrets(ctx context.Context, opts VaultListSecretsOptions) (VaultListSecretsResult, error) {
	result, err := s.drive9Runner().Run(ctx, fswrap.RunOptions{Profile: opts.Profile, Args: []string{"vault", "ls", "--json"}, CaptureStdout: true, IncludeFSAPIKey: true, VaultToken: opts.VaultToken})
	if err != nil {
		return VaultListSecretsResult{}, err
	}
	var out VaultListSecretsResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return VaultListSecretsResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs-vault list response", err)
	}
	return out, nil
}

func (s Service) drive9DeleteVaultSecret(ctx context.Context, opts VaultDeleteSecretOptions) (VaultDeleteResult, error) {
	name, err := validateVaultSecretName(opts.SecretName)
	if err != nil {
		return VaultDeleteResult{}, err
	}
	if err := s.drive9RunIdempotentDelete(ctx, opts.Profile, []string{"vault", "rm", name}); err != nil {
		return VaultDeleteResult{}, err
	}
	return VaultDeleteResult{Operation: "delete_vault_secret", ID: name, Status: "deleted"}, nil
}

func (s Service) drive9CreateVaultGrant(ctx context.Context, opts VaultCreateGrantOptions) (VaultTokenResult, error) {
	if opts.AgentID == "" {
		return VaultTokenResult{}, apperr.New("vault.missing_agent_id", "usage", 2, "--agent-id is required")
	}
	if opts.Permission != "read" && opts.Permission != "write" {
		return VaultTokenResult{}, apperr.New("vault.invalid_permission", "usage", 2, "--permission must be read or write")
	}
	if opts.TTL <= 0 {
		return VaultTokenResult{}, apperr.New("vault.invalid_ttl", "usage", 2, "--ttl must be positive")
	}
	scopes, err := drive9VaultGrantScopes(opts.Scopes)
	if err != nil {
		return VaultTokenResult{}, err
	}
	args := []string{"vault", "grant", "--json", "--agent", opts.AgentID, "--ttl", opts.TTL.String(), "--perm", opts.Permission}
	args = append(args, scopes...)
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return VaultTokenResult{}, err
	}
	var out VaultTokenResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return VaultTokenResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs-vault grant response", err)
	}
	return out, nil
}

func (s Service) drive9DeleteVaultGrant(ctx context.Context, opts VaultDeleteGrantOptions) (VaultDeleteResult, error) {
	grantID := strings.TrimSpace(opts.GrantID)
	if grantID == "" {
		return VaultDeleteResult{}, apperr.New("vault.missing_grant_id", "usage", 2, "--grant-id is required")
	}
	if err := s.drive9RunIdempotentDelete(ctx, opts.Profile, []string{"vault", "revoke", grantID}); err != nil {
		return VaultDeleteResult{}, err
	}
	return VaultDeleteResult{Operation: "delete_vault_grant", ID: grantID, Status: "deleted"}, nil
}

func (s Service) drive9ListVaultAuditEvents(ctx context.Context, opts VaultAuditOptions) (VaultAuditResult, error) {
	args := []string{"vault", "audit", "--json"}
	appendFlagValue(&args, "--secret", opts.SecretName)
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}
	result, err := s.drive9Run(ctx, opts.Profile, args, true)
	if err != nil {
		return VaultAuditResult{}, err
	}
	var out VaultAuditResult
	if err := json.Unmarshal(result.Stdout, &out); err != nil {
		return VaultAuditResult{}, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs-vault audit response", err)
	}
	return out, nil
}

func (s Service) drive9RunWithVaultSecret(ctx context.Context, opts VaultRunWithSecretOptions) error {
	if len(opts.Command) == 0 {
		return apperr.New("vault.missing_command", "usage", 2, "run-with-secret requires `--` followed by a command")
	}
	args := append([]string{"vault", "with", opts.SecretPath, "--"}, opts.Command...)
	runner := s.drive9Runner()
	runner.Stdin = opts.Stdin
	runner.Stdout = opts.Stdout
	runner.Stderr = opts.Stderr
	_, err := runner.Run(ctx, fswrap.RunOptions{Profile: opts.Profile, Args: args, IncludeFSAPIKey: true, VaultToken: opts.VaultToken})
	return err
}

func (s Service) drive9MountVault(ctx context.Context, opts VaultMountOptions) (MountResult, error) {
	if strings.TrimSpace(opts.VaultToken) == "" {
		return MountResult{}, apperr.New("vault.missing_token", "usage", 2, "ti fs-vault mount-vault requires --vault-token or TI_VAULT_TOKEN; use create-grant to mint a delegated vault token first")
	}
	args := []string{"mount", "vault"}
	if opts.Foreground {
		args = append(args, "--foreground")
	}
	args = append(args, opts.MountPath)
	if _, err := s.drive9Runner().Run(ctx, fswrap.RunOptions{Profile: opts.Profile, Args: args, IncludeFSAPIKey: true, VaultToken: opts.VaultToken}); err != nil {
		return MountResult{}, err
	}
	if !opts.Foreground {
		if err := s.writeDrive9MountLocator(opts.Profile, opts.MountPath, "vault"); err != nil {
			_, _ = s.drive9Run(ctx, opts.Profile, []string{"umount", opts.MountPath}, false)
			return MountResult{}, err
		}
	}
	return MountResult{Status: "mounted", Profile: profileName(opts.Profile), FileSystemName: "vault", MountPath: opts.MountPath, RemotePath: "/n/vault", Driver: "fuse"}, nil
}

func (s Service) drive9PackFileSystem(ctx context.Context, opts PackFileSystemOptions) (PackFileSystemResult, error) {
	args := []string{"pack"}
	appendFlagValue(&args, "--local-root", opts.LocalRoot)
	appendFlagValue(&args, "--remote-root", opts.RemoteRoot)
	appendFlagValue(&args, "--mount", opts.MountPath)
	appendFlagValue(&args, "--profile", opts.MountProfile)
	archivePath := strings.TrimSpace(opts.ArchivePath)
	if archivePath != "" {
		normalized, err := normalizeRemotePath(archivePath)
		if err != nil {
			return PackFileSystemResult{}, err
		}
		args = append(args, drive9Remote(normalized))
	}
	args = append(args, opts.Paths...)
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return PackFileSystemResult{}, err
	}
	return PackFileSystemResult{Status: "packed", ArchivePath: archivePath, LocalRoot: opts.LocalRoot, RemoteRoot: opts.RemoteRoot, MountProfile: opts.MountProfile, Paths: opts.Paths, CreatedAt: time.Now().UTC()}, nil
}

func (s Service) drive9UnpackFileSystem(ctx context.Context, opts UnpackFileSystemOptions) (UnpackFileSystemResult, error) {
	args := []string{"unpack"}
	appendFlagValue(&args, "--local-root", opts.LocalRoot)
	appendFlagValue(&args, "--remote-root", opts.RemoteRoot)
	appendFlagValue(&args, "--mount", opts.MountPath)
	appendFlagValue(&args, "--profile", opts.MountProfile)
	if opts.NoReplace {
		args = append(args, "--no-replace")
	}
	archivePath := strings.TrimSpace(opts.ArchivePath)
	if archivePath != "" {
		normalized, err := normalizeRemotePath(archivePath)
		if err != nil {
			return UnpackFileSystemResult{}, err
		}
		args = append(args, drive9Remote(normalized))
	}
	if _, err := s.drive9Run(ctx, opts.Profile, args, false); err != nil {
		return UnpackFileSystemResult{}, err
	}
	return UnpackFileSystemResult{Status: "unpacked", ArchivePath: archivePath, LocalRoot: opts.LocalRoot, RemoteRoot: opts.RemoteRoot, MountProfile: opts.MountProfile, Replaced: !opts.NoReplace, CreatedAt: time.Now().UTC()}, nil
}

func (s Service) drive9MountFileSystem(ctx context.Context, opts MountFileSystemOptions) (MountResult, error) {
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.RemotePath))
	if err != nil {
		return MountResult{}, err
	}
	args := []string{"mount"}
	if opts.Driver != "" {
		args = append(args, "--mode", opts.Driver)
	}
	if opts.Foreground {
		args = append(args, "--foreground")
	}
	if opts.ReadOnly {
		args = append(args, "--read-only")
	}
	fuseRequested := strings.TrimSpace(opts.Driver) == "fuse"
	if fuseRequested {
		appendFlagValue(&args, "--cache-dir", opts.CacheDir)
		if opts.ReadCacheMB > 0 {
			args = append(args, "--cache-size", strconv.FormatInt(opts.ReadCacheMB, 10))
		}
		if opts.ReadCacheFileMB > 0 {
			args = append(args, "--read-cache-max-file-mb", strconv.FormatInt(opts.ReadCacheFileMB, 10))
		}
		if opts.ReadCacheTTL > 0 {
			args = append(args, "--read-cache-ttl", opts.ReadCacheTTL.String())
		}
		if opts.WriteBackCache {
			args = append(args, "--durability", "interactive")
		}
	}
	appendFlagValue(&args, "--profile", opts.MountProfile)
	appendFlagValue(&args, "--local-root", opts.LocalRoot)
	if opts.UnpackArchivePath != "" {
		args = append(args, "--unpack", drive9RemoteMust(opts.UnpackArchivePath))
	}
	if opts.NoAutoUnpack {
		args = append(args, "--no-auto-unpack")
	}
	args = append(args, drive9Remote(remotePath), opts.MountPath)
	result, err := s.drive9Run(ctx, opts.Profile, args, !opts.Foreground)
	if err != nil {
		return MountResult{}, err
	}
	if !opts.Foreground {
		if err := s.writeDrive9MountLocator(opts.Profile, opts.MountPath, "fs"); err != nil {
			_, _ = s.drive9Run(ctx, opts.Profile, []string{"umount", opts.MountPath}, false)
			return MountResult{}, err
		}
	}
	endpoint, _ := s.resolveFS(opts.Profile)
	driver := drive9MountedDriver(result.Stderr, opts.Driver)
	if driver == "" {
		driver = "auto"
	}
	return MountResult{Status: "mounted", Profile: profileName(opts.Profile), FileSystemName: opts.Profile.FSResourceName, MountPath: opts.MountPath, RemotePath: remotePath, Driver: driver, Endpoint: &endpoint, MountProfile: opts.MountProfile, LocalRoot: opts.LocalRoot, PackPaths: opts.PackPaths, WriteBackCache: opts.WriteBackCache}, nil
}

func (s Service) drive9DrainFileSystem(ctx context.Context, opts DrainFileSystemOptions) (DrainResult, error) {
	profile, err := s.drive9MountLocatorProfile(opts.Profile, opts.MountPath)
	if err != nil {
		return DrainResult{}, err
	}
	args := []string{"mount", "drain", "--json"}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}
	args = append(args, opts.MountPath)
	if _, err := s.drive9Run(ctx, profile, args, true); err != nil {
		return DrainResult{}, err
	}
	return DrainResult{Status: "drained", MountPath: opts.MountPath}, nil
}

func (s Service) drive9UnmountFileSystem(ctx context.Context, opts UnmountFileSystemOptions) (UnmountResult, error) {
	profile, err := s.drive9MountLocatorProfile(opts.Profile, opts.MountPath)
	if err != nil {
		if opts.IgnoreAbsent && apperr.CodeFor(err) == "fs.mount_locator_not_found" {
			return UnmountResult{Status: "absent", MountPath: opts.MountPath}, nil
		}
		return UnmountResult{}, err
	}
	args := []string{"umount"}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}
	if opts.PackArchivePath != "" {
		args = append(args, "--pack", drive9RemoteMust(opts.PackArchivePath))
	}
	if opts.NoAutoPack {
		args = append(args, "--no-auto-pack")
	}
	args = append(args, opts.MountPath)
	if _, err := s.drive9Run(ctx, profile, args, false); err != nil {
		if opts.IgnoreAbsent {
			homeDir, homeErr := s.homeDir()
			if homeErr != nil {
				return UnmountResult{}, homeErr
			}
			if removeErr := mountlocator.Remove(homeDir, opts.MountPath); removeErr != nil {
				return UnmountResult{}, apperr.Wrap("fs.remove_mount_locator", "runtime", 1, "remove stale ti fs mount locator", removeErr)
			}
			return UnmountResult{Status: "absent", MountPath: opts.MountPath}, nil
		}
		return UnmountResult{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return UnmountResult{}, err
	}
	if err := mountlocator.Remove(homeDir, opts.MountPath); err != nil {
		return UnmountResult{}, apperr.Wrap("fs.remove_mount_locator", "runtime", 1, "remove ti fs mount locator", err)
	}
	return UnmountResult{Status: "unmounted", MountPath: opts.MountPath}, nil
}

func (s Service) writeDrive9MountLocator(profile *config.Profile, mountPath, kind string) error {
	if profile == nil {
		return apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return err
	}
	companionHome, err := fscred.CompanionHome(homeDir, profile.Name, profile.FSResourceName)
	if err != nil {
		return err
	}
	locator, err := mountlocator.New(profile.Name, profile.FSResourceName, profile.FSPlacementRegionCode, companionHome, mountPath, kind)
	if err != nil {
		return apperr.Wrap("fs.write_mount_locator", "runtime", 1, "construct ti fs mount locator", err)
	}
	locator = locator.WithTokenCorrelation(profile.FSTenantID, profile.FSTokenID, fsTokenFingerprint(profile.FSAPIKey))
	if _, err := mountlocator.Write(homeDir, locator); err != nil {
		return apperr.Wrap("fs.write_mount_locator", "runtime", 1, "write ti fs mount locator", err)
	}
	return nil
}

func (s Service) drive9MountLocatorProfile(base *config.Profile, mountPath string) (*config.Profile, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return nil, err
	}
	locator, _, err := mountlocator.Read(homeDir, mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, apperr.New("fs.mount_locator_not_found", "runtime", 1, fmt.Sprintf("no ti fs mount locator found for %q", mountPath))
		}
		return nil, apperr.Wrap("fs.read_mount_locator", "runtime", 1, fmt.Sprintf("read ti fs mount locator for %q", mountPath), err)
	}
	expectedHome, err := fscred.CompanionHome(homeDir, locator.Profile, locator.FileSystemName)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(expectedHome) != filepath.Clean(locator.CompanionHome) {
		return nil, apperr.New("fs.mount_locator_invalid", "config", 2, "ti fs mount locator companion home does not match its profile and file system")
	}
	placement, err := region.ParsePlacementCode(locator.RegionCode)
	if err != nil {
		return nil, apperr.Wrap("fs.mount_locator_invalid", "config", 2, "ti fs mount locator has an invalid region", err)
	}
	profile := config.Profile{HomeDir: homeDir}
	if base != nil {
		profile = *base
	}
	profile.Name = locator.Profile
	profile.HomeDir = homeDir
	profile.FSResourceName = locator.FileSystemName
	profile.FSTenantID = locator.FileSystemName
	profile.FSPlacementRegionCode = placement.Code
	profile.FSCloudProvider = placement.Provider
	profile.FSRegionCode = placement.NativeCode
	profile.FSAPIKey = ""
	return &profile, nil
}

func drive9CopyArgs(opts CopyFileOptions) ([]string, string, string, error) {
	args := []string{"fs", "cp"}
	if opts.Resume {
		args = append(args, "--resume")
	}
	if opts.Append {
		args = append(args, "--append")
	}
	if opts.Recursive {
		args = append(args, "--recursive")
	}
	appendFlagValue(&args, "--layer", opts.LayerID)
	for _, key := range sortedKeys(opts.Tags) {
		args = append(args, "--tag", key+"="+opts.Tags[key])
	}
	appendFlagValue(&args, "--description", opts.Description)
	source, target := "", ""
	switch {
	case opts.FromStdin && opts.ToRemote != "":
		targetPath, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return nil, "", "", err
		}
		source, target = "-", targetPath
		args = append(args, "-", drive9Remote(targetPath))
	case opts.FromLocal != "" && opts.ToRemote != "":
		targetPath, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return nil, "", "", err
		}
		source, target = opts.FromLocal, targetPath
		args = append(args, opts.FromLocal, drive9Remote(targetPath))
	case opts.FromRemote != "" && opts.ToStdout:
		sourcePath, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return nil, "", "", err
		}
		source, target = sourcePath, "-"
		args = append(args, drive9Remote(sourcePath), "-")
	case opts.FromRemote != "" && opts.ToLocal != "":
		sourcePath, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return nil, "", "", err
		}
		source, target = sourcePath, opts.ToLocal
		args = append(args, drive9Remote(sourcePath), opts.ToLocal)
	case opts.FromRemote != "" && opts.ToRemote != "":
		sourcePath, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return nil, "", "", err
		}
		targetPath, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return nil, "", "", err
		}
		source, target = sourcePath, targetPath
		args = append(args, drive9Remote(sourcePath), drive9Remote(targetPath))
	default:
		return nil, "", "", apperr.New("fs.invalid_copy_flags", "usage", 2, "copy-file requires exactly one source/target pair")
	}
	return args, source, target, nil
}

func drive9Remote(remotePath string) string {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "-" || strings.HasPrefix(remotePath, ":") {
		return remotePath
	}
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	return ":" + remotePath
}

func drive9RemoteMust(remotePath string) string {
	normalized, err := normalizeRemotePath(remotePath)
	if err != nil {
		return drive9Remote(remotePath)
	}
	return drive9Remote(normalized)
}

func drive9MountedDriver(stderr []byte, fallback string) string {
	text := string(stderr)
	switch {
	case strings.Contains(text, "mount mode: fuse"):
		return "fuse"
	case strings.Contains(text, "mount mode: webdav"):
		return "webdav"
	default:
		return strings.TrimSpace(fallback)
	}
}

func drive9VaultSetFieldsFromDirectory(dir string) ([]string, error) {
	fields, err := readVaultFieldsDirectory(dir)
	if err != nil {
		return nil, err
	}
	keys := sortedKeys(fields)
	args := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.Contains(key, "=") {
			return nil, apperr.New("vault.invalid_field", "usage", 2, fmt.Sprintf("field file name %q must not contain '='", key))
		}
		args = append(args, key+"=@"+filepath.Join(dir, key))
	}
	return args, nil
}

func drive9VaultGrantScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, apperr.New("vault.missing_scope", "usage", 2, "at least one --scope is required")
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return nil, apperr.New("vault.missing_scope", "usage", 2, "--scope cannot be empty")
		}
		if strings.HasPrefix(scope, "/n/vault/") {
			scope = strings.TrimPrefix(scope, "/n/vault/")
			if scope == "" {
				return nil, apperr.New("vault.invalid_scope", "usage", 2, "--scope must include a secret name")
			}
			out = append(out, scope)
			continue
		}
		if strings.HasPrefix(scope, "/") {
			return nil, apperr.New("vault.invalid_scope", "usage", 2, "--scope must be <secret>[/<field>] or /n/vault/<secret>[/<field>]")
		}
		out = append(out, scope)
	}
	return out, nil
}

func isDrive9NotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func remoteFileSystemNotFound(fileSystemID string, cause error) error {
	return apperr.Wrap("fs.resource_not_found", "runtime", 1, fmt.Sprintf("file system %q was not found in the selected region", fileSystemID), cause)
}

func isTransientDrive9Error(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, " eof") ||
		strings.Contains(message, ": eof") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "tls handshake timeout") ||
		strings.Contains(message, "i/o timeout") ||
		strings.Contains(message, "timeout awaiting response headers") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "server closed idle connection")
}

func appendFlagValue(args *[]string, flag, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	*args = append(*args, flag, strings.TrimSpace(value))
}

func parseDrive9LS(raw []byte) []FileEntry {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	entries := make([]FileEntry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		entries = append(entries, FileEntry{Name: fields[2], SizeBytes: size, IsDir: fields[0] == "d"})
	}
	if entries == nil {
		return []FileEntry{}
	}
	return entries
}

func parseDrive9Paths(raw []byte, limit int32) []SearchResult {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	results := make([]SearchResult, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		pathValue := fields[0]
		if strings.HasPrefix(pathValue, ":") {
			pathValue = strings.TrimPrefix(pathValue, ":")
		}
		results = append(results, SearchResult{Path: pathValue, Name: filepath.Base(pathValue)})
		if limit > 0 && int32(len(results)) >= limit {
			break
		}
	}
	if results == nil {
		return []SearchResult{}
	}
	return results
}

func parseDrive9LayerCommit(layerID string, raw []byte) apifs.FSLayerCommit {
	commit := apifs.FSLayerCommit{LayerID: layerID, Status: "committed"}
	parts := strings.Fields(string(bytes.TrimSpace(raw)))
	for _, part := range parts {
		if strings.HasPrefix(part, "applied=") {
			applied, _ := strconv.Atoi(strings.TrimPrefix(part, "applied="))
			commit.Applied = applied
		}
		if strings.HasPrefix(part, "layer=") && commit.LayerID == "" {
			commit.LayerID = strings.TrimPrefix(part, "layer=")
		}
	}
	return commit
}

func journalAppendStdin(opts JournalAppendOptions) ([]byte, error) {
	if len(opts.EntryJSON) > 0 {
		if opts.JSONArray {
			return []byte("[" + strings.Join(opts.EntryJSON, ",") + "]\n"), nil
		}
		return []byte(strings.Join(opts.EntryJSON, "\n") + "\n"), nil
	}
	if opts.Stdin == nil {
		return nil, apperr.New("journal.missing_entries", "usage", 2, "journal append requires --entry-json or stdin")
	}
	data, err := io.ReadAll(opts.Stdin)
	if err != nil {
		return nil, apperr.Wrap("journal.read_stdin", "runtime", 1, "read journal entries from stdin", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, apperr.New("journal.missing_entries", "usage", 2, "journal append requires --entry-json or stdin")
	}
	return data, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
