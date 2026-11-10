package credentialenv

import "testing"

func TestIsCredentialMatchesCredentialFamilies(t *testing.T) {
	for _, key := range []string{
		"TIDB_CLOUD_PUBLIC_KEY",
		"TIDB_CLOUD_PRIVATE_KEY",
		"TDC_PUBLIC_KEY",
		"TDC_PRIVATE_KEY",
		"DRIVE9_API_KEY",
		"DRIVE9_PRIVATE_KEY",
		"DRIVE9_VAULT_TOKEN",
		"TI_FS_TOKEN",
		"TI_FS_API_KEY",
		"TI_FS_AI_PROVIDER_API_KEY",
		"TI_VAULT_TOKEN",
		// Windows resolves environment names case-insensitively while Go
		// preserves their spelling in os.Environ().
		"ti_fs_token",
		"Ti_FS_AI_Provider_Api_Key",
		"tIdb_cloud_private_key",
		"Tidb_Cloud_Public_Key",
		"drive9_private_key",
		"tdc_vault_token",
		"ti_vault_token",
	} {
		if !IsCredential(key) {
			t.Fatalf("%s should be treated as a credential", key)
		}
	}
	for _, key := range []string{
		"PATH", "HOME", "TI_PROFILE", "TI_REGION_CODE", "TI_LOGGING",
		"TI_ALLOW_TEST_ENDPOINTS", "TI_TEST_RUNTIME_RECORD", "FAKE_DRIVE9_RECORD",
		"ti_profile", "ti_region_code", "ti_test_runtime_record",
	} {
		if IsCredential(key) {
			t.Fatalf("%s should be inherited", key)
		}
	}
}

func TestScrubRemovesOnlyCredentials(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"TI_TEST_RUNTIME_RECORD=/tmp/record",
		"TIDB_CLOUD_PRIVATE_KEY=secret",
		"TI_FS_AI_PROVIDER_API_KEY=provider-secret",
		"ti_fs_token=fs-bearer",
		"DRIVE9_API_KEY=fs-token",
		"NOT_A_PAIR",
	}
	got := Scrub(env)
	want := []string{"PATH=/usr/bin", "TI_TEST_RUNTIME_RECORD=/tmp/record", "NOT_A_PAIR"}
	if len(got) != len(want) {
		t.Fatalf("scrubbed env = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scrubbed env = %#v, want %#v", got, want)
		}
	}
}
