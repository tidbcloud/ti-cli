package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/version"
)

func TestDownloadRefusesPlainHTTP(t *testing.T) {
	const insecureURL = "http://releases.example.com/ti.tar.gz?token=secret-token"
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureURL, http.StatusFound)
	}))
	defer redirecting.Close()

	c := newClient(version.Info{}, "https://releases.invalid", nil)
	for _, rawURL := range []string{insecureURL, redirecting.URL + "/ti.tar.gz"} {
		_, err := c.download(context.Background(), rawURL)
		var appErr *apperr.Error
		if !errors.As(err, &appErr) || appErr.Code != "update.insecure_url" {
			t.Fatalf("download(%s) error = %v, want update.insecure_url", rawURL, err)
		}
		if !errors.Is(err, apitransport.ErrInsecureURL) {
			t.Fatalf("download(%s) error = %v, want ErrInsecureURL", rawURL, err)
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("download error leaks the query: %v", err)
		}
	}
}

func TestDownloadDrive9CompanionRefusesPlainHTTPBaseURL(t *testing.T) {
	c := newClient(version.Info{}, "https://releases.invalid", nil)
	_, cleanup, err := c.downloadDrive9Companion(context.Background(), "http://drive9.example.com/releases/latest")
	cleanup()
	if !errors.Is(err, apitransport.ErrInsecureURL) {
		t.Fatalf("downloadDrive9Companion error = %v, want ErrInsecureURL", err)
	}
}
