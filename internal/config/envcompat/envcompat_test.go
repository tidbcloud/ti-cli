package envcompat

import (
	"reflect"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestResolveCompatibility(t *testing.T) {
	pair := Pair{Canonical: "TI_REGION_CODE", Legacy: "TDC_REGION_CODE"}
	tests := []struct {
		name       string
		env        map[string]string
		want       string
		present    bool
		usedLegacy bool
		wantCode   string
	}{
		{name: "missing", env: map[string]string{}},
		{name: "canonical", env: map[string]string{"TI_REGION_CODE": "aws-us-east-1"}, want: "aws-us-east-1", present: true},
		{name: "legacy", env: map[string]string{"TDC_REGION_CODE": "aws-us-west-2"}, want: "aws-us-west-2", present: true, usedLegacy: true},
		{name: "equal", env: map[string]string{"TI_REGION_CODE": "aws-us-east-1", "TDC_REGION_CODE": "aws-us-east-1"}, want: "aws-us-east-1", present: true},
		{name: "conflict", env: map[string]string{"TI_REGION_CODE": "aws-us-east-1", "TDC_REGION_CODE": "aws-us-west-2"}, wantCode: "config.environment_conflict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, present, usedLegacy, err := Resolve(tt.env, pair)
			if apperr.CodeFor(err) != tt.wantCode {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want || present != tt.present || usedLegacy != tt.usedLegacy {
				t.Fatalf("Resolve() = %q, %v, %v; want %q, %v, %v", got, present, usedLegacy, tt.want, tt.present, tt.usedLegacy)
			}
		})
	}
}

func TestLegacyNamesDoesNotExposeValues(t *testing.T) {
	got := LegacyNames(map[string]string{
		"TDC_PRIVATE_KEY": "secret",
		"TDC_PROFILE":     "stage",
		"UNRELATED":       "value",
	})
	want := []string{"TDC_PRIVATE_KEY", "TDC_PROFILE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LegacyNames() = %#v, want %#v", got, want)
	}
}
