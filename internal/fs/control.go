package fs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/tdc/internal/api"
	"github.com/tidbcloud/tdc/internal/api/endpoints"
	apifs "github.com/tidbcloud/tdc/internal/api/fs"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/auth"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
	"github.com/tidbcloud/tdc/internal/dryrun"
	"github.com/tidbcloud/tdc/internal/fs/fscred"
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
	FileSystemName string
	WaitUntilReady bool
}

type DeleteFileSystemOptions struct {
	Profile        *config.Profile
	FileSystemName string
}

type CheckFileSystemOptions struct {
	Profile *config.Profile
}

type DescribeFileSystemResult struct {
	Profile string `json:"profile"`
	fscred.Resource
	Drive9Home string `json:"drive9_home"`
}

type FileSystemResult struct {
	FileSystemName    string `json:"file_system_name"`
	TenantID          string `json:"tenant_id,omitempty"`
	CloudProvider     string `json:"cloud_provider,omitempty"`
	RegionCode        string `json:"region_code,omitempty"`
	FSToken           string `json:"fs_token,omitempty"`
	Status            string `json:"status"`
	CredentialsStored bool   `json:"credentials_stored"`
}

type DeleteResult struct {
	FileSystemName      string `json:"file_system_name"`
	TenantID            string `json:"tenant_id,omitempty"`
	Status              string `json:"status"`
	CredentialsRemoved  bool   `json:"credentials_removed"`
	RemoteDeletionState string `json:"remote_deletion_state,omitempty"`
}

type CheckResult struct {
	Status   string                `json:"status"`
	Profile  string                `json:"profile"`
	Resource fscred.Resource       `json:"resource"`
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
	return s.drive9CreateFileSystem(ctx, opts)
}

func (s Service) DeleteFileSystem(ctx context.Context, opts DeleteFileSystemOptions) (DeleteResult, error) {
	return s.drive9DeleteFileSystem(ctx, opts)
}

func (s Service) CheckFileSystem(ctx context.Context, opts CheckFileSystemOptions) (CheckResult, error) {
	return s.drive9CheckFileSystem(ctx, opts)
}

func (s Service) ListFileSystems(_ context.Context, profile *config.Profile) (fscred.ListResult, error) {
	homeDir, err := s.homeDir()
	if err != nil {
		return fscred.ListResult{}, err
	}
	if err := fscred.MigrateLegacy(homeDir, profile); err != nil {
		return fscred.ListResult{}, err
	}
	resources, err := fscred.List(homeDir, profileName(profile))
	if err != nil {
		return fscred.ListResult{}, err
	}
	return fscred.ListResult{Profile: profileName(profile), FileSystems: resources}, nil
}

func (s Service) DescribeFileSystem(_ context.Context, profile *config.Profile) (DescribeFileSystemResult, error) {
	resource := fscred.FromProfile(profile)
	homeDir, err := s.homeDir()
	if err != nil {
		return DescribeFileSystemResult{}, err
	}
	drive9Home, err := fscred.CompanionHome(homeDir, profileName(profile), resource.Name)
	if err != nil {
		return DescribeFileSystemResult{}, err
	}
	return DescribeFileSystemResult{Profile: profileName(profile), Resource: resource, Drive9Home: drive9Home}, nil
}

func (s Service) DryRunCreateFileSystem(ctx context.Context, commandPath string, opts CreateFileSystemOptions) (dryrun.Result, error) {
	request, name, endpoint, endpointErr, err := s.createDryRunInputs(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSVolumeCreate)},
		{Name: "file_system_name", Status: "passed", Message: name},
	}
	checks = append(checks, endpointDryRunCheck(endpoint, endpointErr))
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
			Method: http.MethodPost,
			Path:   "/v1/provision",
			Body:   redactedProvisionRequest(request),
		},
		checks...,
	), nil
}

func (s Service) DryRunDeleteFileSystem(ctx context.Context, commandPath string, opts DeleteFileSystemOptions) (dryrun.Result, error) {
	name, endpoint, endpointErr, err := s.deleteDryRunInputs(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSVolumeDelete)},
	}
	if resource := fscred.FromProfile(opts.Profile); resource.HasAPIKey {
		checks = append(checks, dryrun.Check{Name: "fs_resource_credentials", Status: "passed", Message: name})
	} else {
		checks = append(checks, dryrun.Check{Name: "fs_resource_credentials", Status: "warning", Message: "fs_api_key is not configured; normal execution would fail before remote deletion"})
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return dryrun.Result{}, err
	}
	registryPaths, err := fscred.Paths(homeDir, profileName(opts.Profile), name)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks = append(checks, dryrun.Check{
		Name:    "local_resource_registry",
		Status:  "passed",
		Message: fmt.Sprintf("would remove %s and %s", registryPaths.Config, registryPaths.Credentials),
	})
	checks = append(checks, endpointDryRunCheck(endpoint, endpointErr))
	body, bodyErr := deprovisionRequest(opts.Profile)
	if bodyErr != nil {
		return dryrun.Result{}, bodyErr
	}
	return dryrun.New(
		commandPath,
		"delete_file_system",
		dryrun.RequestSummary{
			Method:      http.MethodDelete,
			Path:        "/v1/tenant",
			Body:        redactedDeprovisionRequest(body),
			Description: "normal execution uses the stored tdc fs API key before deleting",
		},
		checks...,
	), nil
}

func (s Service) createRequestAndEndpoint(opts CreateFileSystemOptions, requireEndpoint bool) (apifs.ProvisionRequest, string, endpoints.Endpoint, error) {
	request, name, endpoint, endpointErr, err := s.createDryRunInputs(opts)
	if err != nil {
		return apifs.ProvisionRequest{}, "", endpoints.Endpoint{}, err
	}
	if endpointErr != nil && requireEndpoint {
		return apifs.ProvisionRequest{}, "", endpoints.Endpoint{}, endpointErr
	}
	return request, name, endpoint, nil
}

func (s Service) createDryRunInputs(opts CreateFileSystemOptions) (apifs.ProvisionRequest, string, endpoints.Endpoint, error, error) {
	creds, err := auth.ValidateProfile(opts.Profile)
	if err != nil {
		return apifs.ProvisionRequest{}, "", endpoints.Endpoint{}, nil, err
	}
	name, err := fileSystemName(opts.FileSystemName)
	if err != nil {
		return apifs.ProvisionRequest{}, "", endpoints.Endpoint{}, nil, err
	}
	endpoint, endpointErr := s.resolveFS(opts.Profile)
	request := apifs.ProvisionRequest{
		PublicKey:  creds.PublicKey,
		PrivateKey: creds.PrivateKey,
	}
	return request, name, endpoint, endpointErr, nil
}

func (s Service) deleteInputsAndEndpoint(opts DeleteFileSystemOptions, requireEndpoint bool) (string, endpoints.Endpoint, error) {
	name, endpoint, endpointErr, err := s.deleteDryRunInputs(opts)
	if err != nil {
		return "", endpoints.Endpoint{}, err
	}
	if endpointErr != nil && requireEndpoint {
		return "", endpoints.Endpoint{}, endpointErr
	}
	return name, endpoint, nil
}

func (s Service) deleteDryRunInputs(opts DeleteFileSystemOptions) (string, endpoints.Endpoint, error, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return "", endpoints.Endpoint{}, nil, err
	}
	name, err := fileSystemName(opts.FileSystemName)
	if err != nil {
		return "", endpoints.Endpoint{}, nil, err
	}
	if opts.Profile.FSResourceName != name {
		return "", endpoints.Endpoint{}, nil, resourceMismatch(opts.Profile.FSResourceName, name)
	}
	endpoint, endpointErr := s.resolveFS(opts.Profile)
	return name, endpoint, endpointErr, nil
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
		UserAgent:   "tdc fs legacy helper",
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

func deprovisionRequest(profile *config.Profile) (apifs.DeprovisionRequest, error) {
	creds, err := auth.ValidateProfile(profile)
	if err != nil {
		return apifs.DeprovisionRequest{}, err
	}
	return apifs.DeprovisionRequest{
		PublicKey:  creds.PublicKey,
		PrivateKey: creds.PrivateKey,
	}, nil
}

type redactedProvisionBody struct {
	PublicKey  string `json:"public_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

type redactedDeprovisionBody struct {
	PublicKey  string `json:"public_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

func redactedProvisionRequest(request apifs.ProvisionRequest) redactedProvisionBody {
	return redactedProvisionBody{
		PublicKey:  redactedConfiguredValue(request.PublicKey),
		PrivateKey: redactedSecretValue(request.PrivateKey),
	}
}

func redactedDeprovisionRequest(request apifs.DeprovisionRequest) redactedDeprovisionBody {
	return redactedDeprovisionBody{
		PublicKey:  redactedConfiguredValue(request.PublicKey),
		PrivateKey: redactedSecretValue(request.PrivateKey),
	}
}

func redactedConfiguredValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "[configured]"
}

func redactedSecretValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "[redacted]"
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

func fileSystemName(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", apperr.New("fs.missing_file_system_name", "usage", 2, "--file-system-name is required")
	}
	if len(trimmed) > 64 || strings.Contains(trimmed, "/") {
		return "", apperr.New("fs.invalid_file_system_name", "usage", 2, "--file-system-name must be 1-64 characters and must not contain /")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", apperr.New("fs.invalid_file_system_name", "usage", 2, "--file-system-name must not contain control characters")
		}
	}
	return trimmed, nil
}

func resourceMismatch(existing, requested string) error {
	return apperr.New(
		"fs.resource_name_mismatch",
		"usage",
		2,
		fmt.Sprintf("profile is already configured for tdc fs resource %q; use that name or delete it before creating %q", existing, requested),
	)
}

func endpointDryRunCheck(endpoint endpoints.Endpoint, err error) dryrun.Check {
	if err != nil {
		return dryrun.Check{Name: "endpoint_selection", Status: "skipped", Message: apperr.MessageFor(err)}
	}
	return dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)}
}

func checkResult(profile *config.Profile, resource fscred.Resource, endpoint *endpoints.Endpoint, remote *apifs.StatusResponse, checks []Check) CheckResult {
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
		"File system: " + r.FileSystemName,
		"Status: " + r.Status,
	}
	if r.TenantID != "" {
		lines = append(lines, "Tenant ID: "+r.TenantID)
	}
	if r.CloudProvider != "" || r.RegionCode != "" {
		lines = append(lines, "Location: "+strings.TrimSpace(r.CloudProvider+" "+r.RegionCode))
	}
	if r.CredentialsStored {
		lines = append(lines, "Credentials: stored in ~/.tdc/credentials")
	}
	return strings.Join(lines, "\n")
}

func (r DeleteResult) Human() string {
	lines := []string{
		"File system: " + r.FileSystemName,
		"Status: " + r.Status,
	}
	if r.TenantID != "" {
		lines = append(lines, "Tenant ID: "+r.TenantID)
	}
	if r.RemoteDeletionState != "" {
		lines = append(lines, "Remote deletion state: "+r.RemoteDeletionState)
	}
	if r.CredentialsRemoved {
		lines = append(lines, "Credentials: removed from ~/.tdc/credentials")
	}
	return strings.Join(lines, "\n")
}

func (r CheckResult) Human() string {
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "tdc fs check: %s\n", r.Status)
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "CHECK\tSTATUS\tMESSAGE")
	for _, check := range r.Checks {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Name, check.Status, check.Message)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
