package region

import (
	"strings"
	"testing"
)

func TestValidateSupportedProviderRegions(t *testing.T) {
	tests := []struct {
		provider string
		region   string
	}{
		{ProviderAWS, "us-east-1"},
		{ProviderAWS, "us-west-2"},
		{ProviderAWS, "eu-central-1"},
		{ProviderAWS, "ap-northeast-1"},
		{ProviderAWS, "ap-southeast-1"},
		{ProviderAlibabaCloud, "ap-southeast-1"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.region, func(t *testing.T) {
			if err := Validate(tt.provider, tt.region); err != nil {
				t.Fatalf("expected provider/region to be valid: %v", err)
			}
		})
	}
}

func TestValidateRejectsUnsupportedProviderRegions(t *testing.T) {
	tests := []struct {
		provider string
		region   string
	}{
		{ProviderAzure, "eastus"},
		{"unsupported", "centralus"},
		{ProviderAlibabaCloud, "us-east-1"},
		{ProviderAWS, "cn-hangzhou"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.region, func(t *testing.T) {
			if err := Validate(tt.provider, tt.region); err == nil {
				t.Fatal("expected provider/region to be rejected")
			}
		})
	}
}

func TestValidateRejectsFilesystemOnlyPlacementForStarter(t *testing.T) {
	for _, test := range []struct {
		provider  string
		region    string
		canonical string
	}{
		{provider: ProviderGCP, region: "us-east1", canonical: "gcp-us-east1"},
		{provider: ProviderAzure, region: "centralus", canonical: "azure-centralus"},
	} {
		t.Run(test.canonical, func(t *testing.T) {
			err := Validate(test.provider, test.region)
			if err == nil {
				t.Fatal("expected the Filesystem-only placement to be rejected for Starter")
			}
			if got := err.Error(); !strings.Contains(got, test.canonical) || !strings.Contains(got, "Filesystem only") {
				t.Fatalf("unexpected Filesystem-only error %q", got)
			}
		})
	}
}

func TestParseCanonicalCodeAcceptsStructuralCodes(t *testing.T) {
	tests := []struct {
		code       string
		canonical  string
		provider   string
		nativeCode string
	}{
		{"aws-us-east-1", "aws-us-east-1", ProviderAWS, "us-east-1"},
		{"aws-ap-southeast-1", "aws-ap-southeast-1", ProviderAWS, "ap-southeast-1"},
		{"alicloud-ap-southeast-1", "alicloud-ap-southeast-1", ProviderAlibabaCloud, "ap-southeast-1"},
		{"ali-ap-southeast-1", "alicloud-ap-southeast-1", ProviderAlibabaCloud, "ap-southeast-1"},
		{"gcp-us-east1", "gcp-us-east1", ProviderGCP, "us-east1"},
		{"azure-centralus", "azure-centralus", ProviderAzure, "centralus"},
		// A structurally valid code is not a Starter whitelist decision. The
		// Filesystem region catalog decides whether these are published.
		{"aws-us-east-2", "aws-us-east-2", ProviderAWS, "us-east-2"},
		{"aws-ap-southeast-3", "aws-ap-southeast-3", ProviderAWS, "ap-southeast-3"},
		{"gcp-europe-west1", "gcp-europe-west1", ProviderGCP, "europe-west1"},
		{"AWS-US-EAST-1", "aws-us-east-1", ProviderAWS, "us-east-1"},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			placement, err := ParseCanonicalCode(tt.code)
			if err != nil {
				t.Fatalf("ParseCanonicalCode failed: %v", err)
			}
			if placement.Code != tt.canonical || placement.Provider != tt.provider || placement.NativeCode != tt.nativeCode {
				t.Fatalf("unexpected placement: %#v", placement)
			}
			canonical, err := CanonicalCode(placement.Provider, placement.NativeCode)
			if err != nil || canonical != tt.canonical {
				t.Fatalf("canonical placement = %q, %v; want %q", canonical, err, tt.canonical)
			}
		})
	}
}

func TestParseCanonicalCodeRejectsMalformedValues(t *testing.T) {
	for _, code := range []string{"", "aws", "aws-", "-us-east-1", "us-east-1", "tencent-ap-beijing", "aws-US East 1", "aws-us_east_1", "aws-us-east-1.1-", "aws-" + strings.Repeat("a", 64)} {
		t.Run(code, func(t *testing.T) {
			if _, err := ParseCanonicalCode(code); err == nil {
				t.Fatal("expected placement code to be rejected")
			}
		})
	}
}

func TestParseCanonicalCodeDoesNotDecideStarterEligibility(t *testing.T) {
	placement, err := ParseCanonicalCode("aws-us-east-2")
	if err != nil {
		t.Fatalf("ParseCanonicalCode failed: %v", err)
	}
	if err := Validate(placement.Provider, placement.NativeCode); err == nil {
		t.Fatal("expected an unpublished Starter placement to be rejected by Validate")
	}
}

func TestCanonicalCodeRejectsUnknownProvider(t *testing.T) {
	if _, err := CanonicalCode("tencent", "ap-beijing"); err == nil {
		t.Fatal("expected an unknown provider to be rejected")
	}
	if code, err := CanonicalCode(ProviderAlibabaCloud, "ap-southeast-3"); err != nil || code != "alicloud-ap-southeast-3" {
		t.Fatalf("CanonicalCode = %q, %v; want alicloud-ap-southeast-3", code, err)
	}
}

func TestProviderForAPI(t *testing.T) {
	for _, test := range []struct {
		api      string
		provider string
	}{
		{"aws", ProviderAWS},
		{"alicloud", ProviderAlibabaCloud},
		{"ali", ProviderAlibabaCloud},
		{"gcp", ProviderGCP},
		{"azure", ProviderAzure},
		{" AWS ", ProviderAWS},
	} {
		t.Run(test.api, func(t *testing.T) {
			provider, ok := ProviderForAPI(test.api)
			if !ok || provider != test.provider {
				t.Fatalf("ProviderForAPI(%q) = %q, %t; want %q", test.api, provider, ok, test.provider)
			}
		})
	}
	if _, ok := ProviderForAPI("tencent"); ok {
		t.Fatal("expected an unknown API provider to be rejected")
	}
}
