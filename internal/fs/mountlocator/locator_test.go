package mountlocator

import (
	"os"
	"reflect"
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

func TestTokenCorrelationAndLegacyListCompatibility(t *testing.T) {
	home := t.TempDir()
	legacy, err := New("default", "tenant-old", "aws-us-east-1", "/tmp/legacy-home", t.TempDir(), "fs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(home, legacy); err != nil {
		t.Fatal(err)
	}
	correlated, err := New("default", "tenant-new", "aws-us-east-1", "/tmp/new-home", t.TempDir(), "fs")
	if err != nil {
		t.Fatal(err)
	}
	correlated = correlated.WithTokenCorrelation("tenant-new", "token-id", "0123456789abcdef0123456789abcdef")
	path, err := Write(home, correlated)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "drive9_") || !strings.Contains(string(data), `"token_id": "token-id"`) {
		t.Fatalf("correlated locator = %s", data)
	}
	locators, err := List(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(locators) != 2 {
		t.Fatalf("locators = %#v", locators)
	}
	for _, locator := range locators {
		if locator.FileSystemName == "tenant-old" && (locator.TokenID != "" || locator.TokenFingerprint != "") {
			t.Fatalf("legacy locator gained correlation: %#v", locator)
		}
	}
}

// TestPackPathsAndLocalRootRoundTrip keeps the two fields unmount replays on a
// JSON round trip; nothing else in the locator is involved.
func TestPackPathsAndLocalRootRoundTrip(t *testing.T) {
	home := t.TempDir()
	locator, err := New("default", "tenant-pack", "aws-us-east-1", "/tmp/pack-home", t.TempDir(), "fs")
	if err != nil {
		t.Fatal(err)
	}
	locator.LocalRoot = "/tmp/pack-home/local"
	locator.PackPaths = []string{"/src", "node_modules"}
	path, err := Write(home, locator)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"local_root": "/tmp/pack-home/local"`) || !strings.Contains(string(data), `"pack_paths"`) {
		t.Fatalf("locator json = %s", data)
	}
	read, _, err := Read(home, locator.MountPath)
	if err != nil {
		t.Fatal(err)
	}
	if read.LocalRoot != locator.LocalRoot || !reflect.DeepEqual(read.PackPaths, locator.PackPaths) {
		t.Fatalf("round trip = %#v, want %#v", read, locator)
	}
}
