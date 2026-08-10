package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestValidateProfileMissingCredentials(t *testing.T) {
	_, err := ValidateProfile(&config.Profile{Name: "stage"})
	if err == nil {
		t.Fatal("expected missing credentials to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected auth exit code 3, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "authentication required") || !strings.Contains(got, "tidb_cloud_public_key and tidb_cloud_private_key") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestValidateProfileRejectsMalformedCredentials(t *testing.T) {
	_, err := ValidateProfile(&config.Profile{
		Name:                "stage",
		TiDBCloudPublicKey:  "public:key",
		TiDBCloudPrivateKey: "private",
	})
	if err == nil {
		t.Fatal("expected malformed credentials to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected auth exit code 3, got %d", got)
	}
}

func TestLoadProfileMapsMissingFileCredentialsToAuthError(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, "stage", store.ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, store.CredentialsProfile{}); err != nil {
		t.Fatal(err)
	}

	_, err := LoadProfile(context.Background(), config.LoadOptions{
		Profile:         "stage",
		ProfileExplicit: true,
		HomeDir:         home,
	})
	if err == nil {
		t.Fatal("expected missing credentials to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected auth exit code 3, got %d", got)
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "authentication required") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestLoadProfileMissingProfileRemainsConfigError(t *testing.T) {
	_, err := LoadProfile(context.Background(), config.LoadOptions{
		Profile:         "missing",
		ProfileExplicit: true,
		HomeDir:         t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected missing profile to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("missing profile should remain a config error, got exit %d", got)
	}
}

func TestLoadProfileMissingPartialEnvironmentCredentialsDoesNotReportEnvProfile(t *testing.T) {
	home := t.TempDir()
	if err := store.WriteProfile(home, config.DefaultProfile, store.ConfigProfile{
		RegionCode: "aws-us-east-1",
	}, store.CredentialsProfile{}); err != nil {
		t.Fatal(err)
	}

	_, err := LoadProfile(context.Background(), config.LoadOptions{
		HomeDir: home,
		Env: map[string]string{
			"TIDB_CLOUD_PUBLIC_KEY": "env-public",
		},
	})
	if err == nil {
		t.Fatal("expected missing environment private key to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 3 {
		t.Fatalf("expected auth exit code 3, got %d", got)
	}
	message := apperr.MessageFor(err)
	if strings.Contains(message, `profile "env"`) || !strings.Contains(message, "environment credentials") {
		t.Fatalf("unexpected message %q", message)
	}
}
