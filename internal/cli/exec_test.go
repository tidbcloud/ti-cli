package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/tidbcloud/ti-cli/internal/version"
)

func TestInvocationMetadataRespectsFlagValuesAndRemoteBoundary(t *testing.T) {
	tests := []struct {
		args              []string
		exec, help, debug bool
	}{
		{[]string{"--profile", "stage", "--debug", "exec", "echo"}, true, false, true},
		{[]string{"--profile", "exec", "fs", "help"}, false, true, false},
		{[]string{"exec", "-e", "--debug", "echo", "--help"}, true, false, false},
		{[]string{"exec", "-w", "--help", "echo", "--debug"}, true, false, false},
		{[]string{"--profile", "--debug", "exec", "--", "--help"}, true, false, false},
		{[]string{"exec", "-vwrepo", "echo", "--debug"}, true, false, false},
		{[]string{"exec", "-wrepo", "--debug=true", "echo"}, true, false, true},
		{[]string{"exec", "--debug", "--debug=false", "echo"}, true, false, false},
		{[]string{"exec", "--help"}, true, true, false},
		{[]string{"exec", "--help=false", "echo"}, true, false, false},
		{[]string{"exec", "echo", "--help", "--debug"}, true, false, false},
		{[]string{"exec", "--new-value-flag", "--help", "echo"}, true, false, false},
		{[]string{"fs", "list-files", "--path", "--debug"}, false, false, false},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			root := NewRootCommand(version.Info{})
			findChildCommand(root, "exec").Flags().String("new-value-flag", "", "test registered flag")
			call := inspectInvocation(root, test.args)
			if call.exec != test.exec || call.help != test.help || call.debug != test.debug {
				t.Fatalf("metadata: exec=%t help=%t debug=%t; want %t %t %t", call.exec, call.help, call.debug, test.exec, test.help, test.debug)
			}
		})
	}
}

func TestExecHelpAndRemoteArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TI_LOGGING", "off")
	t.Setenv("TI_TELEMETRY", "off")
	for _, test := range []struct{ args, remote []string }{
		{[]string{"exec", "help"}, nil},
		{[]string{"--profile", "stage", "exec", "help"}, nil},
		{[]string{"exec", "--help"}, nil},
		{[]string{"exec", "-w", "repo", "help"}, []string{"help"}},
		{[]string{"exec", "--", "help"}, []string{"help"}},
		{[]string{"exec", "echo", "--help"}, []string{"echo", "--help"}},
		{[]string{"exec", "-vwrepo", "help"}, []string{"help"}},
		{[]string{"exec", "-e", "--debug", "echo", "--version"}, []string{"echo", "--version"}},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			root := NewRootCommand(version.Info{})
			var got []string
			findChildCommand(root, "exec").RunE = func(_ *cobra.Command, args []string) error { got = args; return nil }
			var out, errOut bytes.Buffer
			if err := Execute(context.Background(), root, version.Info{}, test.args, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, test.remote) {
				t.Fatalf("remote args=%q, want %q", got, test.remote)
			}
			if test.remote == nil && !bytes.Contains(out.Bytes(), []byte("Run a command in a remote sandbox.")) {
				t.Fatalf("missing local help: %s", out.String())
			}
		})
	}
}

func TestInvocationConsumesEveryRegisteredExecValueFlag(t *testing.T) {
	root := NewRootCommand(version.Info{})
	cmd := findChildCommand(root, "exec")
	check := func(flag *pflag.Flag) {
		if flag.NoOptDefVal != "" {
			return
		}
		for _, value := range []string{"--help", "--debug"} {
			args := []string{"exec", "--" + flag.Name, value, "echo"}
			call := inspectInvocation(root, args)
			if !call.exec || call.help || call.debug {
				t.Errorf("flag value interpreted as metadata: %s", flag.Name)
			}
			if flag.Shorthand != "" {
				args[1] = "-" + flag.Shorthand
				call = inspectInvocation(root, args)
				if !call.exec || call.help || call.debug {
					t.Errorf("short flag value interpreted as metadata: %s", flag.Name)
				}
			}
		}
	}
	cmd.Flags().VisitAll(check)
	cmd.InheritedFlags().VisitAll(check)
}
