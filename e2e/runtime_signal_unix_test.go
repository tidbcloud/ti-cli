//go:build !windows

package e2e

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeExecFirstInterruptReachesCompanion(t *testing.T) {
	bin := tiBinary(t)
	home := t.TempDir()
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Drive9 companion: %v\n%s", err, output)
	}

	cmd := exec.Command(bin, "exec", "wait")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"TI_LOGGING=off",
		"TI_DRIVE9_BIN="+companion,
		"TI_FS_TOKEN="+drive9TestToken("tenant-runtime-signal"),
		"TI_REGION_CODE=aws-us-east-1",
		"FAKE_DRIVE9_EXEC_MODE=wait-for-signal",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{})
	done := make(chan struct{})
	var stdout bytes.Buffer
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			stdout.WriteString(scanner.Text() + "\n")
			if scanner.Text() == "runtime ready" {
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		t.Fatalf("Runtime companion did not become ready; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	<-done
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 130 {
		t.Fatalf("ti exec after first interrupt = %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "runtime canceled") || strings.Contains(stderr.String(), "ti [ERROR]") {
		t.Fatalf("first interrupt did not preserve companion cancellation: %q", stderr.String())
	}
}
