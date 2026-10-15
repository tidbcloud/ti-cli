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
		{ProviderGCP, "us-east1"},
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
		{"unsupported", "centralus"},
		{ProviderAzure, "eastus"},
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

func TestValidateFilesystemAcceptsFilesystemPlacements(t *testing.T) {
	tests := []struct {
		provider string
		region   string
	}{
		{ProviderAWS, "us-east-1"},
		{ProviderAWS, "ap-southeast-1"},
		{ProviderAlibabaCloud, "ap-southeast-1"},
		{ProviderGCP, "us-east1"},
		{ProviderAzure, "centralus"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.region, func(t *testing.T) {
			if err := ValidateFilesystem(tt.provider, tt.region); err != nil {
				t.Fatalf("expected filesystem placement to be valid: %v", err)
			}
		})
	}

	for _, tt := range []struct {
		provider string
		region   string
	}{
		{"unsupported", "centralus"},
		{ProviderAzure, "eastus"},
		{ProviderAWS, "cn-hangzhou"},
	} {
		t.Run("reject/"+tt.provider+"/"+tt.region, func(t *testing.T) {
			if err := ValidateFilesystem(tt.provider, tt.region); err == nil {
				t.Fatal("expected filesystem provider/region to be rejected")
			}
		})
	}
}

func TestValidateRejectsFilesystemOnlyPlacementForStarter(t *testing.T) {
	err := Validate(ProviderAzure, "centralus")
	if err == nil {
		t.Fatal("expected the Filesystem-only placement to be rejected for Starter")
	}
	if got := err.Error(); !strings.Contains(got, "azure-centralus") || !strings.Contains(got, "Filesystem only") {
		t.Fatalf("unexpected Filesystem-only error %q", got)
	}
}

func TestParsePlacementCode(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			placement, err := ParsePlacementCode(tt.code)
			if err != nil {
				t.Fatalf("ParsePlacementCode failed: %v", err)
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

func TestParsePlacementCodeRejectsUnsupportedValues(t *testing.T) {
	for _, code := range []string{"us-east-1", "ali-us-east-1", "gcp-us-east-1", "azure-eastus", "azure-central-us"} {
		t.Run(code, func(t *testing.T) {
			if _, err := ParsePlacementCode(code); err == nil {
				t.Fatal("expected placement code to be rejected")
			}
		})
	}
}
