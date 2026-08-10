package mountlocator

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestWriteReadAndRemoveLocator(t *testing.T) {
	home := t.TempDir()
	locator, err := New("default", "workspace", "aws-us-east-1", "/tmp/drive9-home", "./workspace", "fs")
	if err != nil {
		t.Fatal(err)
	}
	path, err := Write(home, locator)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"drive9_", "api_key", "fs_token"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("locator contains secret material %q: %s", secret, data)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("locator mode = %o, want 600", info.Mode().Perm())
		}
	}
	got, _, err := Read(home, "./workspace")
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "default" || got.FileSystemName != "workspace" || got.RegionCode != "aws-us-east-1" || got.Kind != "fs" {
		t.Fatalf("unexpected locator: %#v", got)
	}
	if err := Remove(home, "./workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("locator still exists: %v", err)
	}
}

func TestReadRejectsIncompleteLocator(t *testing.T) {
	home := t.TempDir()
	locator, err := New("default", "workspace", "aws-us-east-1", "/tmp/drive9-home", "./workspace", "fs")
	if err != nil {
		t.Fatal(err)
	}
	path, err := Write(home, locator)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":"ti.fs.mount-locator/v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(home, "./workspace"); err == nil {
		t.Fatal("expected incomplete locator to fail")
	}
}
