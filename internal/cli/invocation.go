package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const execCommandAnnotation = "ti.exec"

func isExecCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[execCommandAnnotation] == "true"
}

type invocation struct {
	commandAt int
	exec      bool
	update    bool
	help      bool
	debug     bool
	shortFlag string
}

// inspectInvocation observes ti's flags without parsing or retaining their
// values. Registered flag definitions determine which arguments consume values;
// the first exec positional (or --) ends inspection of remote arguments.
func inspectInvocation(root *cobra.Command, args []string) invocation {
	result := invocation{commandAt: -1}
	cmd := root
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			long := strings.HasPrefix(arg, "--")
			if !long && result.shortFlag == "" {
				result.shortFlag = arg
			}
			name := strings.TrimPrefix(arg, "-")
			if long {
				name = strings.TrimPrefix(name, "-")
			}
			for name != "" {
				var flag *pflag.Flag
				var value string
				var attached bool
				if long {
					name, value, attached = strings.Cut(name, "=")
					flag = cmd.Flag(name)
					name = ""
				} else {
					flag = cmd.Flags().ShorthandLookup(name[:1])
					if flag == nil {
						flag = cmd.InheritedFlags().ShorthandLookup(name[:1])
					}
					name = name[1:]
					if flag != nil && len(name) > 0 && (name[0] == '=' || flag.NoOptDefVal == "") {
						value, attached, name = strings.TrimPrefix(name, "="), true, ""
					}
				}
				if flag == nil {
					// Cobra owns unknown-flag diagnostics.
					break
				}
				if !attached {
					value = flag.NoOptDefVal
					if value == "" {
						i++ // A flag value is never metadata, even if it looks like a flag.
						continue
					}
				}
				if enabled, err := strconv.ParseBool(value); err == nil {
					switch flag.Name {
					case "help", "version":
						result.help = result.help || enabled
					case "debug":
						result.debug = enabled
					}
				}
			}
			continue
		}
		if result.exec {
			break
		}
		child := findChildCommand(cmd, arg)
		if child == nil {
			break
		}
		if cmd == root {
			result.commandAt = i
			result.update = child.Name() == "update" || (child.Name() == "help" && i+1 < len(args) && args[i+1] == "update")
		}
		cmd = child
		result.exec = isExecCommand(child)
		result.help = result.help || child.Name() == "help"
	}
	return result
}

func normalizeExecHelp(args []string, call invocation) []string {
	if call.exec && len(args) == call.commandAt+2 && args[call.commandAt+1] == "help" {
		args = append([]string(nil), args...)
		args[len(args)-1] = "--help"
	}
	return args
}
