package aiconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

const (
	MediaTypeImage = "image"
	MediaTypeAudio = "audio"
	MediaTypeVideo = "video"

	ProtocolOpenAI  = "openai"
	ProtocolQwenASR = "qwen-asr"

	maxPromptBytes = 8 * 1024
)

type Service struct {
	Resolver    endpoints.Resolver
	HTTPClient  *http.Client
	Transport   http.RoundTripper
	Timeout     time.Duration
	Debug       bool
	DebugWriter io.Writer
}

type DescribeExtractOptions struct {
	Profile      *config.Profile
	FileSystemID string
	MediaType    string
}

type UpdateExtractOptions struct {
	Profile                *config.Profile
	FileSystemID           string
	MediaType              string
	Enabled                *bool
	ProviderAPIBase        *string
	ProviderModel          *string
	ProviderProtocol       *string
	Prompt                 *string
	ProviderAPIKey         string
	ProviderAPIKeySupplied bool
}

type DescribeEmbeddingOptions struct {
	Profile      *config.Profile
	FileSystemID string
}

type UpdateEmbeddingOptions struct {
	Profile                *config.Profile
	FileSystemID           string
	Enabled                bool
	ProviderAPIBase        *string
	ProviderModel          *string
	ProviderAPIKey         string
	ProviderAPIKeySupplied bool
}

type ExtractResult struct {
	FileSystemID string     `json:"file_system_id"`
	MediaType    string     `json:"media_type"`
	Enabled      bool       `json:"enabled"`
	APIBase      *string    `json:"api_base,omitempty"`
	APIKey       *string    `json:"api_key,omitempty"`
	Model        *string    `json:"model,omitempty"`
	Protocol     *string    `json:"protocol,omitempty"`
	Prompt       *string    `json:"prompt,omitempty"`
	Source       string     `json:"source"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

type EmbeddingResult struct {
	FileSystemID string     `json:"file_system_id"`
	Enabled      bool       `json:"enabled"`
	APIBase      *string    `json:"api_base,omitempty"`
	APIKey       *string    `json:"api_key,omitempty"`
	Model        *string    `json:"model,omitempty"`
	Source       string     `json:"source"`
	Generation   uint64     `json:"generation,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

func (s Service) DescribeExtract(ctx context.Context, opts DescribeExtractOptions) (ExtractResult, error) {
	id, mediaType, client, creds, err := s.extractInputs(opts.Profile, opts.FileSystemID, opts.MediaType, authz.FSExtractConfigRead, "describe file system extract configuration", "")
	if err != nil {
		return ExtractResult{}, err
	}
	response, err := client.GetAdminTenantExtractConfiguration(ctx, creds, id, apifs.ExtractMediaType(mediaType))
	if err != nil {
		return ExtractResult{}, mapError(err, "describe", "extract", id, mediaType)
	}
	if err := validateResponse(response.Source, response.APIKey); err != nil {
		return ExtractResult{}, err
	}
	return extractResult(id, mediaType, response), nil
}

func (s Service) UpdateExtract(ctx context.Context, opts UpdateExtractOptions) (ExtractResult, error) {
	request, err := validateExtractUpdate(opts)
	if err != nil {
		return ExtractResult{}, err
	}
	id, mediaType, client, creds, err := s.extractInputs(opts.Profile, opts.FileSystemID, opts.MediaType, authz.FSExtractConfigUpdate, "update file system extract configuration", opts.ProviderAPIKey)
	if err != nil {
		return ExtractResult{}, err
	}
	response, err := client.UpdateAdminTenantExtractConfiguration(ctx, creds, id, apifs.ExtractMediaType(mediaType), request)
	if err != nil {
		return ExtractResult{}, mapError(err, "update", "extract", id, mediaType)
	}
	if err := validateResponse(response.Source, response.APIKey); err != nil {
		return ExtractResult{}, err
	}
	return extractResult(id, mediaType, response), nil
}

func (s Service) DescribeEmbedding(ctx context.Context, opts DescribeEmbeddingOptions) (EmbeddingResult, error) {
	id, client, creds, err := s.embeddingInputs(opts.Profile, opts.FileSystemID, authz.FSEmbeddingConfigRead, "describe file system embedding configuration", "")
	if err != nil {
		return EmbeddingResult{}, err
	}
	response, err := client.GetAdminTenantEmbeddingConfiguration(ctx, creds, id)
	if err != nil {
		return EmbeddingResult{}, mapError(err, "describe", "embedding", id, "")
	}
	if err := validateResponse(response.Source, response.APIKey); err != nil {
		return EmbeddingResult{}, err
	}
	return embeddingResult(id, response), nil
}

func (s Service) UpdateEmbedding(ctx context.Context, opts UpdateEmbeddingOptions) (EmbeddingResult, error) {
	request, err := validateEmbeddingUpdate(opts)
	if err != nil {
		return EmbeddingResult{}, err
	}
	id, client, creds, err := s.embeddingInputs(opts.Profile, opts.FileSystemID, authz.FSEmbeddingConfigUpdate, "update file system embedding configuration", opts.ProviderAPIKey)
	if err != nil {
		return EmbeddingResult{}, err
	}
	response, err := client.UpdateAdminTenantEmbeddingConfiguration(ctx, creds, id, request)
	if err != nil {
		return EmbeddingResult{}, mapError(err, "update", "embedding", id, "")
	}
	if err := validateResponse(response.Source, response.APIKey); err != nil {
		return EmbeddingResult{}, err
	}
	return embeddingResult(id, response), nil
}

func (s Service) DryRunUpdateExtract(command string, opts UpdateExtractOptions) (dryrun.Result, error) {
	request, err := validateExtractUpdate(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	id, mediaType, _, _, err := s.extractInputs(opts.Profile, opts.FileSystemID, opts.MediaType, authz.FSExtractConfigUpdate, "update file system extract configuration", opts.ProviderAPIKey)
	if err != nil {
		return dryrun.Result{}, err
	}
	body := extractDryRunBody(request, opts.ProviderAPIKeySupplied)
	checks := configurationDryRunChecks(opts.Profile, authz.FSExtractConfigUpdate)
	checks = append(checks, dryrun.Check{Name: "provider_validation", Status: "skipped", Message: "dry-run does not contact the configured provider"})
	return dryrun.New(command, "update_file_system_extract_configuration", dryrun.RequestSummary{
		Method: http.MethodPut,
		Path:   "/v1/admin/tenants/" + url.PathEscape(id) + "/extract-config/" + url.PathEscape(mediaType),
		Body:   body,
	}, checks...), nil
}

func (s Service) DryRunUpdateEmbedding(command string, opts UpdateEmbeddingOptions) (dryrun.Result, error) {
	request, err := validateEmbeddingUpdate(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	id, _, _, err := s.embeddingInputs(opts.Profile, opts.FileSystemID, authz.FSEmbeddingConfigUpdate, "update file system embedding configuration", opts.ProviderAPIKey)
	if err != nil {
		return dryrun.Result{}, err
	}
	body := embeddingDryRunBody(request, opts.ProviderAPIKeySupplied)
	checks := configurationDryRunChecks(opts.Profile, authz.FSEmbeddingConfigUpdate)
	checks = append(checks, dryrun.Check{Name: "provider_validation", Status: "skipped", Message: "dry-run does not contact the configured provider"})
	return dryrun.New(command, "update_file_system_embedding_configuration", dryrun.RequestSummary{
		Method: http.MethodPut,
		Path:   "/v1/admin/tenants/" + url.PathEscape(id) + "/embedding-config",
		Body:   body,
	}, checks...), nil
}

func configurationDryRunChecks(profile *config.Profile, permission authz.Permission) []dryrun.Check {
	return []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profile.Name)},
		{Name: "endpoint_selection", Status: "passed", Message: profile.PlacementRegionCode},
		{Name: "permission_requirement", Status: "passed", Message: string(permission)},
	}
}

func (s Service) extractInputs(profile *config.Profile, fileSystemID, mediaType string, permission authz.Permission, action, providerAPIKey string) (string, string, *apifs.Client, apifs.TiDBCloudCredentials, error) {
	id, err := validateFileSystemID(fileSystemID)
	if err != nil {
		return "", "", nil, apifs.TiDBCloudCredentials{}, err
	}
	mediaType, err = validateMediaType(mediaType)
	if err != nil {
		return "", "", nil, apifs.TiDBCloudCredentials{}, err
	}
	client, creds, err := s.adminClient(profile, permission, action, providerAPIKey)
	return id, mediaType, client, creds, err
}

func (s Service) embeddingInputs(profile *config.Profile, fileSystemID string, permission authz.Permission, action, providerAPIKey string) (string, *apifs.Client, apifs.TiDBCloudCredentials, error) {
	id, err := validateFileSystemID(fileSystemID)
	if err != nil {
		return "", nil, apifs.TiDBCloudCredentials{}, err
	}
	client, creds, err := s.adminClient(profile, permission, action, providerAPIKey)
	return id, client, creds, err
}

func (s Service) adminClient(profile *config.Profile, permission authz.Permission, action, providerAPIKey string) (*apifs.Client, apifs.TiDBCloudCredentials, error) {
	creds, err := auth.ValidateProfile(profile)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, err
	}
	provider := profile.FSCloudProvider
	regionCode := profile.FSRegionCode
	if provider == "" {
		provider = profile.CloudProvider
	}
	if regionCode == "" {
		regionCode = profile.RegionCode
	}
	resolver := s.Resolver
	if resolver.IsZero() {
		resolver = endpoints.NewResolver()
	}
	endpoint, err := resolver.ResolveFS(provider, regionCode)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, err
	}
	raw, err := api.New(api.Options{
		Endpoint: endpoint, ProfileName: creds.ProfileName, Permission: permission, Action: action,
		HTTPClient: s.HTTPClient, Transport: s.Transport, Timeout: s.Timeout, Debug: s.Debug, DebugWriter: s.DebugWriter,
		Redactor: apitransport.Redactor{Secrets: []string{creds.PublicKey, creds.PrivateKey, providerAPIKey}}, UserAgent: "ti fs AI configuration",
	})
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, err
	}
	return apifs.New(raw), apifs.TiDBCloudCredentials{PublicKey: creds.PublicKey, PrivateKey: creds.PrivateKey}, nil
}

func validateExtractUpdate(opts UpdateExtractOptions) (apifs.UpdateAdminTenantExtractConfigurationRequest, error) {
	if opts.Enabled == nil && opts.ProviderAPIBase == nil && opts.ProviderModel == nil && opts.ProviderProtocol == nil && opts.Prompt == nil {
		return apifs.UpdateAdminTenantExtractConfigurationRequest{}, usageError("fs.extract_configuration_no_changes", "provide at least one extract configuration option")
	}
	mediaType, err := validateMediaType(opts.MediaType)
	if err != nil {
		return apifs.UpdateAdminTenantExtractConfigurationRequest{}, err
	}
	if opts.Prompt != nil {
		if !utf8.ValidString(*opts.Prompt) || len(*opts.Prompt) > maxPromptBytes {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, usageError("fs.invalid_extract_prompt", fmt.Sprintf("--prompt must be valid UTF-8 and at most %d bytes", maxPromptBytes))
		}
	}
	if opts.Enabled != nil && !*opts.Enabled {
		if opts.ProviderAPIBase != nil || opts.ProviderModel != nil || opts.ProviderProtocol != nil || opts.Prompt != nil {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, usageError("fs.invalid_extract_configuration", "--enabled false cannot be combined with provider or prompt options")
		}
		return apifs.UpdateAdminTenantExtractConfigurationRequest{Enabled: opts.Enabled}, nil
	}

	providerChange := opts.ProviderAPIBase != nil || opts.ProviderModel != nil || (opts.Enabled != nil && *opts.Enabled)
	protocol := opts.ProviderProtocol
	if providerChange && protocol == nil {
		value := ProtocolOpenAI
		protocol = &value
	}
	if protocol != nil {
		normalized := strings.ToLower(strings.TrimSpace(*protocol))
		if normalized == "" {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, usageError("fs.invalid_provider_protocol", "--provider-protocol must be openai, or qwen-asr for audio")
		}
		if normalized != ProtocolOpenAI && !(mediaType == MediaTypeAudio && normalized == ProtocolQwenASR) {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, usageError("fs.invalid_provider_protocol", "--provider-protocol must be openai, or qwen-asr for audio")
		}
		protocol = &normalized
		if mediaType == MediaTypeAudio && opts.ProviderProtocol != nil {
			providerChange = true
		}
	}
	if providerChange {
		if opts.ProviderAPIBase == nil || opts.ProviderModel == nil || !opts.ProviderAPIKeySupplied {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, providerTrioError()
		}
		apiBase := strings.TrimSpace(*opts.ProviderAPIBase)
		model := strings.TrimSpace(*opts.ProviderModel)
		apiKey := strings.TrimSpace(opts.ProviderAPIKey)
		if err := validateProviderFields(apiBase, model, apiKey); err != nil {
			return apifs.UpdateAdminTenantExtractConfigurationRequest{}, err
		}
		opts.ProviderAPIBase = &apiBase
		opts.ProviderModel = &model
		opts.ProviderAPIKey = apiKey
	}
	request := apifs.UpdateAdminTenantExtractConfigurationRequest{
		Enabled: opts.Enabled, APIBase: opts.ProviderAPIBase, Model: opts.ProviderModel, Protocol: protocol, Prompt: opts.Prompt,
	}
	if providerChange {
		key := opts.ProviderAPIKey
		request.APIKey = &key
	}
	return request, nil
}

func validateEmbeddingUpdate(opts UpdateEmbeddingOptions) (apifs.UpdateAdminTenantEmbeddingConfigurationRequest, error) {
	request := apifs.UpdateAdminTenantEmbeddingConfigurationRequest{Enabled: opts.Enabled}
	if !opts.Enabled {
		if opts.ProviderAPIBase != nil || opts.ProviderModel != nil {
			return request, usageError("fs.invalid_embedding_configuration", "--enabled false cannot be combined with provider options")
		}
		return request, nil
	}
	if opts.ProviderAPIBase == nil || opts.ProviderModel == nil || !opts.ProviderAPIKeySupplied {
		return request, providerTrioError()
	}
	apiBase := strings.TrimSpace(*opts.ProviderAPIBase)
	model := strings.TrimSpace(*opts.ProviderModel)
	apiKey := strings.TrimSpace(opts.ProviderAPIKey)
	if err := validateProviderFields(apiBase, model, apiKey); err != nil {
		return request, err
	}
	request.APIBase = &apiBase
	request.Model = &model
	request.APIKey = &apiKey
	return request, nil
}

func validateProviderFields(apiBase, model, apiKey string) error {
	apiBase = strings.TrimSpace(apiBase)
	model = strings.TrimSpace(model)
	apiKey = strings.TrimSpace(apiKey)
	if apiBase == "" || model == "" || apiKey == "" {
		return providerTrioError()
	}
	if strings.Contains(apiKey, "*") {
		return usageError("fs.invalid_ai_provider_key", "TI_FS_AI_PROVIDER_API_KEY must contain a plaintext provider key, not a masked value")
	}
	parsed, err := url.Parse(apiBase)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return usageError("fs.invalid_provider_api_base", "--provider-api-base must be a valid HTTPS URL without user information, query parameters, or a fragment")
	}
	return nil
}

func validateResponse(source string, maskedAPIKey *string) error {
	if strings.TrimSpace(source) == "" {
		return apperr.New("fs.api_contract", "api", 1, "file system AI configuration response did not include source")
	}
	if maskedAPIKey != nil && *maskedAPIKey != "" && !strings.Contains(*maskedAPIKey, "*") {
		return apperr.New("fs.api_contract", "api", 1, "file system AI configuration response returned an unmasked provider API key")
	}
	return nil
}

func validateFileSystemID(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", usageError("fs.missing_file_system_id", "--file-system-id is required; AI configuration requires TiDB Cloud API credentials")
	}
	return fscred.ValidateFileSystemID(value)
}

func validateMediaType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case MediaTypeImage, MediaTypeAudio, MediaTypeVideo:
		return value, nil
	default:
		return "", usageError("fs.invalid_media_type", "--media-type must be one of image, audio, or video")
	}
}

func providerTrioError() error {
	return usageError("fs.incomplete_ai_provider", "--provider-api-base, --provider-model, and TI_FS_AI_PROVIDER_API_KEY must be provided together")
}

func usageError(code, message string) error {
	return apperr.New(code, "usage", 2, message)
}

func mapError(err error, operation, kind, fileSystemID, mediaType string) error {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.StatusCode == http.StatusNotFound {
		return apperr.Wrap("fs.remote_file_system_not_found", "api", 5, fmt.Sprintf("file system %q was not found in the selected region", fileSystemID), err)
	}
	if kind == "embedding" && apiErr.StatusCode == http.StatusConflict {
		return apperr.Wrap("fs.embedding_configuration_not_applicable", "api", 1, "embedding configuration is not applicable because this file system uses database-managed auto embedding (source=database_auto)", err)
	}
	if operation == "update" {
		lower := strings.ToLower(apiErr.Message)
		if apiErr.StatusCode == http.StatusBadRequest && strings.Contains(lower, "provider") {
			return apperr.Wrap("fs.ai_provider_validation_failed", "api", 1, apiErr.Message, err)
		}
		if apiErr.Code == "api.network_error" || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode == http.StatusBadGateway || apiErr.StatusCode == http.StatusGatewayTimeout {
			describe := "ti fs describe-file-system-embedding-configuration --file-system-id " + fileSystemID
			if kind == "extract" {
				describe = "ti fs describe-file-system-extract-configuration --file-system-id " + fileSystemID + " --media-type " + mediaType
			}
			return apperr.Wrap("fs.ai_configuration_update_ambiguous", "api", 1, "the update outcome is unknown and provider validation might have incurred a charge; run `"+describe+"` before deciding whether to retry", err)
		}
	}
	return err
}

func extractResult(fileSystemID, mediaType string, response apifs.AdminTenantExtractConfiguration) ExtractResult {
	return ExtractResult{
		FileSystemID: fileSystemID, MediaType: mediaType, Enabled: response.Enabled, APIBase: response.APIBase,
		APIKey: response.APIKey, Model: response.Model, Protocol: response.Protocol, Prompt: response.Prompt,
		Source: response.Source, UpdatedAt: response.UpdatedAt,
	}
}

func embeddingResult(fileSystemID string, response apifs.AdminTenantEmbeddingConfiguration) EmbeddingResult {
	return EmbeddingResult{
		FileSystemID: fileSystemID, Enabled: response.Enabled, APIBase: response.APIBase, APIKey: response.APIKey,
		Model: response.Model, Source: response.Source, Generation: response.Generation, UpdatedAt: response.UpdatedAt,
	}
}

func extractDryRunBody(request apifs.UpdateAdminTenantExtractConfigurationRequest, keySupplied bool) map[string]any {
	body := map[string]any{"provider_api_key_supplied": keySupplied}
	addPointer(body, "enabled", request.Enabled)
	addPointer(body, "api_base", request.APIBase)
	addPointer(body, "model", request.Model)
	addPointer(body, "protocol", request.Protocol)
	addPointer(body, "prompt", request.Prompt)
	if keySupplied {
		body["api_key"] = "[REDACTED]"
	}
	return body
}

func embeddingDryRunBody(request apifs.UpdateAdminTenantEmbeddingConfigurationRequest, keySupplied bool) map[string]any {
	body := map[string]any{"enabled": request.Enabled, "provider_api_key_supplied": keySupplied}
	addPointer(body, "api_base", request.APIBase)
	addPointer(body, "model", request.Model)
	if keySupplied {
		body["api_key"] = "[REDACTED]"
	}
	return body
}

func addPointer[T any](body map[string]any, name string, value *T) {
	if value != nil {
		body[name] = *value
	}
}
