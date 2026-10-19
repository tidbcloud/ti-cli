package runtimeexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

func TestCompanionArgsPreserveRuntimeContract(t *testing.T) {
	opts := Options{
		Workspace: ":/repo", WorkspaceSet: true,
		Cwd: "web", CwdSet: true,
		Environment: []string{"CI=1", "TOKEN"},
		Timeout:     "20m", TimeoutSet: true,
		Fresh: true, Layer: "tag:agent=one", LayerSet: true,
		ReadOnly: true, Verbose: true,
		Command: []string{"make", "-j8", "test", "--output", "text"},
	}
	want := []string{
		"exec", "--workspace", ":/repo", "--cwd", "web",
		"--env", "CI=1", "--env", "TOKEN", "--timeout", "20m",
		"--fresh", "--layer", "tag:agent=one", "--read-only", "--verbose",
		"--", "make", "-j8", "test", "--output", "text",
	}
	if got := companionArgs(opts); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("companion args = %#v, want %#v", got, want)
	}
}

func TestExecStreamsAndUsesResourceScopedEnvironment(t *testing.T) {
	companion, recordPath := buildRuntimeCompanion(t)
	home := t.TempDir()
	profile := runtimeProfile(home)
	t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
	t.Setenv("TI_TEST_RUNTIME_MODE", "success")
	t.Setenv("TI_FS_TOKEN", "ambient-ti-token")
	t.Setenv("TIDB_CLOUD_PRIVATE_KEY", "ambient-private")
	t.Setenv("DRIVE9_API_KEY", "ambient-drive9-token")

	var stdout, stderr, debug bytes.Buffer
	service := testService(home, companion, &stdout, &stderr)
	service.Debug = true
	service.DebugWriter = &debug
	err := service.Exec(context.Background(), Options{
		Profile: profile, Workspace: "repo", WorkspaceSet: true,
		Environment: []string{"CI=1"}, Command: []string{"printf", "ok"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if stdout.String() != "runtime stdout\n" || stderr.String() != "runtime stderr\n" {
		t.Fatalf("streams changed or merged: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if debug.String() != "ti [DEBUG]: starting Runtime companion\n" {
		t.Fatalf("debug output = %q", debug.String())
	}
	for _, secret := range []string{"selected-api-key", "ambient-ti-token", "ambient-private", "ambient-drive9-token", "printf", "CI=1"} {
		if strings.Contains(debug.String(), secret) {
			t.Fatalf("debug output leaked %q: %q", secret, debug.String())
		}
	}

	records := readRuntimeRecords(t, recordPath)
	if len(records) != 2 || records[0].Kind != "help" || records[1].Kind != "exec" {
		t.Fatalf("records = %#v, want one capability check and one exec", records)
	}
	preflight := records[0]
	if preflight.APIKey != "" || preflight.Server != "" || preflight.Region != "" || preflight.TIFSToken != "" || preflight.TiDBPrivate != "" {
		t.Fatalf("capability preflight received credentials or remote placement: %#v", preflight)
	}
	wantHome, err := fscred.CompanionHome(home, profile.Name, profile.FSResourceName)
	if err != nil {
		t.Fatal(err)
	}
	execution := records[1]
	if execution.Home != wantHome || execution.APIKey != "selected-api-key" || execution.Server != "https://fs.test" || execution.Region != "aws-us-east-1" {
		t.Fatalf("execution environment = %#v", execution)
	}
	if execution.TIFSToken != "" || execution.TiDBPrivate != "" {
		t.Fatalf("ambient ti credentials leaked: %#v", execution)
	}
}

func TestExecPreservesTerminalExitAndNeverRetries(t *testing.T) {
	for _, test := range []struct {
		mode string
		code int
	}{
		{mode: "nonzero", code: 23},
		{mode: "timeout", code: 124},
		{mode: "canceled", code: 130},
		{mode: "unknown", code: 255},
	} {
		t.Run(test.mode, func(t *testing.T) {
			companion, recordPath := buildRuntimeCompanion(t)
			home := t.TempDir()
			t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
			t.Setenv("TI_TEST_RUNTIME_MODE", test.mode)
			var stdout, stderr bytes.Buffer
			err := testService(home, companion, &stdout, &stderr).Exec(context.Background(), Options{
				Profile: runtimeProfile(home), Command: []string{"run-once"},
			})
			if got := apperr.ExitCodeFor(err); got != test.code || !apperr.IsSilent(err) {
				t.Fatalf("error = %#v, exit=%d silent=%t; want %d and silent", err, got, apperr.IsSilent(err), test.code)
			}
			records := readRuntimeRecords(t, recordPath)
			executions := 0
			for _, record := range records {
				if record.Kind == "exec" {
					executions++
				}
			}
			if executions != 1 {
				t.Fatalf("execution attempts = %d, want 1; records=%#v", executions, records)
			}
		})
	}
}

func TestExecRejectsIncompatibleCompanionBeforeExecution(t *testing.T) {
	companion, recordPath := buildRuntimeCompanion(t)
	home := t.TempDir()
	t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
	t.Setenv("TI_TEST_RUNTIME_MODE", "incompatible")
	err := testService(home, companion, &bytes.Buffer{}, &bytes.Buffer{}).Exec(context.Background(), Options{
		Profile: runtimeProfile(home), Command: []string{"must-not-run"},
	})
	if apperr.CodeFor(err) != "runtime.companion_incompatible" {
		t.Fatalf("error = %v, code=%q", err, apperr.CodeFor(err))
	}
	records := readRuntimeRecords(t, recordPath)
	if len(records) != 1 || records[0].Kind != "help" {
		t.Fatalf("incompatible companion started an execution: %#v", records)
	}
	if records[0].APIKey != "" || records[0].Server != "" || records[0].Region != "" {
		t.Fatalf("incompatible companion preflight received credentials or placement: %#v", records[0])
	}
}

func TestExecCapabilityUsesExitStatusNotHelpText(t *testing.T) {
	companion, recordPath := buildRuntimeCompanion(t)
	home := t.TempDir()
	t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
	t.Setenv("TI_TEST_RUNTIME_MODE", "alternate-help")
	err := testService(home, companion, &bytes.Buffer{}, &bytes.Buffer{}).Exec(context.Background(), Options{
		Profile: runtimeProfile(home), Command: []string{"run-once"},
	})
	if err != nil {
		t.Fatalf("successful capability status was rejected because of help prose: %v", err)
	}
	records := readRuntimeRecords(t, recordPath)
	if len(records) != 2 || records[0].Kind != "help" || records[1].Kind != "exec" {
		t.Fatalf("records = %#v, want capability gate followed by one execution", records)
	}
}

func TestExecPreservesMissingCompanionError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TI_DRIVE9_BIN", filepath.Join(t.TempDir(), "missing-ti-drive9"))
	err := testService(home, "", &bytes.Buffer{}, &bytes.Buffer{}).Exec(context.Background(), Options{
		Profile: runtimeProfile(home), Command: []string{"must-not-run"},
	})
	if apperr.CodeFor(err) != "fs.companion_missing" || !strings.Contains(apperr.MessageFor(err), "ti requires the Drive9 companion binary") {
		t.Fatalf("missing companion error = %#v", err)
	}
}

func TestExecPreservesCancellationDuringCapabilityCheck(t *testing.T) {
	companion, recordPath := buildRuntimeCompanion(t)
	home := t.TempDir()
	t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := testService(home, companion, &bytes.Buffer{}, &bytes.Buffer{}).Exec(ctx, Options{
		Profile: runtimeProfile(home), Command: []string{"must-not-run"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if _, statErr := os.Stat(recordPath); !os.IsNotExist(statErr) {
		t.Fatalf("canceled capability check invoked companion: %v", statErr)
	}
}

func TestExecTreatsStreamFailureAsUnknownWithoutRetry(t *testing.T) {
	companion, recordPath := buildRuntimeCompanion(t)
	home := t.TempDir()
	t.Setenv("TI_TEST_RUNTIME_RECORD", recordPath)
	t.Setenv("TI_TEST_RUNTIME_MODE", "success")
	service := Service{
		HomeDir: home, CompanionPath: companion, Stdout: failingWriter{}, Stderr: &bytes.Buffer{},
		Resolver: endpoints.Resolver{FSBaseURLs: map[endpoints.ProviderRegion]string{
			{Provider: "aws", Region: "us-east-1"}: "https://fs.test",
		}},
	}
	err := service.Exec(context.Background(), Options{
		Profile: runtimeProfile(home), Command: []string{"run-once"},
	})
	if apperr.CodeFor(err) != "runtime.outcome_unknown" || apperr.ExitCodeFor(err) != 255 || apperr.IsSilent(err) {
		t.Fatalf("stream error = %#v, code=%q exit=%d silent=%t", err, apperr.CodeFor(err), apperr.ExitCodeFor(err), apperr.IsSilent(err))
	}
	records := readRuntimeRecords(t, recordPath)
	executions := 0
	for _, record := range records {
		if record.Kind == "exec" {
			executions++
		}
	}
	if executions != 1 {
		t.Fatalf("execution attempts = %d, want 1; records=%#v", executions, records)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("test stream failed")
}

func testService(home, companion string, stdout, stderr *bytes.Buffer) Service {
	return Service{
		HomeDir: home, CompanionPath: companion, Stdout: stdout, Stderr: stderr,
		Resolver: endpoints.Resolver{FSBaseURLs: map[endpoints.ProviderRegion]string{
			{Provider: "aws", Region: "us-east-1"}: "https://fs.test",
		}},
	}
}

func runtimeProfile(home string) *config.Profile {
	return &config.Profile{
		Name: "stage", HomeDir: home, FSResourceName: "tenant-runtime", FSTenantID: "tenant-runtime",
		FSPlacementRegionCode: "aws-us-east-1", FSCloudProvider: "aws", FSRegionCode: "us-east-1", FSAPIKey: "selected-api-key",
	}
}

type runtimeRecord struct {
	Kind        string   `json:"kind"`
	Args        []string `json:"args"`
	Home        string   `json:"home"`
	APIKey      string   `json:"api_key"`
	Server      string   `json:"server"`
	Region      string   `json:"region"`
	TIFSToken   string   `json:"ti_fs_token"`
	TiDBPrivate string   `json:"tidb_private"`
}

func readRuntimeRecords(t *testing.T, path string) []runtimeRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []runtimeRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record runtimeRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func buildRuntimeCompanion(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte(runtimeCompanionSource), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "fake-runtime"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	command := exec.Command("go", "build", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Runtime companion: %v\n%s", err, output)
	}
	return binary, filepath.Join(dir, "calls.jsonl")
}

const runtimeCompanionSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type record struct {
	Kind string ` + "`json:\"kind\"`" + `
	Args []string ` + "`json:\"args\"`" + `
	Home string ` + "`json:\"home\"`" + `
	APIKey string ` + "`json:\"api_key\"`" + `
	Server string ` + "`json:\"server\"`" + `
	Region string ` + "`json:\"region\"`" + `
	TIFSToken string ` + "`json:\"ti_fs_token\"`" + `
	TiDBPrivate string ` + "`json:\"tidb_private\"`" + `
}

func main() {
	args := os.Args[1:]
	kind := "other"
	if len(args) >= 2 && args[0] == "exec" && args[1] == "--help" { kind = "help" }
	if len(args) > 0 && args[0] == "exec" && kind != "help" { kind = "exec" }
	if file, err := os.OpenFile(os.Getenv("TI_TEST_RUNTIME_RECORD"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		_ = json.NewEncoder(file).Encode(record{Kind: kind, Args: args, Home: os.Getenv("HOME"), APIKey: os.Getenv("DRIVE9_API_KEY"), Server: os.Getenv("DRIVE9_SERVER"), Region: os.Getenv("DRIVE9_REGION_CODE"), TIFSToken: os.Getenv("TI_FS_TOKEN"), TiDBPrivate: os.Getenv("TIDB_CLOUD_PRIVATE_KEY")})
		_ = file.Close()
	}
	mode := os.Getenv("TI_TEST_RUNTIME_MODE")
	if kind == "help" {
		if mode == "incompatible" { fmt.Fprintln(os.Stderr, "drive9: unknown command exec"); os.Exit(2) }
		if mode == "alternate-help" { fmt.Fprintln(os.Stderr, "human help may change"); return }
		fmt.Fprintln(os.Stderr, "usage: drive9 exec [flags] [--] command [arg...]")
		return
	}
	if kind != "exec" { os.Exit(2) }
	fmt.Fprintln(os.Stdout, "runtime stdout")
	fmt.Fprintln(os.Stderr, "runtime stderr")
	switch mode {
	case "nonzero": os.Exit(23)
	case "timeout": os.Exit(124)
	case "canceled": os.Exit(130)
	case "unknown": os.Exit(255)
	}
}
`
