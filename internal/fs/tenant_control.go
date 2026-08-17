package fs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

const (
	adminTenantPageSize         = 100
	maxTenantLabels             = 30
	maxLabelNameLength          = 63
	maxLabelPrefixLength        = 253
	maxLabelValueLength         = 63
	invalidDisplayNameMessage   = "--display-name must be 4-64 characters using ASCII letters, numbers, or hyphens, and must start and end with a letter or number"
	invalidDisplayFilterMessage = "--display-name filter cannot be empty or contain %, _, or control characters"
	invalidLabelKeyMessage      = "label keys must be Kubernetes qualified names: an optional lowercase DNS prefix of at most 253 bytes and '/', followed by 1-63 bytes using ASCII letters, numbers, '-', '_' or '.', starting and ending with a letter or number"
	invalidLabelValueMessage    = "label values must be empty or at most 63 bytes using ASCII letters, numbers, '-', '_' or '.', and must start and end with a letter or number"
)

var (
	tenantDisplayNameRegexp = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9]{2,62}[A-Za-z0-9]$`)
	labelNameRegexp         = regexp.MustCompile(`^[A-Za-z0-9](?:[-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelDNSPrefixRegexp    = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$`)
)

func ParseTenantMetadata(displayName string, displayNameSet bool, rawLabels []string) (*string, map[string]string, error) {
	var parsedDisplayName *string
	if displayNameSet {
		if err := validateDisplayName(displayName); err != nil {
			return nil, nil, err
		}
		parsedDisplayName = &displayName
	}
	labels, err := parseLabels(rawLabels, maxTenantLabels)
	if err != nil {
		return nil, nil, err
	}
	return parsedDisplayName, labels, nil
}

func ParseTenantListFilters(displayName string, displayNameSet bool, rawLabels []string) (*string, *LabelFilter, error) {
	var parsedDisplayName *string
	if displayNameSet {
		if displayName == "" || strings.ContainsAny(displayName, "%_") || strings.IndexFunc(displayName, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, nil, apperr.New("fs.invalid_display_name", "usage", 2, invalidDisplayFilterMessage)
		}
		parsedDisplayName = &displayName
	}
	if len(rawLabels) > 1 {
		return nil, nil, apperr.New("fs.invalid_label", "usage", 2, "--label can be provided at most once when listing file systems")
	}
	if len(rawLabels) == 0 {
		return parsedDisplayName, nil, nil
	}
	key, value, err := parseLabel(rawLabels[0])
	if err != nil {
		return nil, nil, err
	}
	return parsedDisplayName, &LabelFilter{Key: key, Value: value}, nil
}

func (s Service) createFileSystem(ctx context.Context, opts CreateFileSystemOptions) (FileSystemResult, error) {
	request, client, creds, endpoint, err := s.adminCreateInputs(opts)
	if err != nil {
		return FileSystemResult{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return FileSystemResult{}, err
	}
	if err := fscred.MigrateNameRegistry(homeDir, opts.Profile); err != nil {
		return FileSystemResult{}, apperr.Wrap("fs.credential_store_preflight", "config", 1, "prepare local FS credential storage", err)
	}
	if err := fscred.PrepareCredentialStore(homeDir, profileName(opts.Profile)); err != nil {
		return FileSystemResult{}, apperr.Wrap("fs.credential_store_preflight", "config", 1, "prepare local FS credential storage", err)
	}
	response, err := client.CreateAdminTenant(ctx, creds, request)
	if err != nil {
		return FileSystemResult{}, mapAdminTenantError(err, "create", "", opts.Profile.PlacementRegionCode)
	}
	fileSystemID, err := fscred.ValidateFileSystemID(response.TenantID)
	if err != nil {
		return FileSystemResult{}, apiContractError("create response did not include a valid tenant_id", err)
	}
	if strings.TrimSpace(response.APIKey) == "" {
		return FileSystemResult{}, apiContractError("create response did not include the one-time owner token", nil)
	}
	status := strings.TrimSpace(response.Status)
	if status == "" {
		status = "provisioning"
	}
	result := FileSystemResult{
		FileSystemID: fileSystemID, DisplayName: effectiveTenantDisplayName(fileSystemID, response.DisplayName), Labels: cloneLabels(response.Labels),
		RegionCode: opts.Profile.PlacementRegionCode, FSToken: response.APIKey, Status: status,
	}
	if _, storeErr := fscred.StoreCredential(homeDir, opts.Profile, fileSystemID, endpoint.RegionName, response.APIKey, false); storeErr != nil {
		if s.Stderr != nil {
			_, _ = fmt.Fprintf(s.Stderr, "ti [WARNING]: file system %s was created, but its one-time token could not be stored locally: %s\n", fileSystemID, apperr.MessageFor(storeErr))
		}
		return result, nil
	}
	result.CredentialsStored = true
	if opts.WaitUntilReady {
		if err := s.waitUntilFileSystemReady(ctx, homeDir, opts.Profile, fileSystemID); err != nil {
			return FileSystemResult{}, err
		}
		result.Status = "ready"
	}
	return result, nil
}

func (s Service) listFileSystems(ctx context.Context, opts ListFileSystemsOptions) (ListFileSystemsResult, error) {
	if err := validateAdminTenantListOptions(opts); err != nil {
		return ListFileSystemsResult{}, err
	}
	client, creds, _, err := s.adminTenantClient(opts.Profile, authz.FSVolumeRead, "list file systems")
	if err != nil {
		return ListFileSystemsResult{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return ListFileSystemsResult{}, err
	}
	if err := fscred.MigrateNameRegistry(homeDir, opts.Profile); err != nil {
		return ListFileSystemsResult{}, err
	}
	credentials, err := fscred.ListCredentials(homeDir, profileName(opts.Profile))
	if err != nil {
		return ListFileSystemsResult{}, err
	}
	hasToken := make(map[string]bool, len(credentials))
	for _, credential := range credentials {
		hasToken[credential.FileSystemID] = credential.HasLocalToken
	}

	page := 1
	seenPages := map[int]bool{}
	seenIDs := map[string]bool{}
	fileSystems := make([]FileSystemSummary, 0)
	for {
		if page <= 0 || seenPages[page] {
			return ListFileSystemsResult{}, apiContractError("file system inventory returned a repeated or invalid page", nil)
		}
		seenPages[page] = true
		apiOpts := apifs.ListAdminTenantsOptions{Page: page, PageSize: adminTenantPageSize}
		if opts.DisplayName != nil {
			apiOpts.DisplayName = *opts.DisplayName
		}
		if opts.Label != nil {
			apiOpts.Label = &apifs.AdminTenantLabelFilter{Key: opts.Label.Key, Value: opts.Label.Value}
		}
		response, listErr := client.ListAdminTenants(ctx, creds, apiOpts)
		if listErr != nil {
			return ListFileSystemsResult{}, mapAdminTenantError(listErr, "list", "", opts.Profile.PlacementRegionCode)
		}
		if response.Page != page {
			return ListFileSystemsResult{}, apiContractError(fmt.Sprintf("file system inventory returned page %d while page %d was requested", response.Page, page), nil)
		}
		for _, tenant := range response.Tenants {
			id, idErr := fscred.ValidateFileSystemID(tenant.TenantID)
			if idErr != nil {
				return ListFileSystemsResult{}, apiContractError("file system inventory included an invalid tenant_id", idErr)
			}
			if seenIDs[id] {
				return ListFileSystemsResult{}, apiContractError(fmt.Sprintf("file system inventory returned duplicate file system ID %q", id), nil)
			}
			seenIDs[id] = true
			tenant.TenantID = id
			fileSystems = append(fileSystems, mapAdminTenant(tenant, opts.Profile.PlacementRegionCode, hasToken[id]))
		}
		if response.NextPage == 0 {
			break
		}
		if response.NextPage <= page || seenPages[response.NextPage] {
			return ListFileSystemsResult{}, apiContractError("file system inventory returned a repeated or regressing next_page", nil)
		}
		page = response.NextPage
	}
	sort.Slice(fileSystems, func(i, j int) bool { return fileSystems[i].FileSystemID < fileSystems[j].FileSystemID })
	return ListFileSystemsResult{RegionCode: opts.Profile.PlacementRegionCode, FileSystems: fileSystems}, nil
}

func (s Service) describeFileSystem(ctx context.Context, profile *config.Profile, fileSystemID string) (DescribeFileSystemResult, error) {
	id, client, creds, err := s.adminItemInputs(profile, fileSystemID, authz.FSVolumeRead, "describe a file system")
	if err != nil {
		return DescribeFileSystemResult{}, err
	}
	response, err := client.GetAdminTenant(ctx, creds, id)
	if err != nil {
		return DescribeFileSystemResult{}, mapAdminTenantError(err, "get", id, profile.PlacementRegionCode)
	}
	if response.TenantID != id {
		return DescribeFileSystemResult{}, apiContractError(fmt.Sprintf("describe response identified file system %q instead of %q", response.TenantID, id), nil)
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return DescribeFileSystemResult{}, err
	}
	if err := fscred.MigrateNameRegistry(homeDir, profile); err != nil {
		return DescribeFileSystemResult{}, err
	}
	_, credentialErr := fscred.GetCredential(homeDir, profileName(profile), id)
	if credentialErr != nil && apperr.CodeFor(credentialErr) != "fs.credential_not_found" {
		return DescribeFileSystemResult{}, credentialErr
	}
	return DescribeFileSystemResult{FileSystemSummary: mapAdminTenant(response, profile.PlacementRegionCode, credentialErr == nil)}, nil
}

func (s Service) deleteFileSystem(ctx context.Context, opts DeleteFileSystemOptions) (DeleteResult, error) {
	id, client, creds, _, err := s.adminDeleteInputs(opts)
	if err != nil {
		return DeleteResult{}, err
	}
	response, err := client.DeleteAdminTenant(ctx, creds, id)
	if err != nil {
		return DeleteResult{}, mapAdminTenantError(err, "delete", id, opts.Profile.PlacementRegionCode)
	}
	if response.TenantID != "" && response.TenantID != id {
		return DeleteResult{}, apiContractError(fmt.Sprintf("delete response identified file system %q instead of %q", response.TenantID, id), nil)
	}
	status := strings.TrimSpace(response.Status)
	if status == "" {
		status = "deleting"
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return DeleteResult{}, err
	}
	credentialsRemoved, err := fscred.DeleteCredential(homeDir, profileName(opts.Profile), id)
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{FileSystemID: id, Status: status, CredentialsRemoved: credentialsRemoved, RemoteDeletionState: status}, nil
}

func (s Service) adminCreateInputs(opts CreateFileSystemOptions) (apifs.AdminTenantCreateRequest, *apifs.Client, apifs.TiDBCloudCredentials, endpoints.Endpoint, error) {
	if err := validateCreateMetadata(opts.DisplayName, opts.Labels); err != nil {
		return apifs.AdminTenantCreateRequest{}, nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	client, creds, endpoint, err := s.adminTenantClient(opts.Profile, authz.FSVolumeCreate, "create a file system")
	if err != nil {
		return apifs.AdminTenantCreateRequest{}, nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	request := apifs.AdminTenantCreateRequest{Labels: cloneLabels(opts.Labels)}
	if opts.DisplayName != nil {
		request.DisplayName = *opts.DisplayName
	}
	return request, client, creds, endpoint, nil
}

func (s Service) adminDeleteInputs(opts DeleteFileSystemOptions) (string, *apifs.Client, apifs.TiDBCloudCredentials, endpoints.Endpoint, error) {
	id, client, creds, err := s.adminItemInputs(opts.Profile, opts.FileSystemID, authz.FSVolumeDelete, "delete a file system")
	if err != nil {
		return "", nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveFS(opts.Profile)
	if err != nil {
		return "", nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	return id, client, creds, endpoint, nil
}

func (s Service) adminItemInputs(profile *config.Profile, fileSystemID string, permission authz.Permission, action string) (string, *apifs.Client, apifs.TiDBCloudCredentials, error) {
	id, err := fscred.ValidateFileSystemID(fileSystemID)
	if err != nil {
		return "", nil, apifs.TiDBCloudCredentials{}, err
	}
	client, creds, _, err := s.adminTenantClient(profile, permission, action)
	return id, client, creds, err
}

func (s Service) adminTenantClient(profile *config.Profile, permission authz.Permission, action string) (*apifs.Client, apifs.TiDBCloudCredentials, endpoints.Endpoint, error) {
	creds, err := auth.ValidateProfile(profile)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	endpoint, err := s.resolveFS(profile)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	raw, err := api.New(api.Options{
		Endpoint: endpoint, ProfileName: creds.ProfileName, Permission: permission, Action: action,
		HTTPClient: s.HTTPClient, Transport: s.Transport, Timeout: s.Timeout, Debug: s.Debug, DebugWriter: s.DebugWriter,
		Redactor: apitransport.Redactor{Secrets: []string{creds.PublicKey, creds.PrivateKey}}, UserAgent: "ti fs control plane",
	})
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	return apifs.New(raw), apifs.TiDBCloudCredentials{PublicKey: creds.PublicKey, PrivateKey: creds.PrivateKey}, endpoint, nil
}

func validateAdminTenantListOptions(opts ListFileSystemsOptions) error {
	if _, err := auth.ValidateProfile(opts.Profile); err != nil {
		return err
	}
	if opts.DisplayName != nil {
		value := *opts.DisplayName
		if value == "" || strings.ContainsAny(value, "%_") || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return apperr.New("fs.invalid_display_name", "usage", 2, invalidDisplayFilterMessage)
		}
	}
	if opts.Label != nil {
		if !isLabelKey(opts.Label.Key) {
			return apperr.New("fs.invalid_label", "usage", 2, invalidLabelKeyMessage)
		}
		if !isLabelValue(opts.Label.Value) {
			return apperr.New("fs.invalid_label", "usage", 2, invalidLabelValueMessage)
		}
	}
	return nil
}

func validateCreateMetadata(displayName *string, labels map[string]string) error {
	if displayName != nil {
		if err := validateDisplayName(*displayName); err != nil {
			return err
		}
	}
	if len(labels) > maxTenantLabels {
		return apperr.New("fs.invalid_label", "usage", 2, fmt.Sprintf("at most %d --label values are allowed", maxTenantLabels))
	}
	for key, value := range labels {
		if !isLabelKey(key) {
			return apperr.New("fs.invalid_label", "usage", 2, invalidLabelKeyMessage)
		}
		if !isLabelValue(value) {
			return apperr.New("fs.invalid_label", "usage", 2, invalidLabelValueMessage)
		}
	}
	return nil
}

func validateDisplayName(value string) error {
	if !tenantDisplayNameRegexp.MatchString(value) {
		return apperr.New("fs.invalid_display_name", "usage", 2, invalidDisplayNameMessage)
	}
	return nil
}

func parseLabels(values []string, limit int) (map[string]string, error) {
	if len(values) > limit {
		return nil, apperr.New("fs.invalid_label", "usage", 2, fmt.Sprintf("at most %d --label values are allowed", limit))
	}
	labels := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, err := parseLabel(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := labels[key]; exists {
			return nil, apperr.New("fs.invalid_label", "usage", 2, fmt.Sprintf("duplicate label key %q", key))
		}
		labels[key] = value
	}
	return labels, nil
}

func parseLabel(raw string) (string, string, error) {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || !isLabelKey(key) {
		return "", "", apperr.New("fs.invalid_label", "usage", 2, invalidLabelKeyMessage)
	}
	if !isLabelValue(value) {
		return "", "", apperr.New("fs.invalid_label", "usage", 2, invalidLabelValueMessage)
	}
	return key, value, nil
}

func isLabelKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) > 2 {
		return false
	}
	name := parts[len(parts)-1]
	if len(name) == 0 || len(name) > maxLabelNameLength || !utf8.ValidString(name) || !labelNameRegexp.MatchString(name) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	prefix := parts[0]
	if len(prefix) == 0 || len(prefix) > maxLabelPrefixLength || !utf8.ValidString(prefix) || !labelDNSPrefixRegexp.MatchString(prefix) {
		return false
	}
	for _, segment := range strings.Split(prefix, ".") {
		if len(segment) > 63 {
			return false
		}
	}
	return true
}

func isLabelValue(value string) bool {
	return value == "" || (len(value) <= maxLabelValueLength && utf8.ValidString(value) && labelNameRegexp.MatchString(value))
}

func mapAdminTenant(tenant apifs.AdminTenant, regionCode string, hasLocalToken bool) FileSystemSummary {
	return FileSystemSummary{
		FileSystemID: tenant.TenantID, DisplayName: effectiveTenantDisplayName(tenant.TenantID, tenant.DisplayName), Labels: cloneLabels(tenant.Labels),
		RegionCode: regionCode, Status: tenant.Status, Kind: tenant.Kind, Quota: tenant.Quota, HasLocalToken: hasLocalToken,
	}
}

func effectiveTenantDisplayName(fileSystemID, displayName string) string {
	if strings.TrimSpace(displayName) == "" {
		return fileSystemID
	}
	return displayName
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}

func apiContractError(message string, cause error) error {
	if cause == nil {
		return apperr.New("fs.api_contract", "api", 1, message)
	}
	return apperr.Wrap("fs.api_contract", "api", 1, message, cause)
}

func mapAdminTenantError(err error, operation, fileSystemID, regionCode string) error {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.StatusCode == http.StatusConflict && operation == "create" {
		return apperr.Wrap("fs.display_name_conflict", "api", 1, "display name conflicts with an existing file system in the organization", err)
	}
	if apiErr.StatusCode == http.StatusNotFound {
		switch operation {
		case "create", "list":
			return apperr.Wrap("fs.control_plane_unavailable", "api", 1, fmt.Sprintf("file system control plane is unavailable in region %q", regionCode), err)
		default:
			return remoteFileSystemNotFound(fileSystemID, err)
		}
	}
	if apiErr.StatusCode >= http.StatusInternalServerError && apiErr.RequestID != "" {
		return apperr.New(apiErr.Code, apiErr.Category, apiErr.ExitCode, fmt.Sprintf("%s (request ID: %s)", apiErr.Message, apiErr.RequestID))
	}
	return err
}
