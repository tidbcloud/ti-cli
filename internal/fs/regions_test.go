package fs

import (
	"context"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fsregion"
)

func regionCatalogResolver(t *testing.T, homeDir string, entries ...fsregion.Entry) Service {
	t.Helper()
	catalog, err := fsregion.Normalize(entries)
	if err != nil {
		t.Fatal(err)
	}
	return Service{HomeDir: homeDir, Resolver: endpoints.Resolver{FSRegions: &catalog}}
}

func TestListRegionsReportsThePublishedCatalog(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	profile.HomeDir = home
	service := regionCatalogResolver(t, home,
		fsregion.Entry{RegionCode: "aws-us-west-2", CloudProvider: "aws", DisplayName: "Oregon (us-west-2)", ServiceAddress: "https://aws-us-west-2.drive9.ai", State: "ACTIVE"},
		fsregion.Entry{RegionCode: "aws-us-east-1", CloudProvider: "aws", DisplayName: "N. Virginia (us-east-1)", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"},
		fsregion.Entry{RegionCode: "azure-centralus", CloudProvider: "azure", DisplayName: "Central US (centralus)", ServiceAddress: "https://azure-centralus.drive9.ai", State: "PENDING"},
	)

	result, err := service.ListRegions(context.Background(), profile)
	if err != nil {
		t.Fatalf("ListRegions failed: %v", err)
	}
	if result.RegionCount != 3 || len(result.Regions) != 3 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Regions[0].RegionCode != "aws-us-east-1" || result.Regions[2].RegionCode != "azure-centralus" {
		t.Fatalf("regions are not sorted by code: %#v", result.Regions)
	}
	azure := result.Regions[2]
	if azure.Eligible || !strings.EqualFold(azure.State, "pending") {
		t.Fatalf("unexpected eligibility for a pending region: %#v", azure)
	}
	if !result.Regions[0].Eligible {
		t.Fatalf("expected an ACTIVE region to be eligible: %#v", result.Regions[0])
	}

	human := result.Human()
	for _, want := range []string{"REGION", "PROVIDER", "SERVICE ADDRESS", "aws-us-east-1", "aws", "pending", "https://azure-centralus.drive9.ai"} {
		if !strings.Contains(human, want) {
			t.Fatalf("text output missing %q:\n%s", want, human)
		}
	}
}

func TestListRegionsRequiresTiDBCloudCredentials(t *testing.T) {
	profile := testProfile()
	profile.HomeDir = t.TempDir()
	profile.TiDBCloudPublicKey = ""
	profile.TiDBCloudPrivateKey = ""
	service := regionCatalogResolver(t, "", fsregion.Entry{RegionCode: "aws-us-east-1", CloudProvider: "aws", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"})

	_, err := service.ListRegions(context.Background(), profile)
	if err == nil || apperr.CodeFor(err) != "auth.missing_credentials" {
		t.Fatalf("expected a missing credentials error, got %v", err)
	}
}

func TestListRegionsRefreshesStoredCredentialAddresses(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	profile.HomeDir = home
	if _, err := fscred.StoreCredential(home, profile, "fs-1", "aws-us-east-1", "fs-secret", false); err != nil {
		t.Fatal(err)
	}
	stored, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil || stored.ServiceAddress != "" {
		t.Fatalf("precondition: credential=%#v err=%v", stored, err)
	}

	service := regionCatalogResolver(t, home,
		fsregion.Entry{RegionCode: "aws-us-east-1", CloudProvider: "aws", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"},
		fsregion.Entry{RegionCode: "aws-us-west-2", CloudProvider: "aws", ServiceAddress: "https://aws-us-west-2.drive9.ai", State: "ACTIVE"},
	)
	if _, err := service.ListRegions(context.Background(), profile); err != nil {
		t.Fatalf("ListRegions failed: %v", err)
	}

	refreshed, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ServiceAddress != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("credential address = %q, want the published address", refreshed.ServiceAddress)
	}
	if refreshed.APIKey != "fs-secret" || refreshed.RegionCode != "aws-us-east-1" {
		t.Fatalf("refresh changed credential identity: %#v", refreshed)
	}
}

func TestListRegionsLeavesUnknownRegionsAlone(t *testing.T) {
	home := t.TempDir()
	profile := testProfile()
	profile.HomeDir = home
	if _, err := fscred.StoreCredential(home, profile, "fs-1", "aws-us-west-2", "fs-secret", false); err != nil {
		t.Fatal(err)
	}
	service := regionCatalogResolver(t, "", fsregion.Entry{RegionCode: "aws-us-east-1", CloudProvider: "aws", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"})
	if _, err := service.ListRegions(context.Background(), profile); err != nil {
		t.Fatalf("ListRegions failed: %v", err)
	}
	stored, err := fscred.GetCredential(home, profile.Name, "fs-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ServiceAddress != "" {
		t.Fatalf("credential for an unpublished region was rewritten: %#v", stored)
	}
}

func TestDryRunCreateFileSystemAcceptsFilesystemOnlyPlacement(t *testing.T) {
	profile := testProfile()
	profile.PlacementRegionCode = "azure-centralus"
	profile.CloudProvider = "azure"
	profile.RegionCode = "centralus"
	service := regionCatalogResolver(t, "",
		fsregion.Entry{RegionCode: "aws-us-east-1", CloudProvider: "aws", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"},
		fsregion.Entry{RegionCode: "azure-centralus", CloudProvider: "azure", ServiceAddress: "https://azure-centralus.drive9.ai", State: "ACTIVE"},
	)
	result, err := service.DryRunCreateFileSystem(context.Background(), "ti fs create-file-system", CreateFileSystemOptions{Profile: profile})
	if err != nil {
		t.Fatalf("DryRunCreateFileSystem failed: %v", err)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "endpoint_selection" {
			found = true
			if check.Status != "passed" || !strings.Contains(check.Message, "azure") || !strings.Contains(check.Message, "centralus") {
				t.Fatalf("unexpected endpoint check: %#v", check)
			}
		}
	}
	if !found {
		t.Fatalf("dry-run did not report endpoint selection: %#v", result.Checks)
	}
}
