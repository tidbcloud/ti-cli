package configure

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/store"
)

func TestRunWritesProfileLocallyAndDoesNotPrintSecret(t *testing.T) {
	home := t.TempDir()
	input := strings.NewReader("aws-us-east-1\npublic-key\nprivate-key\n")
	var output bytes.Buffer

	result, err := Run(context.Background(), Options{
		Profile: "stage",
		HomeDir: home,
		In:      input,
		Out:     &output,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(output.String(), "private-key") {
		t.Fatalf("configure output leaked private key:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Default region code") {
		t.Fatalf("configure output missing default region prompt:\n%s", output.String())
	}
	if result.Profile != "stage" || result.RegionCode != "aws-us-east-1" || !result.CredentialsStored {
		t.Fatalf("unexpected configure result: %#v", result)
	}
	if human := result.Human(); strings.Contains(strings.ToLower(human), "project") || !strings.Contains(human, "Credentials stored: true") {
		t.Fatalf("unexpected configure text result: %q", human)
	}

	profile, err := config.Load(context.Background(), config.LoadOptions{Profile: "stage", ProfileExplicit: true, HomeDir: home})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if profile.CloudProvider != "aws" || profile.RegionCode != "us-east-1" || profile.TiDBCloudPrivateKey != "private-key" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestRunRejectsUnsupportedProviderRegion(t *testing.T) {
	_, err := Run(context.Background(), Options{
		HomeDir: t.TempDir(),
		In:      strings.NewReader("ali-us-east-1\npublic-key\nprivate-key\n"),
		Out:     &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected invalid provider/region to fail")
	}
}

func TestRunNonInteractiveUsesEnvironmentWithoutNetwork(t *testing.T) {
	home := t.TempDir()
	result, err := Run(context.Background(), Options{
		Profile:        "ci",
		HomeDir:        home,
		NonInteractive: true,
		Env: map[string]string{
			"TI_REGION_CODE":         "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY":  "env-public",
			"TIDB_CLOUD_PRIVATE_KEY": "env-private",
		},
		Out: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Profile != "ci" || result.RegionCode != "aws-us-east-1" || !result.CredentialsStored {
		t.Fatalf("unexpected configure result: %#v", result)
	}
	profile, err := config.Load(context.Background(), config.LoadOptions{Profile: "ci", ProfileExplicit: true, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if profile.TiDBCloudPublicKey != "env-public" || profile.TiDBCloudPrivateKey != "env-private" {
		t.Fatalf("environment credentials not stored: %#v", profile)
	}
}

func TestRunNonInteractiveRequiresMissingValues(t *testing.T) {
	_, err := Run(context.Background(), Options{
		HomeDir:        t.TempDir(),
		NonInteractive: true,
		Env: map[string]string{
			"TI_REGION_CODE":        "aws-us-east-1",
			"TIDB_CLOUD_PUBLIC_KEY": "env-public",
		},
		Out: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected missing private key to fail")
	}
	if got := apperr.MessageFor(err); !strings.Contains(got, "non-interactive configure") {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestRunReconfigureRemovesOnlySelectedLegacyProjectID(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(store.ConfigPath(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	configData := "[default]\nregion_code = 'aws-us-east-1'\nproject_id = 'default-project'\n\n[stage]\nregion_code = 'aws-us-west-2'\nproject_id = 'stage-project'\n"
	if err := os.WriteFile(store.ConfigPath(home), []byte(configData), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.CredentialsPath(home), []byte("[default]\ntidb_cloud_public_key = 'default-public'\ntidb_cloud_private_key = 'default-private'\n\n[stage]\ntidb_cloud_public_key = 'old-public'\ntidb_cloud_private_key = 'old-private'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Run(context.Background(), Options{
		Profile:             "stage",
		HomeDir:             home,
		NonInteractive:      true,
		RegionCode:          "aws-us-east-1",
		TiDBCloudPublicKey:  "new-public",
		TiDBCloudPrivateKey: "new-private",
	})
	if err != nil {
		t.Fatalf("reconfigure failed: %v", err)
	}
	doc, err := store.ReadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if doc["stage"].LegacyProjectID != "" {
		t.Fatalf("selected profile retained project id: %#v", doc["stage"])
	}
	if doc["default"].LegacyProjectID != "default-project" {
		t.Fatalf("unselected profile changed: %#v", doc["default"])
	}
	raw, err := os.ReadFile(store.ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stage-project") || !strings.Contains(string(raw), "default-project") {
		t.Fatalf("unexpected config migration:\n%s", raw)
	}
}
