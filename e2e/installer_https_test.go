package e2e

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// installShellDownload returns install.sh's error() and download() functions
// followed by a call `download "$1" "$2"`, so the real download code runs alone.
func installShellDownload(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	var errorLine string
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "error() {") {
			errorLine = line
			break
		}
	}
	start := strings.Index(script, "\ndownload() {\n")
	if errorLine == "" || start < 0 {
		t.Fatal("install.sh no longer defines error() and download()")
	}
	end := strings.Index(script[start+1:], "\n}\n")
	if end < 0 {
		t.Fatal("install.sh download() is not terminated")
	}
	body := script[start+1 : start+1+end+3]
	return "set -eu\nRED=''\nRESET=''\n" + errorLine + "\n" + body + "download \"$1\" \"$2\"\n"
}

func TestUnixInstallerDownloadsStayOnHTTPS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell installer is not supported on Windows")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not found")
	}
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write([]byte("tampered"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, plain.URL+"/ti.tar.gz", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("genuine"))
	}))
	defer secure.Close()

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	script := installShellDownload(t)
	download := func(rawURL, out string) ([]byte, error) {
		cmd := exec.Command("sh", "-c", script, "sh", rawURL, out)
		cmd.Env = []string{
			"PATH=" + filepath.Dir(curl) + ":/usr/bin:/bin",
			"CURL_CA_BUNDLE=" + caFile,
			"SSL_CERT_FILE=" + caFile,
		}
		return cmd.CombinedOutput()
	}

	refused := func(name, rawURL string) {
		t.Helper()
		out := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if output, err := download(rawURL, out); err == nil {
			t.Fatalf("%s: download succeeded:\n%s", name, output)
		}
		if data, _ := os.ReadFile(out); string(data) == "tampered" {
			t.Fatalf("%s: download wrote the cleartext bytes", name)
		}
		if hits := plainHits.Load(); hits != 0 {
			t.Fatalf("%s: the plain http server was reached %d times", name, hits)
		}
	}
	// Needs no TLS, so it runs even where curl can't be pointed at the test CA.
	refused("plain http", plain.URL+"/ti.tar.gz")

	okFile := filepath.Join(dir, "ok")
	if output, err := download(secure.URL+"/ti.tar.gz", okFile); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("curl does not trust the test certificate: %v\n%s", err, output)
		}
		t.Skipf("curl does not trust the test certificate here: %v\n%s", err, output)
	}
	if data, _ := os.ReadFile(okFile); string(data) != "genuine" {
		t.Fatalf("https download wrote %q", data)
	}
	refused("https redirect to http", secure.URL+"/redirect")
}

func TestUnixInstallerRequiresCurl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell installer is not supported on Windows")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}
	// A PATH with only sh: no curl, and no wget fallback either.
	bin := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(bin, "sh"), "-c", installShellDownload(t), "sh", "https://example.invalid/ti.tar.gz", filepath.Join(t.TempDir(), "out"))
	cmd.Env = []string{"PATH=" + bin}
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "curl is required") {
		t.Fatalf("download without curl = %v\n%s", err, output)
	}
}

func TestInstallersRefuseDownloadsThatLeaveHTTPS(t *testing.T) {
	shell, err := os.ReadFile(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(shell), "curl -f") != strings.Count(string(shell), "--proto '=https' --proto-redir '=https'") {
		t.Fatal("every curl download in install.sh must use --proto '=https' --proto-redir '=https'")
	}
	if strings.Contains(string(shell), "wget -") {
		t.Fatal("install.sh must not download with wget: it can't refuse an https-to-http redirect")
	}

	powerShell, err := os.ReadFile(filepath.Join("..", "scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	ps := string(powerShell)
	if got := strings.Count(ps, "Invoke-WebRequest"); got != 1 {
		t.Fatalf("install.ps1 calls Invoke-WebRequest %d times; every download must go through Save-HttpsDownload", got)
	}
	if !strings.Contains(ps, `$FinalUri.Scheme -ne "https"`) {
		t.Fatal("install.ps1 must check that the final download URL is https")
	}
	for _, call := range []string{
		"Save-HttpsDownload $ArchiveUrl $ArchivePath",
		"Save-HttpsDownload $ChecksumsUrl $ChecksumsPath",
		"Save-HttpsDownload $CompanionUrl $CompanionPath",
		"Save-HttpsDownload $CompanionChecksumsUrl $CompanionChecksumsPath",
	} {
		if !strings.Contains(ps, call) {
			t.Fatalf("install.ps1 is missing %q", call)
		}
	}
}
