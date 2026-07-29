package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/auth"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/config/store"
	"github.com/tidbcloud/tdc/internal/dryrun"
	"github.com/tidbcloud/tdc/internal/oplog"
	"github.com/tidbcloud/tdc/internal/output"
	"github.com/tidbcloud/tdc/internal/version"
)

const usageRequiredFlagAnnotation = "tdc_usage_required"

func init() {
	cobra.AddTemplateFunc("usageSynopsis", usageSynopsis)
	cobra.AddTemplateFunc("formattedFlagUsages", formattedFlagUsages)
}

type Options struct {
	Profile string
	Region  string
	Debug   bool
	Output  string
	Query   string
}

func NewRootCommand(info version.Info) *cobra.Command {
	opts := &Options{}

	root := newCommand(commandSpec{
		Use:   "tdc",
		Short: "CLI for TiDB Cloud Filesystem and TiDB Cloud Starter.",
		Long:  "The TiDB Cloud Command Line Interface is a unified tool to manage your TiDB Cloud Filesystem and Starter services.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rootCommandRequiredError(cmd)
		},
	}, info)

	root.SilenceErrors = true
	root.SilenceUsage = true
	root.Annotations = map[string]string{
		"tdc.version": info.Version,
		"tdc.commit":  info.Commit,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(flagErrorFunc)
	root.SetHelpTemplate(helpTemplate)
	root.SetUsageTemplate(usageTemplate)

	flags := root.PersistentFlags()
	flags.StringVar(&opts.Profile, "profile", "default", "The profile name for tdc CLI.")
	flags.StringVar(&opts.Region, "region", "", "Override region code in the profile, for example aws-us-east-1.")
	flags.BoolVar(&opts.Debug, "debug", false, "Enable debug output.")
	flags.StringVar(&opts.Output, "output", "json", "Output format: json or text.")
	flags.StringVar(&opts.Query, "query", "", "JMESPath query applied before rendering the output.")

	root.AddCommand(newConfigureCommand(info))
	root.AddCommand(newUpdateCommand(info))
	root.AddCommand(newDBCommand(info))
	root.AddCommand(newFSCommand(info))
	root.AddCommand(newFSGitCommand(info))
	root.AddCommand(newFSJournalCommand(info))
	root.AddCommand(newOrganizationCommand(info))
	root.AddCommand(newFSVaultCommand(info))

	installHelpCommands(root, info)
	applyCommandDefaults(root, info)

	return root
}

func rootCommandRequiredError(cmd *cobra.Command) error {
	return apperr.New(
		"cli.missing_command",
		"usage",
		2,
		fmt.Sprintf(`the following arguments are required: command

%s

usage: tdc <command> [<subcommand>] [parameters]
To see help information, you can run:

  tdc help
  tdc <command> help
  tdc <command> <subcommand> help`, cmd.Long),
	)
}

func Execute(ctx context.Context, root *cobra.Command, args []string, stdout, stderr io.Writer) error {
	recorder := commandRecorder()
	ctx = oplog.WithRecorder(ctx, recorder)
	start := time.Now()
	if err := rejectShortFlags(args); err != nil {
		recorder.Record(ctx, commandEvent(root, root, err, time.Since(start)))
		return err
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		recorder.Record(ctx, commandEvent(root, cmd, nil, time.Since(start)))
		return nil
	}
	normalized := normalizeError(err)
	recorder.Record(ctx, commandEvent(root, cmd, normalized, time.Since(start)))
	return normalized
}

func commandRecorder() oplog.Recorder {
	home, err := os.UserHomeDir()
	if err != nil {
		return oplog.NewRecorder(oplog.Config{Enabled: false})
	}
	cfg, err := oplog.LoadConfig(home, nil)
	if err != nil {
		return oplog.NewRecorder(oplog.Config{Enabled: false})
	}
	return oplog.NewRecorder(cfg)
}

func commandEvent(root, cmd *cobra.Command, err error, duration time.Duration) oplog.Event {
	if cmd == nil {
		cmd = root
	}
	profileName, regionCode := commandProfileSummary(cmd)
	return oplog.Event{
		Type:          "command",
		Version:       rootAnnotation(root, "tdc.version"),
		Commit:        rootAnnotation(root, "tdc.commit"),
		Profile:       profileName,
		RegionCode:    regionCode,
		Command:       cmd.CommandPath(),
		FlagNames:     changedFlagNames(cmd),
		DurationMS:    duration.Milliseconds(),
		ExitCode:      apperr.ExitCodeFor(err),
		ErrorCode:     apperr.CodeFor(err),
		ErrorCategory: apperr.CategoryFor(err),
	}
}

func rootAnnotation(root *cobra.Command, key string) string {
	if root == nil || root.Annotations == nil {
		return ""
	}
	return root.Annotations[key]
}

func changedFlagNames(cmd *cobra.Command) []string {
	if cmd == nil {
		return nil
	}
	names := make([]string, 0)
	visit := func(flag *pflag.Flag) {
		if flag != nil {
			names = append(names, flag.Name)
		}
	}
	cmd.Flags().Visit(visit)
	cmd.PersistentFlags().Visit(visit)
	cmd.InheritedFlags().Visit(visit)
	return oplog.SortedFlagNames(names)
}

func commandProfileSummary(cmd *cobra.Command) (string, string) {
	profileName := config.DefaultProfile
	regionOverride := ""
	if flag := cmd.Flag("region"); flag != nil {
		regionOverride = strings.TrimSpace(flag.Value.String())
	}
	profileExplicit := false
	if flag := cmd.Flag("profile"); flag != nil {
		if strings.TrimSpace(flag.Value.String()) != "" {
			profileName = flag.Value.String()
		}
		profileExplicit = flag.Changed
	}
	if !profileExplicit {
		if envProfile := strings.TrimSpace(os.Getenv("TDC_PROFILE")); envProfile != "" {
			profileName = envProfile
		}
	}
	if regionOverride != "" {
		return profileName, regionOverride
	}
	if envRegion := strings.TrimSpace(os.Getenv("TDC_REGION_CODE")); envRegion != "" {
		return profileName, envRegion
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return profileName, ""
	}
	configDoc, err := store.ReadConfig(home)
	if err != nil {
		return profileName, ""
	}
	return profileName, configDoc[profileName].RegionCode
}

func rejectShortFlags(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			return nil
		}
		if len(arg) > 1 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			return apperr.New(
				"cli.short_flag_not_allowed",
				"usage",
				2,
				fmt.Sprintf("short flags are not supported: %s; use long flags such as --help", arg),
			)
		}
	}
	return nil
}

type commandSpec struct {
	Use     string
	Aliases []string
	Short   string
	Long    string
	RunE    func(cmd *cobra.Command, args []string) error
}

func newCommand(spec commandSpec, info version.Info) *cobra.Command {
	cmd := &cobra.Command{
		Use:           spec.Use,
		Aliases:       spec.Aliases,
		Short:         spec.Short,
		Long:          spec.Long,
		Version:       info.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          spec.RunE,
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	return cmd
}

func newParentCommand(use, short string, info version.Info) *cobra.Command {
	return newCommand(commandSpec{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}, info)
}

func newPlaceholderCommand(use, short string, info version.Info) *cobra.Command {
	return newCommand(commandSpec{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return apperr.NotImplemented(cmd.CommandPath())
		},
	}, info)
}

type mutationMode string

const (
	readOnlyCommand mutationMode = "read_only"
	mutatingCommand mutationMode = "mutating"
)

type controlPlaneCommandSpec struct {
	Use        string
	Aliases    []string
	Short      string
	Long       string
	Mutation   mutationMode
	Permission authz.Permission
	Run        func(commandContext) (any, error)
	DryRun     func(commandContext) (dryrun.Result, error)
}

type commandContext struct {
	cmd *cobra.Command
}

func (c commandContext) CommandPath() string {
	return c.cmd.CommandPath()
}

func (c commandContext) LoadProfile() (*config.Profile, error) {
	return loadProfileForCommand(c.cmd)
}

func (c commandContext) LoadLocalProfile() (*config.Profile, error) {
	return loadLocalProfileForCommand(c.cmd)
}

func (c commandContext) Permission() authz.Permission {
	permission, _ := authz.ForCommand(c.cmd.CommandPath())
	return permission
}

func (c commandContext) StringFlag(name string) (string, error) {
	return c.cmd.Flags().GetString(name)
}

func (c commandContext) StringArrayFlag(name string) ([]string, error) {
	return c.cmd.Flags().GetStringArray(name)
}

func (c commandContext) FlagChanged(name string) bool {
	return c.cmd.Flags().Changed(name)
}

func (c commandContext) BoolFlag(name string) (bool, error) {
	flag := c.cmd.Flag(name)
	if flag == nil {
		return false, nil
	}
	return strconv.ParseBool(flag.Value.String())
}

func (c commandContext) Int32Flag(name string) (int32, error) {
	return c.cmd.Flags().GetInt32(name)
}

func (c commandContext) Int64Flag(name string) (int64, error) {
	return c.cmd.Flags().GetInt64(name)
}

func (c commandContext) DurationFlag(name string) (time.Duration, error) {
	return c.cmd.Flags().GetDuration(name)
}

func newControlPlanePlaceholderCommand(use, short string, mutation mutationMode, permission authz.Permission, info version.Info) *cobra.Command {
	return newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        use,
		Short:      short,
		Mutation:   mutation,
		Permission: permission,
		Run: func(ctx commandContext) (any, error) {
			return nil, apperr.NotImplemented(ctx.CommandPath())
		},
	}, info)
}

func newControlPlaneCommand(spec controlPlaneCommandSpec, info version.Info) *cobra.Command {
	if spec.Permission == "" {
		panic(fmt.Sprintf("control-plane command %s must declare a permission", spec.Use))
	}
	cmd := newCommand(commandSpec{
		Use:     spec.Use,
		Aliases: spec.Aliases,
		Short:   spec.Short,
		Long:    spec.Long,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := commandContext{cmd: cmd}
			if spec.Mutation == mutatingCommand {
				isDryRun, err := cmd.Flags().GetBool("dry-run")
				if err != nil {
					return err
				}
				if isDryRun {
					run := spec.DryRun
					if run == nil {
						run = defaultControlPlaneDryRun(spec)
					}
					result, err := run(ctx)
					if err != nil {
						return err
					}
					return renderStructured(cmd, result)
				}
			}

			run := spec.Run
			if run == nil {
				run = func(ctx commandContext) (any, error) {
					return nil, apperr.NotImplemented(ctx.CommandPath())
				}
			}
			result, err := run(ctx)
			if err != nil {
				return err
			}
			return renderStructured(cmd, result)
		},
	}, info)
	if spec.Mutation == mutatingCommand {
		cmd.Flags().Bool("dry-run", false, "Validate the request without doing the actual changes.")
	}
	return cmd
}

func defaultControlPlaneDryRun(spec controlPlaneCommandSpec) func(commandContext) (dryrun.Result, error) {
	return func(ctx commandContext) (dryrun.Result, error) {
		profile, err := ctx.LoadProfile()
		if err != nil {
			return dryrun.Result{}, err
		}
		if isFSCommandPath(ctx.CommandPath()) {
			_, selected, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			profile = selected
		}
		provider := profile.CloudProvider
		regionCode := profile.RegionCode
		if profile.FSCloudProvider != "" {
			provider = profile.FSCloudProvider
		}
		if profile.FSRegionCode != "" {
			regionCode = profile.FSRegionCode
		}

		return dryrun.New(
			ctx.CommandPath(),
			strings.ReplaceAll(spec.Use, "-", "_"),
			dryrun.RequestSummary{
				Description: "service-specific request construction is pending implementation",
			},
			dryrun.Check{
				Name:    "config_and_credentials",
				Status:  "passed",
				Message: fmt.Sprintf("profile %q loaded", profile.Name),
			},
			dryrun.Check{
				Name:    "endpoint_selection",
				Status:  "passed",
				Message: fmt.Sprintf("%s %s", provider, regionCode),
			},
			dryrun.Check{
				Name:    "permission_requirement",
				Status:  "passed",
				Message: string(spec.Permission),
			},
			dryrun.Check{
				Name:    "request_construction",
				Status:  "passed",
				Message: "shared dry-run envelope constructed; service-specific request shape is pending command implementation",
			},
		), nil
	}
}

func isFSCommandPath(path string) bool {
	return strings.HasPrefix(path, "tdc fs ") ||
		strings.HasPrefix(path, "tdc fs-git ") ||
		strings.HasPrefix(path, "tdc fs-journal ") ||
		strings.HasPrefix(path, "tdc fs-vault ")
}

func renderStructured(cmd *cobra.Command, result any) error {
	format, err := stringFlag(cmd, "output")
	if err != nil {
		return err
	}
	expression, err := stringFlag(cmd, "query")
	if err != nil {
		return err
	}
	return output.Render(cmd.OutOrStdout(), result, output.Options{
		Format: format,
		Query:  expression,
	})
}

func stringFlag(cmd *cobra.Command, name string) (string, error) {
	flag := cmd.Flag(name)
	if flag == nil {
		return "", nil
	}
	return flag.Value.String(), nil
}

func loadProfileForCommand(cmd *cobra.Command) (*config.Profile, error) {
	opts, err := loadOptionsForCommand(cmd)
	if err != nil {
		return nil, err
	}
	return auth.LoadProfile(cmd.Context(), opts)
}

func loadLocalProfileForCommand(cmd *cobra.Command) (*config.Profile, error) {
	opts, err := loadOptionsForCommand(cmd)
	if err != nil {
		return nil, err
	}
	return config.LoadLocal(cmd.Context(), opts)
}

func loadOptionsForCommand(cmd *cobra.Command) (config.LoadOptions, error) {
	profileName, err := stringFlag(cmd, "profile")
	if err != nil {
		return config.LoadOptions{}, err
	}
	regionOverride, err := stringFlag(cmd, "region")
	if err != nil {
		return config.LoadOptions{}, err
	}
	regionFlag := cmd.Flag("region")
	if regionFlag != nil && regionFlag.Changed && strings.TrimSpace(regionOverride) == "" {
		return config.LoadOptions{}, apperr.New("config.empty_region", "usage", 2, "--region cannot be empty")
	}
	profileFlag := cmd.Flag("profile")
	profileExplicit := profileFlag != nil && profileFlag.Changed
	if profileExplicit {
		if strings.TrimSpace(profileName) == "" {
			return config.LoadOptions{}, apperr.New("config.empty_profile", "usage", 2, "--profile cannot be empty")
		}
	} else if envProfile := strings.TrimSpace(os.Getenv("TDC_PROFILE")); envProfile != "" {
		profileName = envProfile
		profileExplicit = true
	}
	return config.LoadOptions{
		Profile:         profileName,
		ProfileExplicit: profileExplicit,
		RegionOverride:  regionOverride,
	}, nil
}

func applyCommandDefaults(cmd *cobra.Command, info version.Info) {
	cmd.Version = info.String()
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.SetFlagErrorFunc(flagErrorFunc)
	cmd.SetHelpTemplate(helpTemplate)
	cmd.SetUsageTemplate(usageTemplate)
	if cmd.Flags().Lookup("help") == nil {
		cmd.Flags().Bool("help", false, "Display help information.")
	}
	if cmd.Flags().Lookup("version") == nil {
		cmd.Flags().Bool("version", false, "Display the version for this tool.")
	}
	cmd.Flags().SortFlags = true
	cmd.PersistentFlags().SortFlags = true

	for _, child := range cmd.Commands() {
		applyCommandDefaults(child, info)
	}
}

func flagErrorFunc(cmd *cobra.Command, err error) error {
	return apperr.Wrap(
		"cli.invalid_flag",
		"usage",
		2,
		fmt.Sprintf("invalid flag for %s: %v", cmd.CommandPath(), err),
		err,
	)
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}

	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return err
	}
	var converted interface {
		AppError() *apperr.Error
	}
	if errors.As(err, &converted) {
		if appErr := converted.AppError(); appErr != nil {
			return appErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return apperr.New("cli.interrupted", "runtime", 130, "interrupted")
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown command"):
		return apperr.Wrap("cli.unknown_command", "usage", 2, msg, err)
	case strings.Contains(msg, "unknown flag"):
		return apperr.Wrap("cli.unknown_flag", "usage", 2, msg, err)
	case strings.Contains(msg, "accepts 0 arg"):
		return apperr.Wrap("cli.invalid_args", "usage", 2, msg, err)
	default:
		return apperr.Wrap("cli.execution_failed", "runtime", 1, msg, err)
	}
}

func HasShorthand(cmd *cobra.Command) bool {
	found := false
	visitCommands(cmd, func(current *cobra.Command) {
		for _, set := range []*pflag.FlagSet{
			current.Flags(),
			current.PersistentFlags(),
			current.LocalNonPersistentFlags(),
			current.InheritedFlags(),
		} {
			set.VisitAll(func(flag *pflag.Flag) {
				if flag.Shorthand != "" {
					found = true
				}
			})
		}
	})
	return found
}

func visitCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, child := range cmd.Commands() {
		visitCommands(child, fn)
	}
}

func installHelpCommands(cmd *cobra.Command, info version.Info) {
	for _, child := range cmd.Commands() {
		installHelpCommands(child, info)
	}
	if cmd.Name() == "help" || findChildCommand(cmd, "help") != nil {
		return
	}

	helpCmd := newCommand(commandSpec{
		Use:   "help",
		Short: "Display help information.",
		RunE: func(help *cobra.Command, _ []string) error {
			return help.Parent().Help()
		},
	}, info)
	cmd.SetHelpCommand(helpCmd)
	cmd.AddCommand(helpCmd)
}

func findChildCommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func markUsageRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			continue
		}
		if flag.Annotations == nil {
			flag.Annotations = map[string][]string{}
		}
		flag.Annotations[usageRequiredFlagAnnotation] = []string{"true"}
	}
}

func usageSynopsis(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	lines := []string{"  " + usageCommandLine(cmd)}
	lines = append(lines, usageFlagLines(cmd.LocalFlags())...)
	lines = append(lines, usageFlagLines(cmd.InheritedFlags())...)
	return strings.Join(lines, "\n")
}

func usageCommandLine(cmd *cobra.Command) string {
	line := cmd.CommandPath()
	if cmd.HasAvailableSubCommands() {
		return line + " [command]"
	}
	return line
}

func usageFlagLines(flags *pflag.FlagSet) []string {
	if flags == nil {
		return nil
	}
	required := make([]string, 0)
	optional := make([]string, 0)
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag == nil || flag.Hidden {
			return
		}
		line := "    " + usageFlagSegment(flag)
		if usageFlagRequired(flag) {
			required = append(required, line)
			return
		}
		optional = append(optional, "    ["+strings.TrimSpace(line)+"]")
	})
	return append(required, optional...)
}

func usageFlagRequired(flag *pflag.Flag) bool {
	if flag == nil || flag.Annotations == nil {
		return false
	}
	values := flag.Annotations[usageRequiredFlagAnnotation]
	return len(values) > 0 && values[0] == "true"
}

func usageFlagSegment(flag *pflag.Flag) string {
	segment := "--" + flag.Name
	if flag.NoOptDefVal != "" {
		return segment
	}
	valueType := usageFlagValueType(flag)
	if valueType == "" {
		return segment
	}
	return segment + " <" + valueType + ">"
}

func usageFlagValueType(flag *pflag.Flag) string {
	if flag == nil || flag.Value == nil {
		return ""
	}
	switch flag.Value.Type() {
	case "bool":
		return ""
	case "stringArray":
		return "string"
	default:
		return flag.Value.Type()
	}
}

func formattedFlagUsages(flags *pflag.FlagSet) string {
	if flags == nil {
		return ""
	}
	type flagUsage struct {
		label       string
		description string
	}
	lines := make([]flagUsage, 0)
	maxLabelLength := 0
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag == nil || flag.Hidden {
			return
		}
		label := "      " + usageFlagSegment(flag)
		if usageFlagRequired(flag) {
			label += " (required)"
		}
		_, description := pflag.UnquoteUsage(flag)
		description += formattedFlagDefault(flag)
		if flag.Deprecated != "" {
			description += fmt.Sprintf(" (DEPRECATED: %s)", flag.Deprecated)
		}
		lines = append(lines, flagUsage{label: label, description: description})
		if len(label) > maxLabelLength {
			maxLabelLength = len(label)
		}
	})

	var result strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&result, "%-*s   %s\n", maxLabelLength, line.label, line.description)
	}
	return result.String()
}

func formattedFlagDefault(flag *pflag.Flag) string {
	if flag == nil || flagDefaultIsZero(flag) {
		return ""
	}
	if flag.Value.Type() == "string" {
		return fmt.Sprintf(" (default %q)", flag.DefValue)
	}
	return fmt.Sprintf(" (default %s)", flag.DefValue)
}

func flagDefaultIsZero(flag *pflag.Flag) bool {
	if flag == nil || flag.Value == nil {
		return true
	}
	switch flag.Value.Type() {
	case "bool", "boolfunc":
		return flag.DefValue == "false" || flag.DefValue == ""
	case "duration":
		return flag.DefValue == "0" || flag.DefValue == "0s"
	case "int", "int8", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "count", "float32", "float64":
		return flag.DefValue == "0"
	case "string":
		return flag.DefValue == ""
	case "ip", "ipMask", "ipNet":
		return flag.DefValue == "<nil>"
	case "intSlice", "stringSlice", "stringArray", "uintSlice", "boolSlice":
		return flag.DefValue == "[]"
	default:
		return flag.DefValue == "false" || flag.DefValue == "<nil>" || flag.DefValue == "" || flag.DefValue == "0"
	}
}

const helpTemplate = `{{with (or .Long .Short)}}{{.}}

{{end}}Usage:
{{usageSynopsis .}}{{if .Aliases}}

Aliases:
{{range .Aliases}}  {{.}}
{{end}}{{end}}{{if .HasAvailableSubCommands}}

Commands:
{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}  {{rpad .Name .NamePadding }} {{.Short}}
{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{formattedFlagUsages .LocalFlags | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{formattedFlagUsages .InheritedFlags | trimTrailingWhitespaces}}{{end}}
`

const usageTemplate = `Usage:
{{usageSynopsis .}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} help [command]{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{formattedFlagUsages .LocalFlags | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{formattedFlagUsages .InheritedFlags | trimTrailingWhitespaces}}{{end}}
`
