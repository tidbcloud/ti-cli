package fs

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fsregion"
)

// RegionSummary is one published TiDB Cloud Filesystem region.
type RegionSummary struct {
	RegionCode     string `json:"region_code"`
	CloudProvider  string `json:"cloud_provider"`
	DisplayName    string `json:"display_name"`
	ServiceAddress string `json:"service_address"`
	State          string `json:"state"`
	Eligible       bool   `json:"eligible"`
}

// ListRegionsResult is the published Filesystem region list.
type ListRegionsResult struct {
	RegionCount int             `json:"region_count"`
	Regions     []RegionSummary `json:"regions"`
}

// Human renders the region list as an aligned table.
func (r ListRegionsResult) Human() string {
	var builder strings.Builder
	writer := tabwriter.NewWriter(&builder, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "REGION\tPROVIDER\tDISPLAY NAME\tSTATE\tSERVICE ADDRESS")
	for _, region := range r.Regions {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", region.RegionCode, region.CloudProvider, region.DisplayName, strings.ToLower(region.State), region.ServiceAddress)
	}
	_ = writer.Flush()
	return strings.TrimRight(builder.String(), "\n")
}

// ListRegions reads the published Filesystem region catalog. It always asks the
// API so that the result is authoritative, and it backfills the regional address
// on stored credentials as a side effect. Unlike data-plane routing it never
// falls back to a derived address.
func (s Service) ListRegions(ctx context.Context, profile *config.Profile) (ListRegionsResult, error) {
	if _, err := auth.ValidateProfile(profile); err != nil {
		return ListRegionsResult{}, err
	}
	catalog, err := s.resolver().FetchFSRegions(ctx, profile)
	if err != nil {
		return ListRegionsResult{}, err
	}
	s.refreshCredentialServiceAddresses(ctx, profile, catalog)
	regions := make([]RegionSummary, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		regions = append(regions, RegionSummary{
			RegionCode:     entry.RegionCode,
			CloudProvider:  entry.CloudProvider,
			DisplayName:    entry.DisplayName,
			ServiceAddress: entry.ServiceAddress,
			State:          entry.State,
			Eligible:       entry.Eligible(),
		})
	}
	return ListRegionsResult{RegionCount: len(regions), Regions: regions}, nil
}

// refreshCredentialServiceAddresses records the published service address on
// stored credentials that predate the Filesystem region catalog. This is the
// upgrade path for File Systems created by older ti releases: data-plane
// commands route from the credential, so they need the address recorded there.
// Per-credential failures are ignored: a credential that cannot be rewritten
// still reports the actionable hint when a data-plane command needs it.
func (s Service) refreshCredentialServiceAddresses(ctx context.Context, profile *config.Profile, catalog fsregion.Catalog) {
	homeDir, err := s.homeDir()
	if err != nil {
		return
	}
	credentials, err := fscred.ListCredentials(homeDir, profileName(profile))
	if err != nil {
		return
	}
	for _, credential := range credentials {
		if !credential.HasLocalToken || credential.ServiceAddress != "" || credential.APIKey == "" {
			continue
		}
		if entry, ok := catalog.Lookup(credential.RegionCode); !ok || !entry.Eligible() {
			continue
		} else {
			address := entry.ServiceAddress
			// Re-read and write under the credential lock so a concurrent
			// refresh-file-system-token rotation cannot be reverted by this
			// placement-only update.
			_ = fscred.WithCredentialLock(ctx, homeDir, profileName(profile), credential.FileSystemID, func() error {
				current, getErr := fscred.GetCredential(homeDir, profileName(profile), credential.FileSystemID)
				if getErr != nil || current.ServiceAddress != "" {
					return nil
				}
				current.ServiceAddress = address
				_, storeErr := fscred.StoreCredentialRecord(homeDir, profile, current, true)
				return storeErr
			})
		}
	}
}
