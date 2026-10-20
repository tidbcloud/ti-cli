package runtimeexec

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fswrap"
)

type Service struct {
	Resolver      endpoints.Resolver
	HomeDir       string
	CompanionPath string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Debug         bool
	DebugWriter   io.Writer
}

type Options struct {
	Profile      *config.Profile
	Workspace    string
	WorkspaceSet bool
	Cwd          string
	CwdSet       bool
	Environment  []string
	Timeout      string
	TimeoutSet   bool
	Fresh        bool
	Layer        string
	LayerSet     bool
	ReadOnly     bool
	Verbose      bool
	Command      []string
}

// ReadyService is returned only after the local companion has proved that it
// recognizes the Runtime command. Keeping preflight separate lets callers run
// it before loading a profile or resolving any filesystem credential.
type ReadyService struct {
	service Service
}

func (s Service) Exec(ctx context.Context, opts Options) error {
	if len(opts.Command) == 0 {
		return apperr.New("runtime.missing_command", "usage", 2, "missing command; see `ti exec --help`")
	}
	ready, err := s.Preflight(ctx)
	if err != nil {
		return err
	}
	return ready.Exec(ctx, opts)
}

// Preflight proves command availability using only the local companion. It
// intentionally supplies no profile, endpoint, or credential and accepts the
// command's exit status as the contract instead of parsing human help text.
func (s Service) Preflight(ctx context.Context) (ReadyService, error) {
	runner := fswrap.Runner{CompanionPath: s.CompanionPath}
	if err := requireExecCapability(ctx, runner, 10*time.Second); err != nil {
		return ReadyService{}, err
	}
	return ReadyService{service: s}, nil
}

// Exec runs exactly one Runtime command after a successful local preflight.
func (r ReadyService) Exec(ctx context.Context, opts Options) error {
	if opts.Profile == nil {
		return apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	if len(opts.Command) == 0 {
		return apperr.New("runtime.missing_command", "usage", 2, "missing command; see `ti exec --help`")
	}
	homeDir := r.service.HomeDir
	if homeDir == "" {
		homeDir = opts.Profile.HomeDir
	}
	runner := fswrap.Runner{
		HomeDir:       homeDir,
		CompanionPath: r.service.CompanionPath,
		Resolver:      r.service.Resolver,
		Stdin:         r.service.Stdin,
		Stdout:        r.service.Stdout,
		Stderr:        r.service.Stderr,
		Debug:         r.service.Debug,
		DebugWriter:   r.service.DebugWriter,
	}
	return runner.RunStreaming(ctx, fswrap.RunOptions{
		Profile:         opts.Profile,
		ResourceName:    opts.Profile.FSResourceName,
		Args:            companionArgs(opts),
		IncludeFSAPIKey: true,
	})
}

func requireExecCapability(ctx context.Context, runner fswrap.Runner, timeout time.Duration) error {
	probe, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := runner.Probe(probe, "exec", "--help")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apperr.New("runtime.capability_timeout", "runtime", 1, "the local Runtime capability check timed out; update or reinstall ti if this persists")
	}
	return err
}

func companionArgs(opts Options) []string {
	args := []string{"exec"}
	appendValue := func(name, value string, set bool) {
		if set {
			args = append(args, name, value)
		}
	}
	appendValue("--workspace", opts.Workspace, opts.WorkspaceSet)
	appendValue("--cwd", opts.Cwd, opts.CwdSet)
	for _, value := range opts.Environment {
		args = append(args, "--env", value)
	}
	appendValue("--timeout", opts.Timeout, opts.TimeoutSet)
	if opts.Fresh {
		args = append(args, "--fresh")
	}
	appendValue("--layer", opts.Layer, opts.LayerSet)
	if opts.ReadOnly {
		args = append(args, "--read-only")
	}
	if opts.Verbose {
		args = append(args, "--verbose")
	}
	args = append(args, "--")
	return append(args, opts.Command...)
}
