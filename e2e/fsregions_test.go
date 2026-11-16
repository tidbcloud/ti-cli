package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

// fakeFSRegion describes one published Filesystem region for the hidden
// TI_TEST_FS_REGIONS_URL catalog override.
type fakeFSRegion struct {
	RegionCode     string
	CloudProvider  string
	ServiceAddress string
	State          string
}

// newFakeFSRegions serves a Filesystem region catalog response.
func newFakeFSRegions(t *testing.T, regions ...fakeFSRegion) *httptest.Server {
	t.Helper()
	entries := make([]map[string]string, 0, len(regions))
	for _, region := range regions {
		state := region.State
		if state == "" {
			state = "ACTIVE"
		}
		entries = append(entries, map[string]string{
			"name":           "regions/" + region.RegionCode,
			"regionId":       region.RegionCode,
			"cloudProvider":  region.CloudProvider,
			"displayName":    region.RegionCode,
			"serviceAddress": region.ServiceAddress,
			"state":          state,
		})
	}
	payload := map[string]any{"regions": entries, "totalSize": len(entries), "nextPageToken": ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// seedFSCredential records a File System credential exactly as
// `ti fs create-file-system` and `ti fs import-file-system-token` do, so that a
// test can exercise data-plane commands without TiDB Cloud API credentials.
func seedFSCredential(t *testing.T, home, profileName, fileSystemID, regionCode, serviceAddress, token string) {
	t.Helper()
	profile := &config.Profile{Name: profileName, HomeDir: home}
	if _, err := fscred.StoreCredentialRecord(home, profile, fscred.Credential{
		FileSystemID:   fileSystemID,
		RegionCode:     regionCode,
		ServiceAddress: serviceAddress,
		APIKey:         token,
	}, false); err != nil {
		t.Fatalf("seed File System credential: %v", err)
	}
}
