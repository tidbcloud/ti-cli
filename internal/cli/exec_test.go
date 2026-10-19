package cli

import (
	"slices"
	"testing"
)

func TestInvocationCommandIndexSkipsGlobalFlagValues(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		index   int
		command string
	}{
		{name: "plain", args: []string{"exec", "echo"}, index: 0, command: "exec"},
		{name: "global value", args: []string{"--profile", "stage", "--debug", "exec", "echo"}, index: 3, command: "exec"},
		{name: "global attached value", args: []string{"--profile=stage", "exec", "echo"}, index: 1, command: "exec"},
		{name: "flag value named exec", args: []string{"--profile", "exec", "fs", "help"}, index: 2, command: "fs"},
		{name: "invalid short global", args: []string{"-p", "stage", "exec", "echo"}, index: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index, command := invocationCommandIndex(test.args)
			if index != test.index || command != test.command {
				t.Fatalf("invocationCommandIndex(%#v) = (%d, %q), want (%d, %q)", test.args, index, command, test.index, test.command)
			}
		})
	}
}

func TestExecMetadataArgsStopBeforeRemoteCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "implicit command boundary",
			args: []string{"--profile", "stage", "exec", "-e", "CI=1", "echo", "--debug", "--help"},
			want: []string{"--profile", "stage", "exec", "-e", "CI=1"},
		},
		{
			name: "explicit command boundary",
			args: []string{"exec", "--fresh", "--", "--debug", "--version"},
			want: []string{"exec", "--fresh"},
		},
		{
			name: "non exec unchanged",
			args: []string{"fs", "list-files", "--debug"},
			want: []string{"fs", "list-files", "--debug"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := execMetadataArgs(test.args); !slices.Equal(got, test.want) {
				t.Fatalf("execMetadataArgs(%#v) = %#v, want %#v", test.args, got, test.want)
			}
		})
	}
}
