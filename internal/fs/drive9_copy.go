package fs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/interrupt"
)

// The copy family lives in its own file: the route resolver, the --overwrite
// probe, the server-side replace with its staging cleanup, and the companion argv
// they share. `ti fs copy-file` is the only command whose behaviour depends on
// the destination class, so keeping it together makes that one place to read.

func (s Service) drive9CopyFile(ctx context.Context, opts CopyFileOptions) (FileOperationResult, error) {
	args, source, target, err := drive9CopyArgs(opts)
	if err != nil {
		return FileOperationResult{}, err
	}
	if err := s.ensureDrive9CopyTargetAbsent(ctx, opts); err != nil {
		return FileOperationResult{}, err
	}
	if opts.CreateParents && strings.TrimSpace(opts.ToLocal) != "" {
		if err := os.MkdirAll(filepath.Dir(opts.ToLocal), 0o755); err != nil {
			return FileOperationResult{}, apperr.Wrap("fs.create_local_parent", "runtime", 1, fmt.Sprintf("create parent directories for %q", opts.ToLocal), err)
		}
	}
	if isDrive9ServerSideReplace(opts) {
		return s.drive9CopyReplacingRemoteTarget(ctx, opts, args, source, target)
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

// isDrive9ServerSideReplace reports whether this copy is the one shape the
// server cannot do in a single request: a remote to remote copy that must
// replace an existing destination. The other shapes replace natively (an upload
// writes the path, a download creates the file) and their exemptions are
// handled before this point.
func isDrive9ServerSideReplace(opts CopyFileOptions) bool {
	if !opts.Overwrite || opts.Append || opts.Resume || opts.Recursive {
		return false
	}
	if strings.TrimSpace(opts.LayerID) != "" {
		return false
	}
	return opts.FromRemote != "" && opts.ToRemote != ""
}

// drive9CopyReplacingRemoteTarget replaces an existing remote path. The
// server's copy inserts a new node at the destination and the unique path index
// rejects the duplicate, so --overwrite cannot be handed to the companion. Copy
// the source to a sibling temporary path and then move it onto the destination:
// the rename replaces the target in one datastore transaction, so a reader sees
// either the old file or the complete new one, and a failed copy never touches
// the destination. A failure after the staged copy removes the staging file when
// the token allows it; when it does not, the error names the file and the
// command that deletes it.
func (s Service) drive9CopyReplacingRemoteTarget(ctx context.Context, opts CopyFileOptions, args []string, source, target string) (FileOperationResult, error) {
	destination, err := s.resolveDrive9RemoteCopyDestination(ctx, opts, drive9CopySourceRemote, source, target)
	if err != nil {
		return FileOperationResult{}, err
	}
	if !destination.Exists {
		if _, err := s.drive9RunTransientRetry(ctx, opts.Profile, args, false); err != nil {
			return FileOperationResult{}, err
		}
		return FileOperationResult{Operation: "copy_file", SourcePath: source, TargetPath: target, Status: "copied"}, nil
	}
	if destination.IsDir {
		return FileOperationResult{}, apperr.New("fs.remote_copy_target_is_directory", "usage", 2, fmt.Sprintf("remote target %q is a directory; a server-side copy replaces files only - copy into it or delete it first", destination.Path))
	}
	if sameDrive9RemotePath(source, destination.Path) {
		return FileOperationResult{}, remoteTargetExistsError(source, destination.Path, drive9CopySourceRemote)
	}
	temporary := drive9CopyTempPath(destination.Path)
	staged, _, _, stageErr := drive9CopyArgsForTarget(opts, temporary)
	if stageErr != nil {
		return FileOperationResult{}, stageErr
	}
	if _, err := s.drive9RunTransientRetry(ctx, opts.Profile, staged, false); err != nil {
		return FileOperationResult{}, s.drive9CopyFailure(ctx, opts, source, destination.Path, temporary, drive9CopyStageBeforeMove, err)
	}
	if _, err := s.drive9Run(ctx, opts.Profile, []string{"fs", "mv", drive9Remote(temporary), drive9Remote(destination.Path)}, false); err != nil {
		return FileOperationResult{}, s.drive9CopyFailure(ctx, opts, source, destination.Path, temporary, drive9CopyStageAfterMove, err)
	}
	return FileOperationResult{Operation: "copy_file", SourcePath: source, TargetPath: target, Status: "copied"}, nil
}

// drive9CopyFailure decides the error the user sees after a failure that may
// have left the staging file behind, and cleans it up when it can. It asks the
// service whether the staging path exists instead of reading one `fs rm` answer:
// the service authorizes DELETE before it looks a path up, so a token without
// delete answers "access denied" for a file that was never created, and an error
// body that happens to contain "not found" is not a 404.
//
// stage separates the two readings of an absent staging file: before the rename
// it means the copy created nothing, after it means the rename was probably
// applied and only its answer was lost.
func (s Service) drive9CopyFailure(ctx context.Context, opts CopyFileOptions, source, destination, staging string, stage drive9CopyStage, cause error) error {
	// The cleanup has to finish what the cancel started: on Ctrl+C the caller's
	// context is already done, and a check that inherits it would report a
	// leftover it never looked for. The bound keeps a slow stat from pinning the
	// command.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drive9CopyCleanupTimeout)
	defer cancel()

	// A fresh registration, not interrupt.Signals(checkCtx): the command's own
	// channel already holds the signal that cancelled the copy, so reading it
	// here would abandon the cleanup on that very signal.
	signalCh, stopSignals := interrupt.NotifyStop()
	defer stopSignals()

	done := make(chan error, 1)
	go func() {
		done <- s.drive9CopyCleanup(checkCtx, opts, source, destination, staging, stage, cause)
	}()
	select {
	case err := <-done:
		return err
	case <-signalCh:
		cancel()
		return drive9StagingUncheckedError(destination, staging, cause, errors.New("interrupted while checking the staging path"))
	}
}

// drive9CopyCleanup is the body of drive9CopyFailure, split out so the caller can
// stop it on a signal.
func (s Service) drive9CopyCleanup(ctx context.Context, opts CopyFileOptions, source, destination, staging string, stage drive9CopyStage, cause error) error {
	_, exists, statErr := s.drive9StatTarget(ctx, opts.Profile, staging)
	switch {
	case statErr == nil && !exists:
		// Nothing was created, or the rename consumed it.
		if stage == drive9CopyStageAfterMove {
			return drive9CopyUnknownOutcomeError(cause, destination, source)
		}
		return cause
	case statErr == nil:
		removed, removeErr := s.removeDrive9Staging(ctx, opts, staging)
		switch {
		case removeErr != nil:
			return drive9StagingLeftoverError(destination, staging, source, stage, cause, removeErr)
		case !removed:
			// The staging file vanished between the look and the removal: the same
			// reading as an absent file.
			if stage == drive9CopyStageAfterMove {
				return drive9CopyUnknownOutcomeError(cause, destination, source)
			}
		}
		return cause
	}
	// The staging path could not be read: a token whose read does not cover it, a
	// backend error, or an expired bound. This is not evidence that the file is
	// there, so the wording must not claim a leftover; the removal answer is the
	// only signal left, and a failed one says "could not check", not "could not
	// remove".
	if err := ctx.Err(); err != nil {
		return drive9StagingUncheckedError(destination, staging, cause, fmt.Errorf("the cleanup bound expired: %w", err))
	}
	removed, removeErr := s.removeDrive9Staging(ctx, opts, staging)
	switch {
	case removeErr != nil:
		return drive9StagingUncheckedError(destination, staging, cause, fmt.Errorf("reading %q said %v and removing it said %v", staging, statErr, removeErr))
	case !removed:
		if stage == drive9CopyStageAfterMove {
			return drive9CopyUnknownOutcomeError(cause, destination, source)
		}
	}
	return cause
}

// removeDrive9Staging removes the staging path and reports whether it was there
// to remove: "not found" is a normal answer once the rename consumed it.
func (s Service) removeDrive9Staging(ctx context.Context, opts CopyFileOptions, staging string) (bool, error) {
	_, err := s.drive9RunTransientRetry(ctx, opts.Profile, []string{"fs", "rm", drive9Remote(staging)}, true)
	switch {
	case err == nil:
		return true, nil
	case isDrive9NotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// drive9CopyStage says where in the replace a failure happened, which decides how
// an absent staging file reads.
type drive9CopyStage int

const (
	drive9CopyStageBeforeMove drive9CopyStage = iota
	drive9CopyStageAfterMove
)

// drive9CopyCleanupTimeout bounds the cleanup of a failed replace, which runs on
// a context that outlives the cancelled one. A variable so tests can shorten it.
var drive9CopyCleanupTimeout = 30 * time.Second

// drive9CopyUnknownOutcomeError reports a rename whose answer was lost while its
// staging file is gone: the destination may already hold the new bytes.
func drive9CopyUnknownOutcomeError(cause error, destination, source string) error {
	return apperr.Wrap("fs.copy_outcome_unknown", "runtime", 1, fmt.Sprintf("replacing %q reported %v and its staging copy is gone, so the copy may have been applied; check both paths with `ti fs describe-file --path %s` and `--path %s`, then re-run with --overwrite if it was not", destination, cause, destination, source), cause)
}

// drive9StagingLeftoverError names a staging file the replace did not remove.
// That removal needs delete on the path, which is exactly what was refused, so
// the message says so.
func drive9StagingLeftoverError(destination, staging, source string, stage drive9CopyStage, cause, cleanupErr error) error {
	opening := fmt.Sprintf("copying to the staging path for %q", destination)
	if stage == drive9CopyStageAfterMove {
		opening = fmt.Sprintf("replacing %q", destination)
	}
	return apperr.Wrap("fs.copy_staging_left_behind", "runtime", 1, fmt.Sprintf("%s failed (%v) and the staging copy %q could not be removed (%v); delete it with `ti fs delete-file --path %s`, which needs delete on that path; the source is %s", opening, cause, staging, cleanupErr, staging, source), cause)
}

// drive9StagingUncheckedError reports a failure whose staging file ti could not
// look for. It deliberately does not claim the file exists: the caller has to
// look, and the message says where and how.
func drive9StagingUncheckedError(destination, staging string, cause, why error) error {
	return apperr.Wrap("fs.copy_staging_unchecked", "runtime", 1, fmt.Sprintf("the replace at %q failed (%v) and ti could not determine whether the staging file %q exists (%v); look next to the destination for an entry ending in .ti-tmp- and, if it is there, delete it with `ti fs delete-file --path %s`, which needs delete on that path", destination, cause, staging, why, staging), cause)
}

// drive9CopyTempPath names the staging path next to the destination, so the
// final move stays within one directory and needs no extra parent creation.
func drive9CopyTempPath(target string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return strings.TrimSuffix(target, "/") + ".ti-tmp-" + hex.EncodeToString(buf)
}

// drive9CopyTargetKind is the resolved destination class of a copy. The probe
// and the executed command must agree on it, so both derive it from one
// classifier instead of two switches that can drift.
type drive9CopyTargetKind int

const (
	drive9CopyTargetNone drive9CopyTargetKind = iota
	drive9CopyTargetRemote
	drive9CopyTargetLocal
	drive9CopyTargetStdout
)

// drive9CopySourceKind is the resolved source class. The resolver returns it so
// neither consumer re-reads the raw flags and disagrees with the chosen route.
type drive9CopySourceKind int

const (
	drive9CopySourceNone drive9CopySourceKind = iota
	drive9CopySourceStdin
	drive9CopySourceLocal
	drive9CopySourceRemote
)

// ValidateDrive9CopyRoute enforces the shape every copy names: exactly one
// source and exactly one destination. It is the single place that counts them,
// so the command builder (which dispatches --to-stdout before the service runs),
// drive9CopyArgs and the overwrite probe cannot disagree.
func ValidateDrive9CopyRoute(opts CopyFileOptions) error {
	destinations := 0
	for _, set := range []bool{opts.ToRemote != "", opts.ToLocal != "", opts.ToStdout} {
		if set {
			destinations++
		}
	}
	// Two destinations are always the ambiguity error; the partial shapes below
	// are the "not a pair" error.
	if destinations > 1 {
		return apperr.New("fs.ambiguous_copy_target", "usage", 2, "name exactly one destination: --to-remote, --to-local, or --to-stdout")
	}
	sources := 0
	for _, set := range []bool{opts.FromStdin, opts.FromLocal != "", opts.FromRemote != ""} {
		if set {
			sources++
		}
	}
	if sources != 1 || destinations != 1 {
		return apperr.New("fs.invalid_copy_flags", "usage", 2, "copy-file requires exactly one source/target pair")
	}
	// The companion resolves a location scheme itself and treats a ":" prefix as
	// a drive9 path, but ti probes local destinations as literal filesystem
	// paths. Either spelling would therefore pass the probe (nothing exists at
	// the literal string) and still write somewhere else, which is the overwrite
	// ti is supposed to refuse. Reject the spelling instead of probing a path the
	// companion will not use.
	for _, field := range []struct {
		flag  string
		value string
	}{
		{flag: "--from-local", value: opts.FromLocal},
		{flag: "--to-local", value: opts.ToLocal},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, ":") {
			return apperr.New("fs.local_path_scheme_unsupported", "usage", 2, fmt.Sprintf("%s %q is a drive9 path; pass a local filesystem path", field.flag, field.value))
		}
		if scheme := invalidLocalPathScheme(value); scheme != "" {
			return apperr.New("fs.local_path_scheme_unsupported", "usage", 2, fmt.Sprintf("%s %q carries the URL scheme %q; pass a plain filesystem path", field.flag, field.value, scheme))
		}
	}
	return nil
}

// invalidLocalPathScheme returns the scheme of a local path spelling the
// companion would resolve itself, or "" when the value is a plain path.
// A single-letter scheme is a Windows drive letter, and an opaque URL
// ("a:b") is a legal file name on the platforms that matter here.
func invalidLocalPathScheme(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" || len(parsed.Scheme) == 1 {
		return ""
	}
	return parsed.Scheme
}

// resolveDrive9CopyRoute resolves the single source/target pair a copy names.
// drive9CopyArgs and the --overwrite probe both consume this result, so the
// executed command and the probe cannot resolve different paths.
func resolveDrive9CopyRoute(opts CopyFileOptions) (drive9CopyTargetKind, drive9CopySourceKind, string, string, error) {
	if err := ValidateDrive9CopyRoute(opts); err != nil {
		return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
	}
	switch {
	case opts.FromStdin && opts.ToRemote != "":
		target, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		return drive9CopyTargetRemote, drive9CopySourceStdin, "-", target, nil
	case opts.FromLocal != "" && opts.ToRemote != "":
		target, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		return drive9CopyTargetRemote, drive9CopySourceLocal, opts.FromLocal, target, nil
	case opts.FromRemote != "" && opts.ToStdout:
		source, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		return drive9CopyTargetStdout, drive9CopySourceRemote, source, "-", nil
	case opts.FromRemote != "" && opts.ToLocal != "":
		source, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		return drive9CopyTargetLocal, drive9CopySourceRemote, source, opts.ToLocal, nil
	case opts.FromRemote != "" && opts.ToRemote != "":
		source, err := normalizeRemotePath(opts.FromRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		target, err := normalizeRemotePath(opts.ToRemote)
		if err != nil {
			return drive9CopyTargetNone, drive9CopySourceNone, "", "", err
		}
		return drive9CopyTargetRemote, drive9CopySourceRemote, source, target, nil
	}
	return drive9CopyTargetNone, drive9CopySourceNone, "", "", apperr.New("fs.invalid_copy_flags", "usage", 2, "copy-file requires exactly one source/target pair")
}

// drive9CopySourceName is the base name the companion appends when the
// destination names a directory (`dir/<base(source)>`). An empty result means
// the name cannot be known locally (stdin).
func drive9CopySourceName(kind drive9CopySourceKind, source string) string {
	switch kind {
	case drive9CopySourceStdin:
		return ""
	case drive9CopySourceRemote:
		trimmed := strings.TrimSuffix(source, "/")
		if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
			return trimmed[idx+1:]
		}
		return trimmed
	case drive9CopySourceLocal:
		return filepath.Base(filepath.Clean(source))
	}
	return ""
}

// ensureDrive9CopyTargetAbsent enforces the documented --overwrite=false
// contract for companion copies. drive9CopyArgs has no overwrite flag to
// forward because the delegated `drive9 fs cp` has no such flag, and what
// replaces an existing destination differs by direction: an upload writes it and
// a download creates it with os.Create, while a server-side copy inserts a new
// node and the server rejects the duplicate path. ti therefore probes the
// resolved target itself and words the refusal for the direction.
//
// Four cases are deliberately exempt:
//
//   - a recursive copy, where the companion accepts an existing destination root
//     and aborts before transferring anything if a descendant path exists, so
//     --overwrite does not govern it;
//   - a layer destination, where `fs stat` cannot see inside the layer and the
//     layer write is an upsert, so --overwrite does not govern it either;
//   - --append and --resume, where the companion's own branch decides what
//     happens to an existing destination: an append extends it, while a download
//     with --resume ignores the flag and creates the file, so it replaces it.
func (s Service) ensureDrive9CopyTargetAbsent(ctx context.Context, opts CopyFileOptions) error {
	if opts.Overwrite || opts.Append || opts.Resume || opts.Recursive {
		return nil
	}
	kind, sourceKind, source, target, err := resolveDrive9CopyRoute(opts)
	if err != nil {
		return err
	}
	switch kind {
	case drive9CopyTargetRemote:
		if strings.TrimSpace(opts.LayerID) != "" {
			return nil
		}
		return s.ensureDrive9RemoteTargetAbsent(ctx, opts, source, target, sourceKind)
	case drive9CopyTargetLocal:
		return ensureDrive9LocalCopyTargetAbsent(target, drive9CopySourceName(sourceKind, source))
	}
	return nil
}

// drive9RemoteCopyDestination is the remote path a copy will actually write,
// after the directory rule is applied (dir/<base(source)>).
type drive9RemoteCopyDestination struct {
	Path   string
	Exists bool
	IsDir  bool
}

// resolveDrive9RemoteCopyDestination mirrors the companion's destination rule:
// a path with a trailing separator names a directory, an existing directory
// receives <dir>/<base(source)>, and anything else is written as named. The
// probe and the --overwrite replace both consume this, so neither can disagree
// with the other about what path the copy touches.
func (s Service) resolveDrive9RemoteCopyDestination(ctx context.Context, opts CopyFileOptions, sourceKind drive9CopySourceKind, source, target string) (drive9RemoteCopyDestination, error) {
	sourceName := drive9CopySourceName(sourceKind, source)
	if strings.HasSuffix(target, "/") {
		return s.resolveDrive9RemoteCopyChild(ctx, opts, target, sourceName)
	}
	metadata, exists, err := s.drive9StatTarget(ctx, opts.Profile, target)
	if err != nil {
		return drive9RemoteCopyDestination{}, err
	}
	if !exists {
		return drive9RemoteCopyDestination{Path: target}, nil
	}
	if !metadata.IsDir {
		return drive9RemoteCopyDestination{Path: target, Exists: true}, nil
	}
	return s.resolveDrive9RemoteCopyChild(ctx, opts, target, sourceName)
}

func (s Service) resolveDrive9RemoteCopyChild(ctx context.Context, opts CopyFileOptions, dir, sourceName string) (drive9RemoteCopyDestination, error) {
	if sourceName == "" {
		return drive9RemoteCopyDestination{}, copyTargetRequiresName(dir)
	}
	child := strings.TrimSuffix(dir, "/") + "/" + sourceName
	metadata, exists, err := s.drive9StatTarget(ctx, opts.Profile, child)
	if err != nil {
		return drive9RemoteCopyDestination{}, err
	}
	return drive9RemoteCopyDestination{Path: child, Exists: exists, IsDir: exists && metadata.IsDir}, nil
}

func (s Service) ensureDrive9RemoteTargetAbsent(ctx context.Context, opts CopyFileOptions, source, target string, sourceKind drive9CopySourceKind) error {
	destination, err := s.resolveDrive9RemoteCopyDestination(ctx, opts, sourceKind, source, target)
	if err != nil {
		return err
	}
	if !destination.Exists {
		return nil
	}
	return remoteTargetExistsError(source, destination.Path, sourceKind)
}

// remoteTargetExistsError words the --overwrite=false refusal. Every direction
// can be replaced now: an upload writes the remote path, a download creates the
// local file, and a server-side copy is staged and renamed onto the destination
// (see drive9CopyReplacingRemoteTarget), so the advice is the same everywhere.
//
// A directory destination resolves to <dir>/<base(source)>, which can be the
// source itself (`--from-remote /dir/a --to-remote /dir` is an easy thing to
// type). That case must not offer a replacement: the destination is the file
// being copied.
func remoteTargetExistsError(source, target string, sourceKind drive9CopySourceKind) error {
	// Only a remote source shares the destination's namespace: a local absolute
	// path can spell the same string without naming the same file.
	if sourceKind == drive9CopySourceRemote && sameDrive9RemotePath(source, target) {
		return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("remote target %q is the source path; copy to a different path instead", target))
	}
	return apperr.New("fs.target_exists", "usage", 2, fmt.Sprintf("remote target %q already exists; pass --overwrite to replace it", target))
}

// sameDrive9RemotePath reports whether two resolved drive9 paths name the same
// node. Both come from resolveDrive9CopyRoute, so the only spelling difference
// left is the trailing separator of a directory.
func sameDrive9RemotePath(left, right string) bool {
	return strings.TrimSuffix(left, "/") == strings.TrimSuffix(right, "/")
}

// drive9StatTarget reports whether a remote path exists and, when it does, its
// metadata. A not-found answer is not an error.
func (s Service) drive9StatTarget(ctx context.Context, profile *config.Profile, remotePath string) (drive9StatMetadata, bool, error) {
	result, statErr := s.drive9RunTransientRetry(ctx, profile, []string{"fs", "stat", "--output", "json", drive9Remote(remotePath)}, true)
	if statErr != nil {
		if isDrive9NotFound(statErr) {
			return drive9StatMetadata{}, false, nil
		}
		return drive9StatMetadata{}, false, statErr
	}
	var metadata drive9StatMetadata
	if err := json.Unmarshal(result.Stdout, &metadata); err != nil {
		return drive9StatMetadata{}, false, apperr.Wrap("fs.companion_decode", "runtime", 1, "decode ti fs stat response", err)
	}
	return metadata, true, nil
}

// ensureDrive9LocalCopyTargetAbsent is the local-destination half of the
// contract. It mirrors resolveLocalCopyTarget: a trailing separator names a
// directory, an existing directory (or a symlink resolving to one) receives
// <target>/<base(source)>, and Lstat runs first so a dangling symlink counts as
// an existing target instead of being silently followed.
func ensureDrive9LocalCopyTargetAbsent(target, sourceName string) error {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	trailingSeparator := strings.HasSuffix(target, "/") || strings.HasSuffix(target, string(os.PathSeparator))
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !trailingSeparator {
			return nil
		}
		return ensureDrive9LocalChildAbsent(target, sourceName)
	case err != nil:
		return apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local file %q", target), err)
	}
	if trailingSeparator || info.IsDir() || symlinkResolvesToDir(target, info) {
		return ensureDrive9LocalChildAbsent(target, sourceName)
	}
	return ensureLocalTargetCanWrite(target, false, false)
}

func ensureDrive9LocalChildAbsent(dir, sourceName string) error {
	if sourceName == "" {
		return copyTargetRequiresName(dir)
	}
	// Join the directory exactly the way the companion does
	// (filepath.Join(localPath, sourceBase) in resolveLocalCopyTarget). Join
	// cleans a trailing separator itself; trimming them first turned a filesystem
	// root into an empty string, so `--to-local /` probed a path relative to the
	// working directory while the companion wrote /<name>.
	return ensureLocalTargetCanWrite(filepath.Join(dir, sourceName), false, false)
}

// copyTargetRequiresName is the ti-owned spelling of the companion's
// "destination %q requires a file name": a directory destination cannot be
// written when the source carries no name.
func copyTargetRequiresName(dir string) error {
	return apperr.New("fs.copy_target_requires_name", "usage", 2, fmt.Sprintf("destination %q names a directory and the source has no file name; copy to an explicit file path instead", dir))
}

// symlinkResolvesToDir reports whether a symlink at path points at a directory.
// A dangling link reports false, which keeps it classified as an existing target
// (writing through it would create the link target).
func symlinkResolvesToDir(path string, info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	resolved, err := os.Stat(path)
	return err == nil && resolved.IsDir()
}

func drive9CopyArgs(opts CopyFileOptions) ([]string, string, string, error) {
	return drive9CopyArgsForTarget(opts, "")
}

// drive9CopyArgsForTarget builds the companion argv for a copy. targetOverride
// is used by the server-side --overwrite replace, which copies to a sibling
// temporary path first; every other caller passes "".
func drive9CopyArgsForTarget(opts CopyFileOptions, targetOverride string) ([]string, string, string, error) {
	if opts.Recursive && strings.TrimSpace(opts.LayerID) != "" {
		return nil, "", "", apperr.New("fs.recursive_layer_copy_unsupported", "usage", 2, "--recursive cannot be combined with --layer-id; mount the layer with --driver fuse and copy the directory through the mounted path")
	}
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
	kind, sourceKind, source, target, err := resolveDrive9CopyRoute(opts)
	if err != nil {
		return nil, "", "", err
	}
	if targetOverride != "" {
		if kind != drive9CopyTargetRemote || sourceKind != drive9CopySourceRemote {
			return nil, "", "", apperr.New("fs.invalid_copy_flags", "usage", 2, "a retargeted copy must be a remote to remote copy")
		}
		target = targetOverride
	}
	switch kind {
	case drive9CopyTargetRemote:
		switch sourceKind {
		case drive9CopySourceStdin:
			args = append(args, "-", drive9Remote(target))
		case drive9CopySourceRemote:
			args = append(args, drive9Remote(source), drive9Remote(target))
		default:
			args = append(args, source, drive9Remote(target))
		}
	case drive9CopyTargetLocal:
		args = append(args, drive9Remote(source), target)
	case drive9CopyTargetStdout:
		args = append(args, drive9Remote(source), "-")
	}
	return args, source, target, nil
}
