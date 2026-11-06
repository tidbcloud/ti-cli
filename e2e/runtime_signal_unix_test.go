//go:build !windows

package e2e

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeExecSignals(t *testing.T) {
	bin := tiBinary(t)
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build companion: %v\n%s", err, output)
	}
	for _, test := range []struct {
		name         string
		signal       syscall.Signal
		group, twice bool
	}{
		{"terminal interrupt", syscall.SIGINT, true, false},
		{"parent interrupt", syscall.SIGINT, false, false},
		{"parent termination", syscall.SIGTERM, false, false},
		{"second interrupt detaches", syscall.SIGINT, false, true},
		{"second terminal interrupt detaches", syscall.SIGINT, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode := "wait-for-signal"
			if test.twice {
				mode = "wait-for-second-signal"
			}
			cmd := exec.Command(bin, "exec", "wait")
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TI_LOGGING=off", "TI_TELEMETRY=off", "TI_ALLOW_TEST_ENDPOINTS=1", "TI_DRIVE9_BIN="+companion,
				"TI_FS_TOKEN="+drive9TestToken("tenant-runtime-signal"), "TI_REGION_CODE=aws-us-east-1", "FAKE_DRIVE9_EXEC_MODE="+mode)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines := make(chan string, 16)
			go func() {
				defer close(lines)
				scanner := bufio.NewScanner(pipe)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
			}()
			childPID := 0
			t.Cleanup(func() {
				if childPID != 0 {
					_ = syscall.Kill(childPID, syscall.SIGKILL)
				}
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			awaitLine := func(want string) {
				t.Helper()
				timer := time.NewTimer(10 * time.Second)
				defer timer.Stop()
				for {
					select {
					case line, ok := <-lines:
						if !ok {
							t.Fatalf("companion exited before %q", want)
						}
						if strings.HasPrefix(line, "runtime pid ") {
							childPID, _ = strconv.Atoi(strings.TrimPrefix(line, "runtime pid "))
						}
						if line == want {
							return
						}
					case <-timer.C:
						t.Fatalf("companion did not reach %q", want)
					}
				}
			}
			awaitLine("runtime ready")
			pid := cmd.Process.Pid
			if test.group {
				pid = -pid
			}
			if err := syscall.Kill(pid, test.signal); err != nil {
				t.Fatal(err)
			}
			if test.twice {
				awaitLine("runtime canceling")
				if err := syscall.Kill(pid, test.signal); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				_ = syscall.Kill(childPID, syscall.SIGKILL)
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("ti did not finish after interrupt")
			}
			childPID = 0
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 130 {
				t.Fatalf("exit=%v stderr=%q", err, stderr.String())
			}
			want := "runtime canceled"
			if test.twice {
				want = "runtime detached"
			}
			if !strings.Contains(stderr.String(), want) || strings.Contains(stderr.String(), "ti [ERROR]") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

func TestRuntimeCapabilityInterrupt(t *testing.T) {
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	if output, err := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, group := range []bool{false, true} {
		t.Run(strconv.FormatBool(group), func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "calls.jsonl")
			cmd := exec.Command(tiBinary(t), "exec", "must-not-run")
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TI_LOGGING=off", "TI_TELEMETRY=off", "TI_ALLOW_TEST_ENDPOINTS=1", "TI_DRIVE9_BIN="+companion, "FAKE_DRIVE9_RECORD="+record, "FAKE_DRIVE9_EXEC_MODE=slow-help")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			deadline := time.After(10 * time.Second)
			for {
				if data, err := os.ReadFile(record); err == nil && bytes.Contains(data, []byte("--help")) {
					break
				}
				select {
				case <-deadline:
					t.Fatal("capability probe did not start")
				case <-time.After(10 * time.Millisecond):
				}
			}
			pid := cmd.Process.Pid
			if group {
				pid = -pid
			}
			if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 130 || !strings.Contains(stderr.String(), "interrupted") || strings.Contains(stderr.String(), "does not support") {
				t.Fatalf("probe interrupt=%v stderr=%q", err, stderr.String())
			}
		})
	}
}
