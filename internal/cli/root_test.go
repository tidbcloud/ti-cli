package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config/store"
	"github.com/tidbcloud/tdc/internal/dryrun"
	"github.com/tidbcloud/tdc/internal/oplog"
	"github.com/tidbcloud/tdc/internal/settings"
	"github.com/tidbcloud/tdc/internal/telemetry"
	"github.com/tidbcloud/tdc/internal/version"
)

func TestHelpCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "root help",
			args: []string{"help"},
			want: "Commands:",
		},
		{
			name: "db help",
			args: []string{"db", "help"},
			want: "create-db-cluster",
		},
		{
			name: "fs help",
			args: []string{"fs", "help"},
			want: "mount-file-system",
		},
		{
			name: "subcommand help",
			args: []string{"fs", "mount-file-system", "help"},
			want: "Mount a file system to a local path.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := executeForTest(tt.args...)
			if err != nil {
				t.Fatalf("expected help to succeed, got %v", err)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Fatalf("expected output to contain %q, got:\n%s", tt.want, stdout)
			}
		})
	}
}

func TestRootRequiresCommand(t *testing.T) {
	stdout, stderr, err := executeForTest()
	if err == nil {
		t.Fatal("expected root command without arguments to fail")
	}
	if stdout != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("expected CLI boundary to leave error rendering to main, got %q", stderr)
	}
	if got := apperr.CodeFor(err); got != "cli.missing_command" {
		t.Fatalf("expected cli.missing_command, got %q", got)
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	message := apperr.MessageFor(err)
	for _, want := range []string{
		"the following arguments are required: command",
		"The TiDB Cloud Command Line Interface is a unified tool",
		"usage: tdc <command> [<subcommand>] [parameters]",
		"tdc <command> <subcommand> help",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected root usage error to contain %q, got:\n%s", want, message)
		}
	}
}

func TestVersionWorksAtEveryLevel(t *testing.T) {
	tests := [][]string{
		{"--version"},
		{"db", "--version"},
		{"fs", "mount-file-system", "--version"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := executeForTest(args...)
			if err != nil {
				t.Fatalf("expected version to succeed, got %v", err)
			}
			if got := strings.TrimSpace(stdout); got != testVersion().String() {
				t.Fatalf("expected %q, got %q", testVersion().String(), got)
			}
		})
	}
}

func TestUnknownCommandReturnsActionableError(t *testing.T) {
	_, _, err := executeForTest("db", "missing-command")
	if err == nil {
		t.Fatal("expected unknown command to fail")
	}
	if apperr.ExitCodeFor(err) != 2 {
		t.Fatalf("expected exit code 2, got %d", apperr.ExitCodeFor(err))
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "unknown command") {
		t.Fatalf("expected unknown command message, got %q", got)
	}
}

func TestContextCanceledIsRenderedAsInterrupted(t *testing.T) {
	err := normalizeError(context.Canceled)
	if err == nil {
		t.Fatal("expected interrupted error")
	}
	if got := apperr.ExitCodeFor(err); got != 130 {
		t.Fatalf("expected exit code 130, got %d", got)
	}
	if got := apperr.MessageFor(err); got != "interrupted" {
		t.Fatalf("expected interrupted message, got %q", got)
	}
}

func TestShortHelpFlagIsRejected(t *testing.T) {
	stdout, _, err := executeForTest("-h")
	if err == nil {
		t.Fatal("expected -h to fail")
	}
	if stdout != "" {
		t.Fatalf("expected no stdout, got %q", stdout)
	}
	if apperr.ExitCodeFor(err) != 2 {
		t.Fatalf("expected exit code 2, got %d", apperr.ExitCodeFor(err))
	}
}

func TestCommandOperationLogRecordsSafeSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "on")
	configureIAMForTest(t)

	_, _, err := executeForTest(
		"configure",
		"--profile", "ci",
		"--region-code", "aws-us-east-1",
		"--tdc-public-key", "public-secret",
		"--tdc-private-key", "private-secret",
		"--non-interactive",
	)
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	data, err := os.ReadFile(oplog.Path(home))
	if err != nil {
		t.Fatalf("read operation log: %v", err)
	}
	if strings.Contains(string(data), "public-secret") || strings.Contains(string(data), "private-secret") {
		t.Fatalf("operation log leaked secret values:\n%s", string(data))
	}
	var event oplog.Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var candidate oplog.Event
		if err := json.Unmarshal(line, &candidate); err != nil {
			t.Fatalf("decode operation log: %v\n%s", err, string(data))
		}
		if candidate.Type == "command" {
			event = candidate
		}
	}
	if event.Type != "command" || event.Command != "tdc configure" || event.Profile != "ci" || event.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected command event: %#v", event)
	}
	if !containsString(event.FlagNames, "tdc-private-key") || !containsString(event.FlagNames, "tdc-public-key") {
		t.Fatalf("expected flag names only, got %#v", event.FlagNames)
	}
}

func TestCommandOperationLogCanBeDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")

	_, _, err := executeForTest("help")
	if err != nil {
		t.Fatalf("help failed: %v", err)
	}
	if _, err := os.Stat(oplog.Path(home)); !os.IsNotExist(err) {
		t.Fatalf("expected no operation log file, got err=%v", err)
	}
}

func TestMalformedGlobalSettingsDisableLoggingWithRedactedDebug(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "on")
	if err := os.MkdirAll(filepath.Dir(settings.Path(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	sensitiveMarker := "must-not-appear-in-debug"
	before := []byte("[logging]\nenabled = \"" + sensitiveMarker + "\"\n")
	if err := os.WriteFile(settings.Path(home), before, 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := executeForTest("--debug", "help")
	if err != nil {
		t.Fatalf("help failed because settings were malformed: %v", err)
	}
	if !strings.Contains(stderr, "operation logging disabled because global settings could not be loaded") {
		t.Fatalf("missing redacted debug diagnostic: %q", stderr)
	}
	if strings.Contains(stderr, sensitiveMarker) || strings.Contains(stderr, "enabled") {
		t.Fatalf("debug diagnostic leaked settings data: %q", stderr)
	}
	after, err := os.ReadFile(settings.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("malformed settings were rewritten")
	}
	if _, err := os.Stat(oplog.Path(home)); !os.IsNotExist(err) {
		t.Fatalf("malformed settings created operation log: %v", err)
	}
}

func TestIsUpdateInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "update", args: []string{"update"}, want: true},
		{name: "update check", args: []string{"update", "--check"}, want: true},
		{name: "global boolean before update", args: []string{"--debug", "update", "--check"}, want: true},
		{name: "global value before update", args: []string{"--profile", "stage", "update"}, want: true},
		{name: "global equals before update", args: []string{"--profile=stage", "update"}, want: true},
		{name: "help update", args: []string{"help", "update"}, want: true},
		{name: "update help", args: []string{"update", "help"}, want: true},
		{name: "db update", args: []string{"db", "update-db-cluster"}, want: false},
		{name: "update profile value", args: []string{"--profile", "update", "db", "list-db-clusters"}, want: false},
		{name: "update profile and command", args: []string{"--profile", "update", "update"}, want: true},
		{name: "update query value", args: []string{"--query", "update", "db", "list-db-clusters"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isUpdateInvocation(test.args); got != test.want {
				t.Fatalf("isUpdateInvocation(%q) = %t, want %t", test.args, got, test.want)
			}
		})
	}
}

func TestUpdateInvocationsDoNotReadOrWriteTDCHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "on")
	tdcDir := filepath.Join(home, ".tdc")
	if err := os.MkdirAll(tdcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		settings.Path(home):         []byte("not valid toml = ["),
		store.ConfigPath(home):      []byte("not valid config = ["),
		store.CredentialsPath(home): []byte("not valid credentials = ["),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.0","html_url":"https://example.test/v0.1.0","assets":[]}`))
	}))
	defer server.Close()
	t.Setenv("TDC_RELEASE_API_BASE_URL", server.URL)

	for _, args := range [][]string{
		{"update", "--check", "--debug"},
		{"--debug", "update", "--check"},
		{"update", "--dry-run", "--debug"},
		{"update", "help", "--debug"},
		{"update", "--help", "--debug"},
		{"update", "--version", "--debug"},
		{"help", "update", "--debug"},
	} {
		_, stderr, _ := executeForTest(args...)
		if strings.Contains(stderr, "operation logging disabled") {
			t.Fatalf("%q read global settings: %s", args, stderr)
		}
	}

	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) {
			t.Fatalf("update invocation changed %s", path)
		}
	}
	if _, err := os.Stat(oplog.Path(home)); !os.IsNotExist(err) {
		t.Fatalf("update invocation wrote operation log: %v", err)
	}
}

func TestTelemetryExclusionsDoNotReadStateOrSend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")
	for _, key := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TF_BUILD", "JENKINS_URL", "TDC_TELEMETRY"} {
		t.Setenv(key, "")
	}
	legacyConfig := "[logging]\nenabled = false\n"
	if err := os.MkdirAll(filepath.Dir(store.ConfigPath(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ConfigPath(home), []byte(legacyConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://example.test/v0.2.0","assets":[]}`))
	}))
	defer releaseServer.Close()
	t.Setenv("TDC_RELEASE_API_BASE_URL", releaseServer.URL)
	info := testVersion()
	info.Version = "0.2.0"
	info.InstallSource = "archive"
	info.TelemetryEndpoint = server.URL

	invocations := [][]string{
		nil,
		{"help"},
		{"--help"},
		{"--version"},
		{"db"},
		{"db", "help"},
		{"db", "--help"},
		{"db", "--version"},
		{"db", "create-db-cluster", "help"},
		{"db", "create-db-cluster", "--help"},
		{"db", "create-db-cluster", "--version"},
		{"update"},
		{"update", "--check"},
		{"update", "--dry-run"},
		{"update", "--target-version", "v0.2.0"},
		{"update", "help"},
		{"update", "--help"},
		{"update", "--version"},
		{"help", "update"},
		{"--profile", "stage", "update", "--check"},
		{"unknown-command"},
	}
	for _, args := range invocations {
		var stdout, stderr bytes.Buffer
		root := NewRootCommand(info)
		_ = Execute(context.Background(), root, info, args, &stdout, &stderr)
	}
	if requests != 0 {
		t.Fatalf("excluded invocations sent %d telemetry requests", requests)
	}
	data, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != legacyConfig {
		t.Fatalf("excluded invocation read and migrated settings:\n%s", data)
	}
	for _, path := range []string{settings.Path(home), telemetry.InstallationIDPath(home)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("excluded invocation created %s: %v", path, err)
		}
	}
}

func TestTelemetrySendsCanonicalSafeCommandEvent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")
	t.Setenv("TDC_TELEMETRY", "on")
	t.Setenv("TDC_TELEMETRY_TAG", "e2b-preview")
	t.Setenv("TDC_TELEMETRY_EXTRA", `{"campaign":"launch","runtime":"e2b"}`)
	withConfigEnv(t)

	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	info := testVersion()
	info.TelemetryEndpoint = server.URL

	clusterName := "must-not-appear-cluster-name"
	stdout, stderr, err := executeForTestWithInfo(info,
		"--profile", "must-not-appear-profile",
		"db", "create-db-cluster",
		"--db-cluster-type", "starter",
		"--db-cluster-name", clusterName,
		"--project-id", "must-not-appear-project-id",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if stdout == "" || stderr != "" {
		t.Fatalf("unexpected command streams: stdout=%q stderr=%q", stdout, stderr)
	}
	var body []byte
	select {
	case body = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("eligible command did not send telemetry")
	}
	for _, prohibited := range []string{clusterName, "must-not-appear-profile", "must-not-appear-project-id", "test-public", "test-private"} {
		if strings.Contains(string(body), prohibited) {
			t.Fatalf("telemetry payload leaked %q: %s", prohibited, body)
		}
	}
	var payload struct {
		SchemaVersion int `json:"schema_version"`
		Events        []struct {
			CommandPath   string          `json:"command_path"`
			FlagNames     []string        `json:"flag_names"`
			ExitCode      int             `json:"exit_code"`
			ErrorCode     string          `json:"error_code"`
			CloudProvider string          `json:"cloud_provider"`
			RegionCode    string          `json:"region_code"`
			ProfileSource string          `json:"profile_source"`
			Tag           string          `json:"tag"`
			Extra         json.RawMessage `json:"extra"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != 2 || len(payload.Events) != 1 {
		t.Fatalf("event count = %d", len(payload.Events))
	}
	event := payload.Events[0]
	if event.CommandPath != "tdc db create-db-cluster" || event.ExitCode != 0 || event.ErrorCode != "" || event.CloudProvider != "aws" || event.RegionCode != "aws-us-east-1" || event.ProfileSource != "explicit" || event.Tag != "e2b-preview" || string(event.Extra) != `{"campaign":"launch","runtime":"e2b"}` {
		t.Fatalf("unexpected telemetry event: %#v", event)
	}
	for _, want := range []string{"db-cluster-name", "dry-run", "profile", "project-id"} {
		if !containsString(event.FlagNames, want) {
			t.Fatalf("missing changed flag %q in %#v", want, event.FlagNames)
		}
	}
}

func TestTelemetryCapturesResolvedValidationError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")
	t.Setenv("TDC_TELEMETRY", "on")
	requests := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	info := testVersion()
	info.TelemetryEndpoint = server.URL

	_, _, commandErr := executeForTestWithInfo(info, "db", "describe-db-cluster")
	if commandErr == nil {
		t.Fatal("expected missing required flag error")
	}
	var payload struct {
		Events []struct {
			CommandPath string `json:"command_path"`
			ExitCode    int    `json:"exit_code"`
			ErrorCode   string `json:"error_code"`
		} `json:"events"`
	}
	var body []byte
	select {
	case body = <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("resolved validation error did not send telemetry")
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.Events[0]; got.CommandPath != "tdc db describe-db-cluster" || got.ExitCode != 2 || got.ErrorCode == "" {
		t.Fatalf("unexpected validation event: %#v", got)
	}
}

func TestResolvedTelemetryCommandUsesCanonicalLeaf(t *testing.T) {
	root := NewRootCommand(testVersion())
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"db", "create-db-cluster"}, want: "tdc db create-db-cluster"},
		{args: []string{"--profile", "stage", "db", "create-db-cluster"}, want: "tdc db create-db-cluster"},
		{args: []string{"fs", "cp"}, want: "tdc fs copy-file"},
		{args: []string{"configure"}, want: "tdc configure"},
		{args: []string{"db"}},
		{args: []string{"db", "help"}},
		{args: []string{"db", "missing"}},
		{args: []string{"update"}},
	}
	for _, test := range tests {
		cmd := resolvedTelemetryCommand(root, test.args)
		got := ""
		if cmd != nil {
			got = cmd.CommandPath()
		}
		if got != test.want {
			t.Errorf("resolvedTelemetryCommand(%q) = %q, want %q", test.args, got, test.want)
		}
	}
}

func TestNoTelemetryManagementCommandIsRegistered(t *testing.T) {
	root := NewRootCommand(testVersion())
	for _, command := range root.Commands() {
		if command.Name() == "telemetry" || strings.Contains(command.Name(), "telemetry") {
			t.Fatalf("unexpected telemetry management command %q", command.CommandPath())
		}
	}
}

func TestTelemetryFailureDoesNotChangeCommandResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")
	withConfigEnv(t)
	args := []string{"db", "create-db-cluster", "--db-cluster-name", "demo", "--project-id", "project-1", "--dry-run"}

	t.Setenv("TDC_TELEMETRY", "off")
	wantStdout, wantStderr, wantErr := executeForTestWithInfo(testVersion(), args...)

	t.Setenv("TDC_TELEMETRY", "on")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("backend-sensitive-message"))
	}))
	non202Endpoint := server.URL
	closedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	networkFailureEndpoint := closedServer.URL
	closedServer.Close()

	for name, endpoint := range map[string]string{
		"non-202 response": non202Endpoint,
		"network failure":  networkFailureEndpoint,
	} {
		t.Run(name, func(t *testing.T) {
			info := testVersion()
			info.TelemetryEndpoint = endpoint
			gotStdout, gotStderr, gotErr := executeForTestWithInfo(info, args...)
			if gotStdout != wantStdout || gotStderr != wantStderr || apperr.CodeFor(gotErr) != apperr.CodeFor(wantErr) || apperr.ExitCodeFor(gotErr) != apperr.ExitCodeFor(wantErr) {
				t.Fatalf("telemetry failure changed result:\nwant stdout=%q stderr=%q err=%v\ngot stdout=%q stderr=%q err=%v", wantStdout, wantStderr, wantErr, gotStdout, gotStderr, gotErr)
			}
		})
	}
	server.Close()
}

func TestConfigureRejectsReservedLoggingProfileBeforeWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_LOGGING", "off")

	_, _, err := executeForTest(
		"configure",
		"--profile", "logging",
		"--region-code", "aws-us-east-1",
		"--tdc-public-key", "public",
		"--tdc-private-key", "private",
		"--non-interactive",
	)
	if err == nil {
		t.Fatal("expected reserved profile to fail")
	}
	if apperr.CodeFor(err) != "config.reserved_profile" || apperr.ExitCodeFor(err) != 2 {
		t.Fatalf("unexpected reserved profile error: %v", err)
	}
	for _, path := range []string{store.ConfigPath(home), store.CredentialsPath(home), settings.Path(home)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("reserved configure wrote %s: %v", path, statErr)
		}
	}
}

func TestHelpOutputDoesNotExposeShortFlags(t *testing.T) {
	stdout, _, err := executeForTest("--help")
	if err != nil {
		t.Fatalf("expected --help to succeed, got %v", err)
	}
	if strings.Contains(stdout, "-h,") || strings.Contains(stdout, "-v,") {
		t.Fatalf("help output exposes a short flag:\n%s", stdout)
	}
	if !strings.Contains(stdout, "--region <string>") {
		t.Fatalf("help output does not include global --region flag:\n%s", stdout)
	}
}

func TestHelpUsageShowsRequiredFirstAndOptionalBracketed(t *testing.T) {
	stdout, _, err := executeForTest("configure", "help")
	if err != nil {
		t.Fatalf("expected configure help to succeed, got %v", err)
	}
	if strings.Contains(stdout, "[flags]") {
		t.Fatalf("help output should not use generic [flags] usage:\n%s", stdout)
	}
	if want := "Usage:\n  tdc configure\n    [--help]\n    [--non-interactive]\n    [--region-code <string>]"; !strings.Contains(stdout, want) {
		t.Fatalf("expected optional flags to be bracketed in configure usage, got:\n%s", stdout)
	}

	stdout, _, err = executeForTest("db", "execute-sql-statement", "help")
	if err != nil {
		t.Fatalf("expected db execute help to succeed, got %v", err)
	}
	required := "    --db-cluster-id <string>\n    --sql <string>"
	optional := "    [--admin]"
	requiredIndex := strings.Index(stdout, required)
	optionalIndex := strings.Index(stdout, optional)
	if requiredIndex < 0 || optionalIndex < 0 {
		t.Fatalf("expected required and optional usage lines, got:\n%s", stdout)
	}
	if requiredIndex > optionalIndex {
		t.Fatalf("expected required flags before optional flags, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "    [--profile <string>]") {
		t.Fatalf("expected inherited global flags to be bracketed in usage, got:\n%s", stdout)
	}

	stdout, _, err = executeForTest("db", "create-db-cluster", "help")
	if err != nil {
		t.Fatalf("expected db create help to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "    [--project-id <string>]") {
		t.Fatalf("expected --project-id to be optional, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "    [--wait]") {
		t.Fatalf("expected --wait to be optional, got:\n%s", stdout)
	}
	for _, want := range []string{
		"--db-cluster-name <string> (required)",
		"--db-cluster-type <string>",
		"--db-cluster-type <string> (required)",
		"--project-id <string>",
		"--monthly-spending-limit-usd-cents <int32>",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected formatted flag %q, got:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "--project-id <string> (required)") {
		t.Fatalf("optional --project-id must not be marked required, got:\n%s", stdout)
	}
	stdout, _, err = executeForTest("db", "delete-db-cluster", "help")
	if err != nil {
		t.Fatalf("expected db delete help to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "    [--wait]") {
		t.Fatalf("expected --wait to be optional, got:\n%s", stdout)
	}

	stdout, _, err = executeForTest("db", "create-db-cluster-branch", "help")
	if err != nil {
		t.Fatalf("expected db branch create help to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "    [--wait]") {
		t.Fatalf("expected branch --wait to be optional, got:\n%s", stdout)
	}

	stdout, _, err = executeForTest("fs", "create-file-system", "help")
	if err != nil {
		t.Fatalf("expected fs create help to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "    [--wait]") {
		t.Fatalf("expected --wait to be optional, got:\n%s", stdout)
	}

	stdout, _, err = executeForTest("update", "help")
	if err != nil {
		t.Fatalf("expected update help to succeed, got %v", err)
	}
	if strings.Contains(stdout, "--yes") {
		t.Fatalf("update help should not expose the removed --yes flag:\n%s", stdout)
	}
}

func TestNoCommandDefinesShortFlags(t *testing.T) {
	root := NewRootCommand(testVersion())
	visitCommands(root, func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		cmd.InitDefaultVersionFlag()
	})
	if HasShorthand(root) {
		t.Fatal("expected command tree to avoid short flags")
	}
}

func TestServiceCommandsDeclarePermissions(t *testing.T) {
	root := NewRootCommand(testVersion())
	visitCommands(root, func(cmd *cobra.Command) {
		if cmd == root || cmd.Name() == "help" || cmd.HasSubCommands() {
			return
		}
		path := cmd.CommandPath()
		if !strings.HasPrefix(path, "tdc db ") &&
			!strings.HasPrefix(path, "tdc fs ") &&
			!strings.HasPrefix(path, "tdc fs-git ") &&
			!strings.HasPrefix(path, "tdc fs-journal ") &&
			!strings.HasPrefix(path, "tdc fs-vault ") &&
			!strings.HasPrefix(path, "tdc organization ") {
			return
		}
		if _, err := authz.ForCommand(path); err != nil {
			t.Fatalf("missing permission for %s: %v", path, err)
		}
	})
}

func TestFSUnixAliasesResolveToCanonicalCommands(t *testing.T) {
	root := NewRootCommand(testVersion())
	fs := findChildCommand(root, "fs")
	if fs == nil {
		t.Fatal("missing fs command")
	}

	tests := []struct {
		canonical string
		alias     string
	}{
		{canonical: "copy-file", alias: "cp"},
		{canonical: "read-file", alias: "cat"},
		{canonical: "list-files", alias: "ls"},
		{canonical: "describe-file", alias: "stat"},
		{canonical: "move-file", alias: "mv"},
		{canonical: "delete-file", alias: "rm"},
		{canonical: "create-directory", alias: "mkdir"},
		{canonical: "chmod-file", alias: "chmod"},
		{canonical: "create-symlink", alias: "symlink"},
		{canonical: "create-hardlink", alias: "hardlink"},
		{canonical: "search-file-content", alias: "grep"},
		{canonical: "find-files", alias: "find"},
		{canonical: "mount-file-system", alias: "mount"},
		{canonical: "drain-file-system", alias: "drain"},
		{canonical: "unmount-file-system", alias: "umount"},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			canonical := findChildCommand(fs, tt.canonical)
			if canonical == nil {
				t.Fatalf("missing canonical command %s", tt.canonical)
			}
			if !containsString(canonical.Aliases, tt.alias) {
				t.Fatalf("expected %s aliases to contain %q, got %#v", tt.canonical, tt.alias, canonical.Aliases)
			}

			resolved, _, err := root.Find([]string{"fs", tt.alias})
			if err != nil {
				t.Fatalf("resolve alias: %v", err)
			}
			if resolved.Name() != tt.canonical {
				t.Fatalf("expected alias %s to resolve to %s, got %s", tt.alias, tt.canonical, resolved.Name())
			}
			path := resolved.CommandPath()
			if want := "tdc fs " + tt.canonical; path != want {
				t.Fatalf("expected canonical path %q, got %q", want, path)
			}
			if _, err := authz.ForCommand(path); err != nil {
				t.Fatalf("alias %s resolved to command without permission: %v", tt.alias, err)
			}

			stdout, _, err := executeForTest("fs", tt.alias, "help")
			if err != nil {
				t.Fatalf("expected alias help to succeed, got %v", err)
			}
			if !strings.Contains(stdout, "Aliases:") || !strings.Contains(stdout, "  "+tt.alias) {
				t.Fatalf("expected alias help to list %q, got:\n%s", tt.alias, stdout)
			}
		})
	}
}

func TestFSAdjunctCommandsRequireConfiguredFSResource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	writeCompleteProfile(t, home, "default")

	_, _, err := executeForTest("fs-vault", "list-secrets")
	if err == nil {
		t.Fatal("expected missing tdc fs resource to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected config exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "file system name is required") || !strings.Contains(got, "TDC_FS_FILE_SYSTEM_NAME") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestFSOperationalCommandsExposeResourceSelector(t *testing.T) {
	root := NewRootCommand(testVersion())
	excluded := map[string]bool{
		"tdc fs list-file-systems":   true,
		"tdc fs drain-file-system":   true,
		"tdc fs unmount-file-system": true,
		"tdc fs-vault unmount-vault": true,
	}
	visitCommands(root, func(cmd *cobra.Command) {
		if cmd.Name() == "help" || cmd.HasSubCommands() || excluded[cmd.CommandPath()] {
			return
		}
		path := cmd.CommandPath()
		if !strings.HasPrefix(path, "tdc fs ") && !strings.HasPrefix(path, "tdc fs-git ") && !strings.HasPrefix(path, "tdc fs-journal ") && !strings.HasPrefix(path, "tdc fs-vault ") {
			return
		}
		if cmd.Flags().Lookup("file-system-name") == nil {
			t.Fatalf("%s does not expose --file-system-name", path)
		}
	})
}

func TestFSRemoteCommandsExposeTokenFlag(t *testing.T) {
	root := NewRootCommand(testVersion())
	excluded := map[string]bool{
		"tdc fs create-file-system":   true,
		"tdc fs list-file-systems":    true,
		"tdc fs describe-file-system": true,
		"tdc fs drain-file-system":    true,
		"tdc fs unmount-file-system":  true,
		"tdc fs-vault unmount-vault":  true,
	}
	visitCommands(root, func(cmd *cobra.Command) {
		if cmd.Name() == "help" || cmd.HasSubCommands() || excluded[cmd.CommandPath()] {
			return
		}
		path := cmd.CommandPath()
		if !strings.HasPrefix(path, "tdc fs ") && !strings.HasPrefix(path, "tdc fs-git ") && !strings.HasPrefix(path, "tdc fs-journal ") && !strings.HasPrefix(path, "tdc fs-vault ") {
			return
		}
		if cmd.Flags().Lookup("fs-token") == nil {
			t.Fatalf("%s does not expose --fs-token", path)
		}
	})
}

func TestFSRegistryDryRunDoesNotMigrateLegacyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	if err := store.WriteProfile(home, "default", store.ConfigProfile{
		RegionCode:      "aws-us-east-1",
		FSResourceName:  "workspace",
		FSTenantID:      "tenant-1",
		FSCloudProvider: "aws",
		FSRegionCode:    "aws-us-east-1",
	}, store.CredentialsProfile{TDCPublicKey: "public", TDCPrivateKey: "private", FSAPIKey: "key-1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeForTest("fs", "copy-file", "--file-system-name", "workspace", "--from-remote", "/source", "--to-remote", "/target", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, store.TDCDirName, "fs_resources")); !os.IsNotExist(err) {
		t.Fatalf("dry-run migrated legacy state: %v", err)
	}
}

func TestFSDefaultCommandSurfaceIsRemoved(t *testing.T) {
	for _, args := range [][]string{
		{"fs", "set-default-file-system"},
		{"fs", "unset-default-file-system"},
		{"fs", "create-file-system", "--file-system-name", "workspace", "--set-default"},
	} {
		_, _, err := executeForTest(args...)
		if err == nil {
			t.Fatalf("expected removed command surface %v to fail", args)
		}
		if got := apperr.ExitCodeFor(err); got != 2 {
			t.Fatalf("%v exit code = %d, want 2: %v", args, got, err)
		}
	}
}

func TestUpdateCheckUsesReleaseMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"tag_name": "v0.1.0",
			"html_url": "https://github.com/tidbcloud/tdc/releases/tag/v0.1.0",
			"assets": [
				{
					"name": "tdc_linux_amd64.tar.gz",
					"browser_download_url": "https://github.com/tidbcloud/tdc/releases/download/v0.1.0/tdc_linux_amd64.tar.gz"
				}
			]
		}`))
	}))
	defer server.Close()
	t.Setenv("TDC_RELEASE_API_BASE_URL", server.URL)

	stdout, _, err := executeForTest("update", "--check", "--query", "latest_version")
	if err != nil {
		t.Fatalf("expected update --check to succeed, got %v", err)
	}
	if got := stdout; got != "\"0.1.0\"\n" {
		t.Fatalf("unexpected output %q", got)
	}
}

func TestUpdateCheckRejectsApplyFlags(t *testing.T) {
	_, _, err := executeForTest("update", "--check", "--dry-run")
	if err == nil {
		t.Fatal("expected update --check --dry-run to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "--dry-run cannot be used with --check") {
		t.Fatalf("unexpected message: %q", got)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestCLIUpdateRefusesUnownedLocalBuild(t *testing.T) {
	_, _, err := executeForTest("update", "--dry-run")
	if err == nil {
		t.Fatal("expected local build update to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "not owned by tdc") {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestControlPlaneCommandSpecRendersImplementedResult(t *testing.T) {
	root := newCommand(commandSpec{
		Use: "tdc",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}, testVersion())
	root.PersistentFlags().String("output", "json", "output format")
	root.PersistentFlags().String("query", "", "JMESPath query applied to JSON output")
	root.AddCommand(newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "implemented-command",
		Short:      "Implemented command.",
		Mutation:   readOnlyCommand,
		Permission: authz.OrganizationProjectRead,
		Run: func(commandContext) (any, error) {
			return map[string]any{
				"items": []map[string]string{
					{"id": "item-1"},
				},
			}, nil
		},
	}, testVersion()))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Execute(context.Background(), root, testVersion(), []string{"implemented-command", "--query", "items[0].id"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected implemented command to succeed, got %v", err)
	}
	if got := stdout.String(); got != "\"item-1\"\n" {
		t.Fatalf("unexpected output %q", got)
	}
	if stderr.String() != "" {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestControlPlaneCommandSpecUsesCustomDryRun(t *testing.T) {
	root := newCommand(commandSpec{Use: "tdc"}, testVersion())
	root.PersistentFlags().String("output", "json", "output format")
	root.PersistentFlags().String("query", "", "JMESPath query applied to JSON output")
	root.AddCommand(newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-resource",
		Short:      "Create a resource.",
		Mutation:   mutatingCommand,
		Permission: authz.StarterClusterCreate,
		Run: func(ctx commandContext) (any, error) {
			return nil, apperr.NotImplemented(ctx.CommandPath())
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			return dryrun.New(
				ctx.CommandPath(),
				"create_resource",
				dryrun.RequestSummary{
					Method: "POST",
					Path:   "/v1/resources",
					Body: map[string]string{
						"name": "demo",
					},
				},
			), nil
		},
	}, testVersion()))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Execute(context.Background(), root, testVersion(), []string{"create-resource", "--dry-run", "--query", "request.path"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected custom dry-run to succeed, got %v", err)
	}
	if got := stdout.String(); got != "\"/v1/resources\"\n" {
		t.Fatalf("unexpected output %q", got)
	}
	if stderr.String() != "" {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}

func TestMutatingControlPlaneDryRunRendersJSON(t *testing.T) {
	withConfigEnv(t)

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", "demo-cluster", "--project-id", "project-1", "--wait", "--dry-run")
	if err != nil {
		t.Fatalf("expected dry-run to succeed, got %v", err)
	}

	var got struct {
		DryRun           bool   `json:"dry_run"`
		Command          string `json:"command"`
		Operation        string `json:"operation"`
		WouldSendRequest bool   `json:"would_send_request"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode dry-run output: %v\n%s", err, stdout)
	}
	if !got.DryRun || !got.WouldSendRequest {
		t.Fatalf("unexpected dry-run flags: %+v", got)
	}
	if got.Command != "tdc db create-db-cluster" {
		t.Fatalf("unexpected command %q", got.Command)
	}
	if got.Operation != "create_db_cluster" {
		t.Fatalf("unexpected operation %q", got.Operation)
	}
	if !strings.Contains(stdout, "operation_permission") || !strings.Contains(stdout, string(authz.StarterClusterCreate)) {
		t.Fatalf("dry-run output did not include permission requirement:\n%s", stdout)
	}
	if !strings.Contains(stdout, "post_create_wait") || !strings.Contains(stdout, "ACTIVE") {
		t.Fatalf("dry-run output did not include the requested wait:\n%s", stdout)
	}
}

func TestRegionOverrideWinsOverEnvironmentCredentials(t *testing.T) {
	t.Setenv("TDC_REGION_CODE", "aws-us-east-1")
	t.Setenv("TDC_PUBLIC_KEY", "test-public")
	t.Setenv("TDC_PRIVATE_KEY", "test-private")

	stdout, _, err := executeForTest("--region", "aws-ap-southeast-1", "db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run")
	if err != nil {
		t.Fatalf("expected dry-run to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "aws ap-southeast-1") {
		t.Fatalf("expected region override in endpoint check, got:\n%s", stdout)
	}
}

func TestRegionOverrideAllowsEnvironmentCredentialsWithoutEnvRegion(t *testing.T) {
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "test-public")
	t.Setenv("TDC_PRIVATE_KEY", "test-private")

	stdout, _, err := executeForTest("--region", "ali-ap-southeast-1", "db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run")
	if err != nil {
		t.Fatalf("expected dry-run to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "alibaba_cloud ap-southeast-1") {
		t.Fatalf("expected region override in endpoint check, got:\n%s", stdout)
	}
}

func TestCreateClusterUsesConfiguredDefaultProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	writeCompleteProfile(t, home, "default")

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--dry-run")
	if err != nil {
		t.Fatalf("expected profile project fallback to succeed: %v", err)
	}
	if !strings.Contains(stdout, `"tidb.cloud/project": "virtual-test"`) {
		t.Fatalf("dry-run did not use configured project:\n%s", stdout)
	}
}

func TestCreateClusterAllowsMissingConfiguredProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	if err := store.WriteProfile(home, "default", store.ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, store.CredentialsProfile{
		TDCPublicKey:  "test-public",
		TDCPrivateKey: "test-private",
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-type", "starter", "--db-cluster-name", "demo-cluster", "--dry-run")
	if err != nil {
		t.Fatalf("expected server-default project fallback to succeed: %v", err)
	}
	if strings.Contains(stdout, "tidb.cloud/project") || strings.Contains(stdout, `"labels"`) {
		t.Fatalf("dry-run unexpectedly included a project label:\n%s", stdout)
	}
}

func TestCreateClusterExplicitEmptyProjectDoesNotFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	writeCompleteProfile(t, home, "default")

	_, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "", "--dry-run")
	if apperr.CodeFor(err) != "db.empty_project_id" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDBTypeSelectedCommandsRequireExactStarter(t *testing.T) {
	withConfigEnv(t)

	_, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo", "--dry-run")
	if apperr.CodeFor(err) != "db.missing_required_flag" || !strings.Contains(apperr.MessageFor(err), "--db-cluster-type is required") {
		t.Fatalf("unexpected missing create type error: %v", err)
	}
	_, _, err = executeForTest("db", "list-db-clusters")
	if apperr.CodeFor(err) != "db.missing_required_flag" {
		t.Fatalf("unexpected missing list type error: %v", err)
	}
	_, _, err = executeForTest("db", "create-db-cluster", "--db-cluster-type", "STARTER", "--db-cluster-name", "demo", "--dry-run")
	if apperr.CodeFor(err) != "db.unsupported_cluster_type" {
		t.Fatalf("unexpected uppercase type error: %v", err)
	}
	_, _, err = executeForTest("db", "list-db-clusters", "--db-cluster-type", "starter", "--skip", "1")
	if err == nil || !strings.Contains(apperr.MessageFor(err), "unknown flag: --skip") {
		t.Fatalf("expected removed --skip to fail, got %v", err)
	}
}

func TestConfigureUsesTDCProfileNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_PROFILE", "stage")
	configureIAMForTest(t)

	stdout, _, err := executeForTest(
		"configure", "--non-interactive",
		"--region-code", "aws-us-east-1",
		"--tdc-public-key", "public",
		"--tdc-private-key", "private",
	)
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if !strings.Contains(stdout, `"profile": "stage"`) {
		t.Fatalf("configure did not select TDC_PROFILE:\n%s", stdout)
	}
	doc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if doc["stage"].ProjectID != "virtual-test" {
		t.Fatalf("stage project was not stored: %#v", doc)
	}
	if _, exists := doc["default"]; exists {
		t.Fatalf("configure unexpectedly wrote default profile: %#v", doc)
	}
}

func TestConfigureDoesNotDisplayTelemetryNotice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TDC_TELEMETRY", "off")
	configureIAMForTest(t)
	_, stderr, err := executeForTest(
		"configure", "--non-interactive",
		"--region-code", "aws-us-east-1",
		"--tdc-public-key", "public",
		"--tdc-private-key", "private",
	)
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if stderr != "" {
		t.Fatalf("configure unexpectedly wrote to stderr: %q", stderr)
	}
}

func TestExplicitEmptyRegionFails(t *testing.T) {
	_, _, err := executeForTest("--region", "", "db", "list-db-clusters")
	if err == nil {
		t.Fatal("expected empty region to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "--region cannot be empty") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestMutatingControlPlaneDryRunSupportsTextOutput(t *testing.T) {
	withConfigEnv(t)

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run", "--output", "text")
	if err != nil {
		t.Fatalf("expected dry-run to succeed, got %v", err)
	}
	if !strings.Contains(stdout, "Dry run: tdc db create-db-cluster") {
		t.Fatalf("unexpected text output:\n%s", stdout)
	}
}

func TestQueryAppliesToDryRunResult(t *testing.T) {
	withConfigEnv(t)

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run", "--query", "command")
	if err != nil {
		t.Fatalf("expected query to succeed, got %v", err)
	}
	if got := stdout; got != "\"tdc db create-db-cluster\"\n" {
		t.Fatalf("unexpected query output %q", got)
	}
}

func TestInvalidQueryFails(t *testing.T) {
	withConfigEnv(t)

	_, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run", "--query", "command[")
	if err == nil {
		t.Fatal("expected invalid query to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "invalid --query expression") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestDryRunRequiresConfigAndCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	writeConfigOnlyProfile(t, "default")

	_, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run")
	if err == nil {
		t.Fatal("expected missing config to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected exit code 3, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "authentication required") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestTDCProfileEnvironmentSelectsFileProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TDC_PROFILE", "stage")
	t.Setenv("TDC_REGION_CODE", "")
	t.Setenv("TDC_PUBLIC_KEY", "")
	t.Setenv("TDC_PRIVATE_KEY", "")
	writeCompleteProfile(t, home, "stage")

	stdout, _, err := executeForTest("db", "create-db-cluster", "--db-cluster-name", "demo-cluster", "--db-cluster-type", "starter", "--project-id", "project-1", "--dry-run")
	if err != nil {
		t.Fatalf("expected dry-run to succeed, got %v", err)
	}
	if !strings.Contains(stdout, `profile \"stage\" loaded`) {
		t.Fatalf("expected TDC_PROFILE to select stage profile, got:\n%s", stdout)
	}
}

func TestExplicitEmptyProfileFails(t *testing.T) {
	t.Setenv("TDC_PROFILE", "stage")

	_, _, err := executeForTest("db", "list-db-clusters", "--profile", "")
	if err == nil {
		t.Fatal("expected empty profile to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "--profile cannot be empty") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestDryRunRejectedOnReadOnlyCommand(t *testing.T) {
	_, _, err := executeForTest("db", "list-db-clusters", "--dry-run")
	if err == nil {
		t.Fatal("expected dry-run on read-only command to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "unknown flag: --dry-run") {
		t.Fatalf("unexpected message %q", got)
	}
}

func withConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TDC_REGION_CODE", "aws-us-east-1")
	t.Setenv("TDC_PUBLIC_KEY", "test-public")
	t.Setenv("TDC_PRIVATE_KEY", "test-private")
}

func writeCompleteProfile(t *testing.T, home, profileName string) {
	t.Helper()
	err := store.WriteProfile(home, profileName, store.ConfigProfile{
		RegionCode: "aws-us-east-1",
		ProjectID:  "virtual-test",
	}, store.CredentialsProfile{
		TDCPublicKey:  "test-public",
		TDCPrivateKey: "test-private",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeConfigOnlyProfile(t *testing.T, profileName string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	err := store.WriteProfile(home, profileName, store.ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, store.CredentialsProfile{})
	if err != nil {
		t.Fatal(err)
	}
}

func executeForTest(args ...string) (string, string, error) {
	return executeForTestWithInfo(testVersion(), args...)
}

func executeForTestWithInfo(info version.Info, args ...string) (string, string, error) {
	if _, ok := os.LookupEnv("TDC_LOGGING"); !ok {
		_ = os.Setenv("TDC_LOGGING", "off")
		defer os.Unsetenv("TDC_LOGGING")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := NewRootCommand(info)
	err := Execute(context.Background(), root, info, args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func configureIAMForTest(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/projects" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"projects":[{"id":"virtual-test","type":"tidbx_virtual"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("TDC_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv("TDC_TEST_IAM_BASE_URL", server.URL)
}

func testVersion() version.Info {
	return version.Info{
		Version:        "0.0.0-test",
		Commit:         "testcommit",
		Date:           "2026-07-02",
		OS:             "linux",
		Arch:           "amd64",
		InstallSource:  "local",
		ReleaseChannel: "stable",
	}
}
