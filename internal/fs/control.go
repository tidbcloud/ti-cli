package fs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

type Service struct {
	Resolver                endpoints.Resolver
	HTTPClient              *http.Client
	Transport               http.RoundTripper
	Timeout                 time.Duration
	FSReadyWaitTimeout      time.Duration
	FSReadyWaitPollInterval time.Duration
	Debug                   bool
	DebugWriter             io.Writer
	HomeDir                 string

	CompanionPath string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

type CreateFileSystemOptions struct {
	Profile        *config.Profile
	WaitUntilReady bool
	DisplayName    *string
	Labels         map[string]string
}

type LabelFilter struct {
	Key   string
	Value string
}

type ListFileSystemsOptions struct {
	Profile     *config.Profile
	DisplayName *string
	Label       *LabelFilter
}

type DeleteFileSystemOptions struct {
	Profile      *config.Profile
	FileSystemID string
}

type CheckFileSystemOptions struct {
	Profile *config.Profile
}

type ImportFileSystemTokenOptions struct {
	Profile      *config.Profile
	FileSystemID string
	Token        string
	Replace      bool
}

type FileSystemSummary struct {
	FileSystemID  string                  `json:"file_system_id"`
	DisplayName   string                  `json:"display_name"`
	Labels        map[string]string       `json:"labels"`
	RegionCode    string                  `json:"region_code,omitempty"`
	Status        string                  `json:"status,omitempty"`
	Kind          string                  `json:"kind,omitempty"`
	Quota         *apifs.AdminTenantQuota `json:"quota,omitempty"`
	HasLocalToken bool                    `json:"has_local_token"`
}

type ListFileSystemsResult struct {
	RegionCode  string              `json:"region_code"`
	FileSystems []FileSystemSummary `json:"file_systems"`
}

type DescribeFileSystemResult struct {
	FileSystemSummary
}

type FileSystemResult struct {
	FileSystemID      string            `json:"file_system_id"`
	DisplayName       string            `json:"display_name"`
	Labels            map[string]string `json:"labels"`
	RegionCode        string            `json:"region_code,omitempty"`
	FSToken           string            `json:"fs_token,omitempty"`
	Status            string            `json:"status"`
	CredentialsStored bool              `json:"credentials_stored"`
}

type DeleteResult struct {
	FileSystemID        string `json:"file_system_id"`
	Status              string `json:"status"`
	CredentialsRemoved  bool   `json:"credentials_removed"`
	RemoteDeletionState string `json:"remote_deletion_state,omitempty"`
}

type ImportFileSystemTokenResult struct {
	FileSystemID      string `json:"file_system_id"`
	RegionCode        string `json:"region_code"`
	CredentialsStored bool   `json:"credentials_stored"`
	Status            string `json:"status"`
}

type CheckResult struct {
	Status   string                `json:"status"`
	Profile  string                `json:"profile"`
	Resource fscred.Credential     `json:"resource"`
	Endpoint *endpoints.Endpoint   `json:"endpoint,omitempty"`
	Remote   *apifs.StatusResponse `json:"remote,omitempty"`
	Checks   []Check               `json:"checks"`
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (s Service) CreateFileSystem(ctx context.Context, opts CreateFileSystemOptions) (FileSystemResult, error) {
	return s.createFileSystem(ctx, opts)
}

func (s Service) DeleteFileSystem(ctx context.Context, opts DeleteFileSystemOptions) (DeleteResult, error) {
	return s.deleteFileSystem(ctx, opts)
}

func (s Service) CheckFileSystem(ctx context.Context, opts CheckFileSystemOptions) (CheckResult, error) {
	return s.drive9CheckFileSystem(ctx, opts)
}

func (s Service) ListFileSystems(ctx context.Context, opts ListFileSystemsOptions) (ListFileSystemsResult, error) {
	return s.listFileSystems(ctx, opts)
}

func (s Service) DescribeFileSystem(ctx context.Context, profile *config.Profile, fileSystemID string) (DescribeFileSystemResult, error) {
	return s.describeFileSystem(ctx, profile, fileSystemID)
}

func (s Service) ImportFileSystemToken(ctx context.Context, opts ImportFileSystemTokenOptions) (ImportFileSystemTokenResult, error) {
	return s.importFileSystemToken(ctx, opts, true)
}

func (s Service) DryRunImportFileSystemToken(ctx context.Context, commandPath string, opts ImportFileSystemTokenOptions) (dryrun.Result, error) {
	result, err := s.importFileSystemToken(ctx, opts, false)
	if err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"import_file_system_token",
		dryrun.RequestSummary{
			Method:      "EXEC",
			Path:        "ti-drive9 fs stat --output json :/",
			Description: "the companion verifies access to the remote root; normal execution then stores the token in the selected local profile namespace",
		},
		dryrun.Check{Name: "token_validation", Status: "passed", Message: result.FileSystemID},
		dryrun.Check{Name: "credential_destination", Status: "passed", Message: profileName(opts.Profile)},
	), nil
}

func (s Service) DryRunCreateFileSystem(ctx context.Context, commandPath string, opts CreateFileSystemOptions) (dryrun.Result, error) {
	request, _, _, endpoint, err := s.adminCreateInputs(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSVolumeCreate)},
		{Name: "remote_identity", Status: "passed", Message: "the File System backend assigns file_system_id"},
	}
	checks = append(checks, endpointDryRunCheck(endpoint, nil))
	if opts.WaitUntilReady {
		checks = append(checks, dryrun.Check{
			Name:    "post_create_wait",
			Status:  "passed",
			Message: fmt.Sprintf("normal execution waits up to %s for the Drive9 data plane root to become readable", s.fsReadyWaitTimeout()),
		})
	}
	return dryrun.New(
		commandPath,
		"create_file_system",
		dryrun.RequestSummary{
			Method:      http.MethodPost,
			Path:        "/v1/admin/tenants",
			Body:        request,
			Description: "normal execution authenticates with TiDB Cloud API-key headers; credential values are not included in the request body or dry-run output",
		},
		checks...,
	), nil
}

func (s Service) DryRunDeleteFileSystem(ctx context.Context, commandPath string, opts DeleteFileSystemOptions) (dryrun.Result, error) {
	fileSystemID, _, _, endpoint, err := s.adminDeleteInputs(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSVolumeDelete)},
	}
	checks = append(checks, dryrun.Check{Name: "file_system_id", Status: "passed", Message: fileSystemID})
	homeDir, err := s.homeDir()
	if err != nil {
		return dryrun.Result{}, err
	}
	credentialPaths, err := fscred.CredentialPath(homeDir, profileName(opts.Profile), fileSystemID)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks = append(checks, dryrun.Check{
		Name:    "local_credentials",
		Status:  "passed",
		Message: fmt.Sprintf("would remove %s after Drive9 accepts deletion if it exists", credentialPaths.Credentials),
	})
	checks = append(checks, endpointDryRunCheck(endpoint, nil))
	return dryrun.New(
		commandPath,
		"delete_file_system",
		dryrun.RequestSummary{
			Method:      http.MethodDelete,
			Path:        "/v1/admin/tenants/" + fileSystemID,
			Description: "normal execution authenticates with TiDB Cloud API-key headers and removes matching local credentials only after the backend accepts deletion",
		},
		checks...,
	), nil
}

func (s Service) resolveFS(profile *config.Profile) (endpoints.Endpoint, error) {
	provider := profile.FSCloudProvider
	regionCode := profile.FSRegionCode
	if provider == "" {
		provider = profile.CloudProvider
	}
	if regionCode == "" {
		regionCode = profile.RegionCode
	}
	return s.resolver().ResolveFS(provider, regionCode)
}

func (s Service) bearerClient(profile *config.Profile, endpoint endpoints.Endpoint, permission authz.Permission, action string) (*apifs.Client, error) {
	if profile == nil {
		return nil, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	client, err := api.NewBearerClient(profileName(profile), profile.FSAPIKey, endpoint, permission, api.Options{
		Action:      action,
		HTTPClient:  s.HTTPClient,
		Transport:   s.Transport,
		Timeout:     s.Timeout,
		Debug:       s.Debug,
		DebugWriter: s.DebugWriter,
		UserAgent:   "ti fs legacy helper",
	})
	if err != nil {
		return nil, err
	}
	return apifs.New(client), nil
}

func (s Service) resolver() endpoints.Resolver {
	if s.Resolver.IsZero() {
		return endpoints.NewResolver()
	}
	return s.Resolver
}

func (s Service) homeDir() (string, error) {
	if s.HomeDir != "" {
		return s.HomeDir, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap("fs.home_dir", "config", 1, "cannot determine home directory", err)
	}
	return homeDir, nil
}

func (s Service) metadataStore(profile *config.Profile) (*fsMetadataStore, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return nil, err
	}
	return newFSMetadataStore(homeDir, profile)
}

func validateProfile(profile *config.Profile) error {
	if _, err := auth.ValidateProfile(profile); err != nil {
		return err
	}
	return nil
}

func endpointDryRunCheck(endpoint endpoints.Endpoint, err error) dryrun.Check {
	if err != nil {
		return dryrun.Check{Name: "endpoint_selection", Status: "skipped", Message: apperr.MessageFor(err)}
	}
	return dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)}
}

func checkResult(profile *config.Profile, resource fscred.Credential, endpoint *endpoints.Endpoint, remote *apifs.StatusResponse, checks []Check) CheckResult {
	status := "passed"
	for _, check := range checks {
		if check.Status == "failed" {
			status = "failed"
			break
		}
		if check.Status == "warning" && status == "passed" {
			status = "warning"
		}
	}
	return CheckResult{
		Status:   status,
		Profile:  profileName(profile),
		Resource: resource,
		Endpoint: endpoint,
		Remote:   remote,
		Checks:   checks,
	}
}

func profileName(profile *config.Profile) string {
	if profile == nil || profile.Name == "" {
		return config.DefaultProfile
	}
	return profile.Name
}

func (r FileSystemResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Display name: " + r.DisplayName,
		"Labels: " + humanLabels(r.Labels),
		"Status: " + r.Status,
	}
	if r.RegionCode != "" {
		lines = append(lines, "Region: "+r.RegionCode)
	}
	if r.CredentialsStored {
		lines = append(lines, "Credentials: stored locally")
	}
	if r.FSToken != "" {
		lines = append(lines, "FS token: "+r.FSToken)
	}
	return strings.Join(lines, "\n")
}

func (r ListFileSystemsResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "FILE_SYSTEM_ID\tDISPLAY_NAME\tREGION\tSTATUS\tKIND\tLOCAL_TOKEN")
	for _, fileSystem := range r.FileSystems {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%t\n", fileSystem.FileSystemID, fileSystem.DisplayName, fileSystem.RegionCode, fileSystem.Status, fileSystem.Kind, fileSystem.HasLocalToken)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r DescribeFileSystemResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Display name: " + r.DisplayName,
		"Labels: " + humanLabels(r.Labels),
		"Region: " + r.RegionCode,
		"Status: " + r.Status,
		"Kind: " + r.Kind,
		fmt.Sprintf("Local token: %t", r.HasLocalToken),
	}
	if r.Quota != nil {
		lines = append(lines, fmt.Sprintf("Quota: %v", r.Quota))
	}
	return strings.Join(lines, "\n")
}

func humanLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+labels[key])
	}
	return strings.Join(values, ", ")
}

func (r DeleteResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Status: " + r.Status,
	}
	if r.RemoteDeletionState != "" {
		lines = append(lines, "Remote deletion state: "+r.RemoteDeletionState)
	}
	if r.CredentialsRemoved {
		lines = append(lines, "Credentials: removed from ~/.ti/fs_credentials")
	}
	return strings.Join(lines, "\n")
}

func (r ImportFileSystemTokenResult) Human() string {
	return strings.Join([]string{
		"File system ID: " + r.FileSystemID,
		"Region: " + r.RegionCode,
		"Status: " + r.Status,
		"Credentials: stored locally",
	}, "\n")
}

func (r CheckResult) Human() string {
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "ti fs check: %s\n", r.Status)
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "CHECK\tSTATUS\tMESSAGE")
	for _, check := range r.Checks {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Name, check.Status, check.Message)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
