package fs

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type fakeDrive9Call struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func buildFakeDrive9(t *testing.T) (binPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "calls.jsonl")
	sourcePath := filepath.Join(dir, "fake_drive9.go")
	source := `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type callRecord struct {
	Args []string          ` + "`json:\"args\"`" + `
	Env  map[string]string ` + "`json:\"env\"`" + `
}

func main() {
	if os.Getenv("TI_FAKE_ESCAPE_WORKER") == "1" {
		ms, _ := strconv.Atoi(os.Getenv("TI_FAKE_MOUNT_ESCAPE_DELAY_MS"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		root := os.Getenv("TI_FAKE_ESCAPE_ROOT")
		_ = os.MkdirAll(filepath.Join(root, "overlay"), 0o755)
		_ = os.Symlink(os.Getenv("TI_FAKE_ESCAPE_TARGET"), filepath.Join(root, "overlay", "evil"))
		return
	}
	args := os.Args[1:]
	record(args)
	if len(args) > 0 && args[0] == "--help" {
		fmt.Println("fake drive9 help")
		return
	}
	if len(args) == 0 {
		return
	}
	switch {
	case len(args) >= 3 && args[0] == "fs" && args[1] == "layer" && args[2] == "help":
		if os.Getenv("TI_FAKE_DRIVE9_OLD_LAYER") == "1" {
			fmt.Println("usage: drive9 fs layer <create|list|status|diff|checkpoint|rollback|commit>")
		} else {
			fmt.Println("usage: drive9 fs layer <create|list|status|diff|checkpoint|rollback|commit|fork|chain|delete>")
		}
	case len(args) >= 2 && args[0] == "mount" && args[1] == "--help":
		if os.Getenv("TI_FAKE_DRIVE9_OLD_LAYER") == "1" {
			fmt.Println("usage: drive9 mount [flags] [:/remote] <mountpoint>")
		} else {
			fmt.Println("-layer string")
			fmt.Println("-checkpoint string")
		}
	case len(args) >= 3 && args[0] == "fs" && args[1] == "layer" && args[2] == "fork":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"layer_id": "child-id", "name": "style-analyst", "state": "active", "base_root_path": "/research/q3-market",
			"parent_layer_id": "parent-id", "origin_seq": 12, "origin_checkpoint_id": "seed", "root_layer_id": "root-id", "depth": 1,
		})
	case len(args) >= 3 && args[0] == "fs" && args[1] == "layer" && args[2] == "chain":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"chain": []map[string]any{
			{"layer_id": "root-id", "name": "research-base", "state": "active", "depth": 0, "limit_seq": 12, "base_root_path": "/research/q3-market"},
			{"layer_id": "child-id", "name": "style-analyst", "state": "active", "depth": 1, "parent_layer_id": "root-id", "origin_seq": 12, "limit_seq": 12, "origin_checkpoint_id": "seed", "base_root_path": "/research/q3-market"},
		}})
	case len(args) >= 3 && args[0] == "fs" && args[1] == "layer" && args[2] == "delete":
		if os.Getenv("TI_FAKE_DRIVE9_DELETE_DESCENDANT") == "1" {
			fmt.Fprintln(os.Stderr, "fs layer delete: layer has live descendants")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "ok")
	case args[0] == "create":
		if path := os.Getenv("TI_FAKE_DRIVE9_BREAK_CREDENTIAL_ROOT"); path != "" {
			_ = os.RemoveAll(path)
			_ = os.WriteFile(path, []byte("not a directory"), 0600)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"tenant_id":      "tenant-1",
			"api_key":        "fs-secret",
			"status":         "active",
			"cloud_provider": "aws",
			"region_code":    os.Getenv("DRIVE9_REGION_CODE"),
			"server":         os.Getenv("DRIVE9_SERVER"),
		})
	case args[0] == "umount":
		if os.Getenv("TI_FAKE_UMOUNT_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "umount: mount is busy")
			os.Exit(1)
		}
	case args[0] == "mount" && (len(args) == 1 || args[1] != "drain"):
		fmt.Fprintln(os.Stderr, "component: drive9 mount")
		if flagValue(args, "--mode") == "fuse" {
			fmt.Fprintln(os.Stderr, "drive9: mount mode: fuse")
		} else {
			fmt.Fprintln(os.Stderr, "drive9: mount mode: webdav")
		}
		if os.Getenv("TI_FAKE_DRIVE9_MOUNT_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "mount: drive9 mount: background mount exited before becoming ready")
			os.Exit(1)
		}
		if mountPath := args[len(args)-1]; len(args) >= 2 && len(mountPath) > 0 && mountPath[0] != '-' {
			_ = os.MkdirAll(mountPath, 0o755)
			_ = os.WriteFile(mountPath+string(os.PathSeparator)+".drive9-mounted", []byte("fake mount ready\n"), 0o644)
		}
		if os.Getenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK") == "1" {
			root := flagValue(args, "--local-root")
			if root == "" {
				root = os.Getenv("TI_FAKE_ESCAPE_ROOT")
			}
			if root != "" {
				_ = os.MkdirAll(filepath.Join(root, "overlay"), 0o755)
				_ = os.Symlink(os.Getenv("TI_FAKE_ESCAPE_TARGET"), filepath.Join(root, "overlay", "evil"))
			}
		}
		if os.Getenv("TI_FAKE_MOUNT_ESCAPE_DELAY_MS") != "" {
			if root := flagValue(args, "--local-root"); root != "" && os.Getenv("TI_FAKE_ESCAPE_TARGET") != "" {
				// The mount command is a foreground client for a daemon, so the
				// delayed write must survive this process exiting: start a
				// detached worker that installs the link later.
				worker := exec.Command(os.Args[0])
				worker.Env = append(os.Environ(), "TI_FAKE_ESCAPE_WORKER=1", "TI_FAKE_ESCAPE_ROOT="+root)
				_ = worker.Start()
			}
		}
		if os.Getenv("TI_FAKE_DRIVE9_MOUNT_FAIL_AFTER_UNPACK") == "1" {
			fmt.Fprintln(os.Stderr, "mount: drive9 mount failed after unpack")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "drive9: mount running in background")
		fmt.Fprintln(os.Stderr, "drive9: unmount with drive9 umount /workspace")
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "delete":
		if os.Getenv("TI_FAKE_DRIVE9_NOT_FOUND") == "1" {
			fmt.Fprintln(os.Stderr, "delete admin tenant: HTTP 404: tenant not found")
			os.Exit(1)
		}
		if os.Getenv("TI_FAKE_DRIVE9_DELETE_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "admin tenant delete: backend unavailable")
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"tenant_id": "tenant-1", "status": "deleting"})
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "list":
		switch os.Getenv("TI_FAKE_DRIVE9_LIST_MODE") {
		case "empty":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"tenants": []any{}, "page": 1, "page_size": 100})
		case "mixed-local-token":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
					{"tenant_id": "tenant-2", "status": "active", "kind": "live"},
				},
				"page": 1, "page_size": 100,
			})
		case "malformed":
			fmt.Fprint(os.Stdout, "{")
		case "paginate":
			if flagValue(args, "--page") == "1" {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"tenants": []map[string]any{{"tenant_id": "tenant-2", "status": "active", "kind": "live"}},
					"page": 1, "page_size": 100, "next_page": 2,
				})
			} else {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
					"tenants": []map[string]any{{"tenant_id": "tenant-1", "status": "active", "kind": "live"}},
					"page": 2, "page_size": 100,
				})
			}
		case "regress":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []any{}, "page": 1, "page_size": 100, "next_page": 1,
			})
		case "page-mismatch":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []any{}, "page": 2, "page_size": 100,
			})
		case "duplicate":
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
					{"tenant_id": "tenant-1", "status": "active", "kind": "live"},
				},
				"page": 1, "page_size": 100,
			})
		default:
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"tenants": []map[string]any{{"tenant_id": "tenant-1", "status": "active", "kind": "live"}},
				"page": 1, "page_size": 100,
			})
		}
	case len(args) >= 3 && args[0] == "admin" && args[1] == "tenant" && args[2] == "get":
		if os.Getenv("TI_FAKE_DRIVE9_NOT_FOUND") == "1" {
			fmt.Fprintln(os.Stderr, "get admin tenant: HTTP 404: tenant not found")
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"tenant_id": "tenant-1", "status": "active", "kind": "live"})
	case len(args) >= 2 && args[0] == "vault" && args[1] == "get":
		if payload := os.Getenv("TI_FAKE_VAULT_GET"); payload != "" {
			fmt.Fprint(os.Stdout, payload)
		}
	case len(args) >= 2 && args[0] == "fs" && args[1] == "cat":
		fmt.Fprint(os.Stdout, "file bytes")
	case len(args) >= 2 && args[0] == "fs" && args[1] == "cp" && os.Getenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE") != "":
		sequencePath := os.Getenv("TI_FAKE_DRIVE9_CP_FAILURE_SEQUENCE")
		if _, err := os.Stat(sequencePath); os.IsNotExist(err) {
			_ = os.WriteFile(sequencePath, []byte("attempted"), 0600)
			fmt.Fprintln(os.Stderr, "fs cp: unexpected EOF")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "fs cp: remote resource not found")
		os.Exit(1)
	case len(args) >= 2 && args[0] == "fs" && args[1] == "stat":
		if expected := os.Getenv("TI_FAKE_DRIVE9_EXPECT_API_KEY"); expected != "" && os.Getenv("DRIVE9_API_KEY") != expected {
			fmt.Fprintln(os.Stderr, "fs stat: unauthorized")
			os.Exit(1)
		}
		if os.Getenv("TI_FAKE_DRIVE9_STAT_ALWAYS_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "fs stat: storage backend unavailable; resource is still provisioning")
			os.Exit(1)
		}
		if os.Getenv("TI_FAKE_DRIVE9_STAT_NOT_FOUND") == "1" {
			fmt.Fprintln(os.Stderr, "fs stat: remote file not found")
			os.Exit(1)
		}
		if sequencePath := os.Getenv("TI_FAKE_DRIVE9_STAT_FAILURE_SEQUENCE"); sequencePath != "" {
			if _, err := os.Stat(sequencePath); os.IsNotExist(err) {
				_ = os.WriteFile(sequencePath, []byte("attempted"), 0600)
				fmt.Fprintln(os.Stderr, "fs stat: storage backend unavailable; resource is still provisioning")
				os.Exit(1)
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"path": ":/", "size": 12, "isdir": false, "revision": 3})
	default:
		return
	}
}

func record(args []string) {
	path := os.Getenv("TI_FAKE_DRIVE9_RECORD")
	if path == "" {
		return
	}
	env := map[string]string{}
	for _, key := range []string{"DRIVE9_API_KEY", "DRIVE9_SERVER", "DRIVE9_REGION_CODE", "DRIVE9_PUBLIC_KEY", "DRIVE9_PRIVATE_KEY", "DRIVE9_VAULT_TOKEN", "HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(callRecord{Args: args, Env: env})
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
`
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write fake companion source: %v", err)
	}
	binPath = filepath.Join(dir, "ti-drive9")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binPath, sourcePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake companion: %v\n%s", err, output)
	}
	return binPath, recordPath
}

func readFakeDrive9Calls(t *testing.T, recordPath string) []fakeDrive9Call {
	t.Helper()
	file, err := os.Open(recordPath)
	if err != nil {
		t.Fatalf("open fake companion record: %v", err)
	}
	defer file.Close()
	var calls []fakeDrive9Call
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call fakeDrive9Call
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
			t.Fatalf("decode fake companion record %q: %v", scanner.Text(), err)
		}
		calls = append(calls, call)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fake companion record: %v", err)
	}
	return calls
}

func recordedFlagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func requireFakeDrive9Call(t *testing.T, recordPath string, prefix ...string) fakeDrive9Call {
	t.Helper()
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, prefix) {
			return call
		}
	}
	t.Fatalf("missing fake companion call with prefix %#v", prefix)
	return fakeDrive9Call{}
}

func requireFakeDrive9CallWithoutArg(t *testing.T, recordPath, excluded string, prefix ...string) fakeDrive9Call {
	t.Helper()
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if hasArgPrefix(call.Args, prefix) && !containsArg(call.Args, excluded) {
			return call
		}
	}
	t.Fatalf("missing fake companion call with prefix %#v without %q", prefix, excluded)
	return fakeDrive9Call{}
}

func hasArgPrefix(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
