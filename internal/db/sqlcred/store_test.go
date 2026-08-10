package sqlcred

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSafeClusterIDRejectsUnsafePathSegments(t *testing.T) {
	tests := []string{
		"",
		".",
		"..",
		"../cluster",
		"cluster/one",
		`cluster\one`,
		"cluster..one",
		"cluster:one",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := SafeClusterID(value); err == nil {
				t.Fatalf("expected %q to be rejected", value)
			}
		})
	}

	got, err := SafeClusterID("clusters/10445133422632795167")
	if err != nil {
		t.Fatalf("expected valid cluster id: %v", err)
	}
	if got != "10445133422632795167" {
		t.Fatalf("unexpected normalized cluster id %q", got)
	}
}

func TestReadWriteCredentials(t *testing.T) {
	home := t.TempDir()
	doc := Document{
		ReadOnly:  Credential{Username: "prefix.ti_ro", Password: "ro-pass"},
		ReadWrite: Credential{Username: "prefix.ti_rw", Password: "rw-pass"},
		Admin:     Credential{Username: "prefix.ti_admin", Password: "admin-pass"},
	}
	if err := Write(home, "cluster-1", doc); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	path, err := CredentialsPath(home, "cluster-1")
	if err != nil {
		t.Fatalf("CredentialsPath failed: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join(".ti", "db_users", "cluster-1", "credentials")) {
		t.Fatalf("unexpected path %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !strings.Contains(string(data), "[read_only]") || !strings.Contains(string(data), "prefix.ti_admin") {
		t.Fatalf("unexpected credentials file:\n%s", string(data))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials mode: want 0600, got %o", info.Mode().Perm())
		}
	}

	read, err := Read(home, "cluster-1")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if read.ReadWrite.Password != "rw-pass" {
		t.Fatalf("unexpected read document: %#v", read)
	}
	if credential, ok := read.Credential(ReadOnly); !ok || credential.Username != "prefix.ti_ro" {
		t.Fatalf("unexpected read_only credential: %#v ok=%t", credential, ok)
	}
}

func TestReadMissingCredentialsReturnsEmptyDocument(t *testing.T) {
	doc, err := Read(t.TempDir(), "cluster-1")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if doc.ReadOnly.Username != "" || doc.ReadWrite.Username != "" || doc.Admin.Username != "" {
		t.Fatalf("expected empty document, got %#v", doc)
	}
}
