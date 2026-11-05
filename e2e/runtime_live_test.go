package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
)

// The observer sees response frames only, never headers, credentials or argv.
// It forwards the original bytes unchanged and retains only started metadata.
type runtimeStartObserver struct {
	mu        sync.Mutex
	line      []byte
	oversized bool
	starts    []string
}

func (o *runtimeStartObserver) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			var f struct {
				Type    string `json:"type"`
				Sandbox string `json:"sandbox"`
			}
			if !o.oversized && json.Unmarshal(o.line, &f) == nil && f.Type == "started" {
				o.starts = append(o.starts, f.Sandbox)
			}
			o.line = nil
			o.oversized = false
		} else if len(o.line) < 8192 && !o.oversized {
			o.line = append(o.line, b)
		} else {
			o.line = nil
			o.oversized = true
		}
	}
	return len(p), nil
}
func (o *runtimeStartObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.starts = nil
	o.line = nil
	o.oversized = false
}
func (o *runtimeStartObserver) want(t *testing.T, sandbox string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.starts) != 1 || o.starts[0] != sandbox {
		t.Fatalf("started sandbox fields=%q, want exactly one %q", o.starts, sandbox)
	}
}

type runtimeObservedBody struct {
	io.Reader
	io.Closer
}

func TestRuntimeStartObserver(t *testing.T) {
	o := &runtimeStartObserver{}
	for _, part := range []string{"{\"type\":\"sta", "rted\"}\n", strings.Repeat("x", 9000), "\n{bad}\n"} {
		_, _ = o.Write([]byte(part))
	}
	o.want(t, "")
	o.reset()
	_, _ = o.Write([]byte("{\"type\":\"started\",\"sandbox\":\"new\"}\n"))
	o.want(t, "new")
}

func TestLiveRuntimeExec(t *testing.T) {
	requireLive(t)
	if os.Getenv("TI_LIVE_RUNTIME") != "1" {
		t.Skip("TI_LIVE_RUNTIME=1 is required; run make live-e2e-runtime")
	}
	bin := tiBinary(t)
	profile := ensureLiveFSResource(t, bin, liveProfileName(t))
	endpoint, err := endpoints.NewResolver().ResolveFS(profile.FSCloudProvider, profile.FSRegionCode)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(endpoint.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	observer := &runtimeStartObserver{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.URL.Path == "/v1/runtime/exec" && r.StatusCode == http.StatusOK {
			r.Body = runtimeObservedBody{io.TeeReader(r.Body, observer), r.Body}
		}
		return nil
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(endpoints.FSRegionManifest{Service: "ti-fs", Regions: []endpoints.FSRegionManifestEntry{{RegionCode: profile.FSPlacementRegionCode, Mode: endpoints.DefaultFSMode, ServerURL: server.URL, CloudProvider: profile.FSCloudProvider, TiDBRegion: profile.FSRegionCode}}})
	}))
	t.Cleanup(manifest.Close)
	env := append(liveFSTokenEnv(profile, t.TempDir()), "TI_TELEMETRY=off", "TI_ALLOW_TEST_ENDPOINTS=1", "TI_TEST_FS_MANIFEST_URL="+manifest.URL, "TI_EXEC_TEST_LOCAL=local value")
	run := func(t *testing.T, args ...string) commandResult {
		t.Helper()
		observer.reset()
		// Bound both remote execution and the local test. No execution is retried.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), env...)
		cmd.WaitDelay = 5 * time.Second
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		err := cmd.Run()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatalf("Runtime process failed: %v", err)
			}
		}
		if ctx.Err() != nil {
			t.Fatal("local deadline exceeded; remote outcome unconfirmed")
		}
		return commandResult{t: t, args: args, exitCode: code, stdout: out.String(), stderr: errOut.String()}
	}
	root := fmt.Sprintf("ti-runtime-%d-%d", os.Getpid(), time.Now().UnixNano())
	created := run(t, "fs", "create-directory", "--path", "/"+root)
	created.wantExitCode(0)
	t.Cleanup(func() { r := run(t, "fs", "delete-file", "--path", "/"+root, "--recursive"); r.wantExitCode(0) })
	setup := run(t, "exec", "--fresh", "-t", "60s", "-w", root, "sh", "-c", "mkdir sub; printf '%s' persisted > sub/state.txt")
	setup.wantExitCode(0)
	observer.want(t, "new")
	t.Run("lightweight list and read", func(t *testing.T) {
		r := run(t, "exec", "--read-only", "-w", root, "-C", "sub", "ls")
		r.wantExitCode(0)
		if r.stdout != "state.txt\n" {
			r.fail("unexpected directory contents")
		}
		observer.want(t, "")
		r = run(t, "exec", "--read-only", "-w", root, "-C", "sub", "cat", "state.txt")
		r.wantExitCode(0)
		if r.stdout != "persisted" {
			r.fail("workspace content mismatch")
		}
		observer.want(t, "")
	})
	t.Run("python falls back to sandbox", func(t *testing.T) {
		r := run(t, "exec", "-w", root, "python3", "-c", "from pathlib import Path; Path('result.txt').write_text('total=42\\n'); print('done')")
		r.wantExitCode(0)
		if r.stdout != "done\n" {
			r.fail("Python command did not complete")
		}
		observer.mu.Lock()
		starts := append([]string(nil), observer.starts...)
		observer.mu.Unlock()
		if len(starts) != 1 || (starts[0] != "new" && starts[0] != "warm") {
			t.Fatalf("Python sandbox fields=%q", starts)
		}
		r = run(t, "fs", "read-file", "--path", "/"+root+"/result.txt")
		r.wantExitCode(0)
		if r.stdout != "total=42\n" {
			r.fail("Python result not persisted")
		}
	})
	t.Run("fresh sandbox preserves files", func(t *testing.T) {
		r := run(t, "exec", "--fresh", "-w", root, "-C", "sub", "cat", "state.txt")
		r.wantExitCode(0)
		if r.stdout != "persisted" {
			r.fail("fresh sandbox lost Filesystem data")
		}
		observer.want(t, "new")
	})
	t.Run("read only rejects writes", func(t *testing.T) {
		r := run(t, "exec", "--fresh", "--read-only", "-w", root, "sh", "-c", "printf changed > sub/state.txt")
		r.wantExitCode(2)
		observer.want(t, "new")
		if !strings.Contains(strings.ToLower(r.stderr), "read-only file system") {
			r.fail("write failed without a read-only denial")
		}
		r = run(t, "fs", "read-file", "--path", "/"+root+"/sub/state.txt")
		r.wantExitCode(0)
		if r.stdout != "persisted" {
			r.fail("rejected write changed persisted data")
		}
	})
	t.Run("streams and arguments", func(t *testing.T) {
		r := run(t, "exec", "printf 'out\\n'; printf 'err\\n' >&2")
		r.wantExitCode(0)
		if r.stdout != "out\n" || r.stderr != "err\n" {
			r.fail("stdout/stderr changed or merged")
		}
		r = run(t, "exec", "printf", "%s\n", "two words", "--help")
		r.wantExitCode(0)
		if r.stdout != "two words\n--help\n" {
			r.fail("remote argv changed")
		}
		r = run(t, "exec", "-e", "TI_EXEC_TEST=explicit value", "-e", "TI_EXEC_TEST_LOCAL", "sh", "-c", "printf '%s|%s' \"$TI_EXEC_TEST\" \"$TI_EXEC_TEST_LOCAL\"")
		r.wantExitCode(0)
		if r.stdout != "explicit value|local value" {
			r.fail("remote environment changed")
		}
	})
	t.Run("exit statuses", func(t *testing.T) {
		for _, v := range []struct {
			command string
			code    int
		}{{"exit 17", 17}, {"ti-runtime-no-such-command", 127}} {
			r := run(t, "exec", v.command)
			r.wantExitCode(v.code)
			r.wantStderrNotContains("ti [ERROR]")
		}
	})
	t.Run("timeout preserves completed writes", func(t *testing.T) {
		r := run(t, "exec", "-w", root, "-t", "15s", "sh", "-c", "printf before > before-timeout; echo ready; sleep 60; printf late > after-timeout")
		r.wantExitCode(124)
		if r.stdout != "ready\n" {
			r.fail("timeout did not exercise a started command")
		}
		r = run(t, "exec", "--read-only", "-w", root, "sh", "-c", "cat before-timeout; test ! -e after-timeout")
		r.wantExitCode(0)
		if r.stdout != "before" {
			r.fail("pre-timeout write not persisted")
		}
	})
	t.Run("parent cancellation preserves writes", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("native Windows signal validation is separate")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "exec", "-w", root, "-t", "60s", "sh", "-c", "printf before > before-cancel; echo ready; sleep 90; printf late > after-cancel")
		cmd.Env = append(os.Environ(), env...)
		cmd.WaitDelay = 5 * time.Second
		ready := make(chan struct{}, 1)
		cmd.Stdout = &runtimeReadyWriter{ready: ready}
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ready:
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
		case err := <-done:
			t.Fatalf("command exited before readiness: %v", err)
		case <-ctx.Done():
			<-done
			t.Fatal("command did not become ready")
		}
		err := <-done
		if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 130 {
			t.Fatalf("cancel exit=%v stderr=%q", err, errOut.String())
		}
		r := run(t, "exec", "--read-only", "-w", root, "sh", "-c", "cat before-cancel; test ! -e after-cancel")
		r.wantExitCode(0)
		if r.stdout != "before" {
			r.fail("pre-cancel write not persisted")
		}
	})
}

type runtimeReadyWriter struct {
	ready  chan struct{}
	prefix []byte
}

func (w *runtimeReadyWriter) Write(p []byte) (int, error) {
	if len(w.prefix) < 6 {
		n := min(6-len(w.prefix), len(p))
		w.prefix = append(w.prefix, p[:n]...)
		if string(w.prefix) == "ready\n" {
			select {
			case w.ready <- struct{}{}:
			default:
			}
		}
	}
	return len(p), nil
}
