package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/runtimeexec"
	"github.com/tidbcloud/ti-cli/internal/version"
)

type execFlags struct {
	workspace   string
	cwd         string
	environment []string
	timeout     string
	fresh       bool
	layer       string
	readOnly    bool
	verbose     bool
}

func newExecCommand(info version.Info) *cobra.Command {
	flags := &execFlags{}
	cmd := newCommand(commandSpec{
		Use:   "exec [flags] [--] command [arg...]",
		Short: "Run a command against a remote Filesystem.",
		Long: `Run a command against a remote Filesystem.

The server chooses the execution backend: supported commands use a lightweight
executor (justbash); other commands use a sandbox. A command that has already
started is never retried on another backend. Runtime is currently available only
in AWS us-east-1 and us-west-2.

The selected Filesystem is available at /workspace. Standard output and
standard error stream live. Only files under /workspace persist.

Select a Filesystem with --file-system-id, TI_FS_FILE_SYSTEM_ID, or an explicit
FS token. After selecting it, use -w to choose a Filesystem directory. Inside
a matching local FUSE mount, omit -w to drain pending writes and follow the
local directory. Explicit -w does not drain local writes. WebDAV mounts need -w.

Examples:
  ti exec --file-system-id <id> --read-only ls /workspace
  ti exec --file-system-id <id> -w repo npm test
  ti exec -w repo 'npm ci && npm test'
  ti exec -w repo -e CI=1 -t 20m -- make -j8 test

The last two examples assume a Filesystem is selected through the environment.
Remote stdin and interactive terminals are not supported. Executions are never
retried: a lost response may mean the command ran.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return apperr.New("runtime.missing_command", "usage", 2, "missing command; see `ti exec --help`")
			}
			for _, name := range []string{"output", "query"} {
				if flag := cmd.Flag(name); flag != nil && flag.Changed {
					return apperr.New("runtime.structured_output_not_supported", "usage", 2, fmt.Sprintf("--%s is not supported by `ti exec`; Runtime stdout and stderr stream unchanged", name))
				}
			}

			ctx := commandContext{cmd: cmd}
			debug, err := ctx.BoolFlag("debug")
			if err != nil {
				return err
			}
			service := runtimeexec.Service{
				Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
				Debug: debug, DebugWriter: cmd.ErrOrStderr(),
			}
			ready, err := service.Preflight(cmd.Context())
			if err != nil {
				return err
			}
			profile, err := ctx.LoadLocalProfile()
			if err != nil {
				return err
			}
			profile, err = fsResolveAuthenticatedProfile(ctx, profile, true)
			if err != nil {
				return err
			}
			return ready.Exec(cmd.Context(), runtimeexec.Options{
				Profile:      profile,
				Workspace:    flags.workspace,
				WorkspaceSet: cmd.Flags().Changed("workspace"),
				Cwd:          flags.cwd,
				CwdSet:       cmd.Flags().Changed("cwd"),
				Environment:  append([]string(nil), flags.environment...),
				Timeout:      flags.timeout,
				TimeoutSet:   cmd.Flags().Changed("timeout"),
				Fresh:        flags.fresh,
				Layer:        flags.layer,
				LayerSet:     cmd.Flags().Changed("layer"),
				ReadOnly:     flags.readOnly,
				Verbose:      flags.verbose,
				Command:      append([]string(nil), args...),
			})
		},
	}, info)
	cmd.Annotations = map[string]string{execCommandAnnotation: "true"}
	cmd.Args = func(_ *cobra.Command, _ []string) error { return nil }
	cmd.Flags().StringVarP(&flags.workspace, "workspace", "w", "", "Filesystem directory to run in; defaults to the matching FUSE directory or the Filesystem root.")
	cmd.Flags().StringVarP(&flags.cwd, "cwd", "C", "", "Subdirectory of the selected workspace to run in.")
	cmd.Flags().StringArrayVarP(&flags.environment, "env", "e", nil, "Set KEY=VALUE remotely, or copy a local KEY; repeatable.")
	cmd.Flags().StringVarP(&flags.timeout, "timeout", "t", "", "Stop the command after this duration; the server may use a shorter limit.")
	cmd.Flags().BoolVar(&flags.fresh, "fresh", false, "Start a new sandbox; persisted Filesystem data is kept.")
	cmd.Flags().StringVar(&flags.layer, "layer", "", "Use a LayerFS layer ID, name, or tag:key=value reference.")
	cmd.Flags().BoolVar(&flags.readOnly, "read-only", false, "Prevent writes to the Filesystem during this command.")
	cmd.Flags().BoolVarP(&flags.verbose, "verbose", "v", false, "Show execution details, working directory, and duration.")
	cmd.Flags().String("file-system-id", "", "The file system ID. Can also be supplied through TI_FS_FILE_SYSTEM_ID or derived from an explicitly supplied FS token.")
	cmd.Flags().String("fs-token", "", "File system token. Prefer TI_FS_TOKEN so the secret does not enter process arguments.")
	cmd.Flags().SetInterspersed(false)
	return cmd
}
