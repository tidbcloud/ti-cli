package cli

import (
	"fmt"
	"strings"

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
		Short: "Run a command in a sandbox with the selected Filesystem at /workspace.",
		Long: `Run one command in a remote sandbox with the selected TiDB Cloud Filesystem at /workspace.

Output streams live and the remote command's exit status is preserved. Inside
a matching local FUSE mount, ti uploads pending changes and runs in the same
directory. Elsewhere, use -w to select a remote workspace directory. Runtime
executions are never retried because a lost response may mean the command ran.`,
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
	cmd.Args = func(_ *cobra.Command, _ []string) error { return nil }
	cmd.Flags().StringVarP(&flags.workspace, "workspace", "w", "", "Drive9 directory to run in; default: the matching local FUSE mount directory, otherwise the drive root.")
	cmd.Flags().StringVarP(&flags.cwd, "cwd", "C", "", "Subdirectory of the selected workspace to run in.")
	cmd.Flags().StringArrayVarP(&flags.environment, "env", "e", nil, "Set KEY=VALUE remotely, or copy a local KEY; repeatable.")
	cmd.Flags().StringVarP(&flags.timeout, "timeout", "t", "", "Stop the command after this duration; the server may use a shorter limit.")
	cmd.Flags().BoolVar(&flags.fresh, "fresh", false, "Start in a new sandbox instead of reusing a warm sandbox.")
	cmd.Flags().StringVar(&flags.layer, "layer", "", "Use a LayerFS layer ID, name, or tag:key=value reference.")
	cmd.Flags().BoolVar(&flags.readOnly, "read-only", false, "Mount the workspace read-only.")
	cmd.Flags().BoolVarP(&flags.verbose, "verbose", "v", false, "Show the sandbox, working directory, and duration.")
	cmd.Flags().String("file-system-id", "", "The file system ID. Can also be supplied through TI_FS_FILE_SYSTEM_ID or derived from an explicitly supplied FS token.")
	cmd.Flags().String("fs-token", "", "File system token. Prefer TI_FS_TOKEN so the secret does not enter process arguments.")
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func isExecInvocation(args []string) bool {
	_, command := invocationCommandIndex(args)
	return command == "exec"
}

func invocationCommandIndex(args []string) (int, string) {
	valueFlags := map[string]struct{}{
		"profile": {}, "region": {}, "output": {}, "query": {},
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return -1, ""
		}
		if strings.HasPrefix(arg, "--") {
			name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			if _, ok := valueFlags[name]; ok && !hasValue && index+1 < len(args) {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return -1, ""
		}
		return index, arg
	}
	return -1, ""
}

func execMetadataArgs(args []string) []string {
	execIndex, command := invocationCommandIndex(args)
	if command != "exec" {
		return args
	}
	valueFlags := map[string]struct{}{
		"w": {}, "workspace": {}, "C": {}, "cwd": {}, "e": {}, "env": {},
		"t": {}, "timeout": {}, "layer": {}, "file-system-id": {}, "fs-token": {},
		"profile": {}, "region": {}, "output": {}, "query": {},
	}
	for index := execIndex + 1; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return args[:index]
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return args[:index]
		}
		name := strings.TrimLeft(arg, "-")
		name, _, hasValue := strings.Cut(name, "=")
		if _, ok := valueFlags[name]; ok && !hasValue && index+1 < len(args) {
			index++
		}
	}
	return args
}
