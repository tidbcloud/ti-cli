package tokenmgmt

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	pathpkg "path"
	"strings"
	"text/tabwriter"
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
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
	MaxTTL           = 365 * 24 * time.Hour
)

type Service struct {
	Resolver    endpoints.Resolver
	HTTPClient  *http.Client
	Transport   http.RoundTripper
	Timeout     time.Duration
	Debug       bool
	DebugWriter io.Writer
	HomeDir     string

	storeCredential func(string, *config.Profile, fscred.Credential, bool) (fscred.Credential, error)
	writeRecovery   func(string, string, fscred.Credential) (string, error)
	commitRecovery  func(string, string, string, string) error
}

type GenerateOptions struct {
	Profile        *config.Profile
	FileSystemID   string
	TokenName      string
	TTL            *time.Duration
	NoExpiration   bool
	StoreLocally   bool
	Replace        bool
	RegionOverride string
}

type GenerateScopedOptions struct {
	Profile        *config.Profile
	FileSystemID   string
	Token          string
	TokenExplicit  bool
	Subject        string
	TTL            *time.Duration
	Allows         []string
	StoreLocally   bool
	Replace        bool
	RegionOverride string
}

type ListOptions struct {
	Profile        *config.Profile
	FileSystemID   string
	Token          string
	TokenExplicit  bool
	IncludeExpired bool
	Offset         int
	Limit          int
	RegionOverride string
}

type MutationOptions struct {
	Profile        *config.Profile
	FileSystemID   string
	TokenID        string
	Token          string
	TokenExplicit  bool
	RegionOverride string
}

type RefreshOptions struct {
	Profile        *config.Profile
	FileSystemID   string
	Token          string
	TokenExplicit  bool
	RegionOverride string
	TTL            *time.Duration
	DryRun         bool
}

type GenerateResult struct {
	FileSystemID      string     `json:"file_system_id"`
	TokenID           string     `json:"token_id"`
	TokenName         string     `json:"token_name"`
	ScopeKind         string     `json:"scope_kind"`
	Status            string     `json:"status"`
	IssuedAt          time.Time  `json:"issued_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	FSToken           string     `json:"fs_token"`
	CredentialsStored bool       `json:"credentials_stored"`
	PreviousTokenNote string     `json:"previous_token_note,omitempty"`
}

type TokenScope struct {
	Prefix string   `json:"prefix"`
	Ops    []string `json:"ops"`
}

type GenerateScopedResult struct {
	FileSystemID      string       `json:"file_system_id"`
	TokenID           string       `json:"token_id"`
	Subject           string       `json:"subject,omitempty"`
	ScopeKind         string       `json:"scope_kind"`
	ExpiresAt         *time.Time   `json:"expires_at,omitempty"`
	Scopes            []TokenScope `json:"scopes"`
	FSToken           string       `json:"fs_token"`
	CredentialsStored bool         `json:"credentials_stored"`
	PreviousTokenNote string       `json:"previous_token_note,omitempty"`
}

type TokenMetadata struct {
	TokenID            string     `json:"token_id"`
	TokenName          string     `json:"token_name"`
	ScopeKind          string     `json:"scope_kind"`
	Status             string     `json:"status"`
	Expired            bool       `json:"expired"`
	IssuedByProvider   string     `json:"issued_by_provider,omitempty"`
	IssuedBySubjectKey string     `json:"issued_by_subject_key,omitempty"`
	IssuedAt           time.Time  `json:"issued_at"`
	ExpiresAt          *time.Time `json:"expires_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type ListResult struct {
	FileSystemID string          `json:"file_system_id"`
	Tokens       []TokenMetadata `json:"tokens"`
	NextOffset   *int            `json:"next_offset,omitempty"`
}

type MutationResult struct {
	FileSystemID            string `json:"file_system_id"`
	TokenID                 string `json:"token_id"`
	Status                  string `json:"status"`
	LocalCredentialsUpdated bool   `json:"local_credentials_updated"`
	LocalCredentialsReason  string `json:"local_credentials_reason,omitempty"`
	CacheConvergenceNote    string `json:"cache_convergence_note"`
}

type RefreshResult struct {
	FileSystemID      string     `json:"file_system_id"`
	TokenID           string     `json:"token_id"`
	ScopeKind         string     `json:"scope_kind"`
	ExpiresAt         *time.Time `json:"expires_at"`
	FSToken           string     `json:"fs_token"`
	CredentialsStored bool       `json:"credentials_stored"`
	RecoveryPath      string     `json:"recovery_path,omitempty"`
}

type PartialResultError struct {
	Code    string
	Message string
	Result  any
}

func (e *PartialResultError) Error() string         { return e.Message }
func (e *PartialResultError) StructuredResult() any { return e.Result }
func (e *PartialResultError) AppError() *apperr.Error {
	return apperr.New(e.Code, "runtime", 1, e.Message)
}

func (s Service) Generate(ctx context.Context, opts GenerateOptions) (GenerateResult, error) {
	fileSystemID, tokenName, ttlSeconds, err := validateGenerate(opts)
	if err != nil {
		return GenerateResult{}, err
	}
	if opts.StoreLocally {
		var result GenerateResult
		err := fscred.WithCredentialLock(ctx, s.homeDir(opts.Profile), profileName(opts.Profile), fileSystemID, func() error {
			var generateErr error
			result, generateErr = s.generate(ctx, opts, fileSystemID, tokenName, ttlSeconds)
			return generateErr
		})
		return result, err
	}
	return s.generate(ctx, opts, fileSystemID, tokenName, ttlSeconds)
}

func (s Service) GenerateScoped(ctx context.Context, opts GenerateScopedOptions) (GenerateScopedResult, error) {
	subject, ttlSeconds, scopes, err := validateGenerateScoped(opts)
	if err != nil {
		return GenerateScopedResult{}, err
	}
	if opts.StoreLocally {
		fileSystemID := strings.TrimSpace(opts.FileSystemID)
		if fileSystemID == "" {
			token := strings.TrimSpace(opts.Token)
			if token == "" {
				token, _, _, err = envcompat.ResolveNames(nil, "TI_FS_TOKEN", envcompat.LegacyNameFor("TI_FS_TOKEN"))
				if err != nil {
					return GenerateScopedResult{}, err
				}
			}
			if strings.TrimSpace(token) == "" {
				return GenerateScopedResult{}, apperr.New("fs.missing_file_system_id", "usage", 2, "--file-system-id is required when using a locally stored owner token")
			}
			fileSystemID, err = fscred.FileSystemIDFromToken(token)
			if err != nil {
				return GenerateScopedResult{}, err
			}
		}
		var result GenerateScopedResult
		err := fscred.WithCredentialLock(ctx, s.homeDir(opts.Profile), profileName(opts.Profile), fileSystemID, func() error {
			resolved, resolveErr := s.resolveBearer(opts.Profile, opts.FileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
			if resolveErr != nil {
				return resolveErr
			}
			var generateErr error
			result, generateErr = s.generateScoped(ctx, opts, resolved, subject, ttlSeconds, scopes)
			return generateErr
		})
		return result, err
	}
	resolved, err := s.resolveBearer(opts.Profile, opts.FileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
	if err != nil {
		return GenerateScopedResult{}, err
	}
	return s.generateScoped(ctx, opts, resolved, subject, ttlSeconds, scopes)
}

func (s Service) generateScoped(ctx context.Context, opts GenerateScopedOptions, resolved bearerInput, subject string, ttlSeconds int64, scopes []TokenScope) (GenerateScopedResult, error) {
	homeDir := s.homeDir(opts.Profile)
	if opts.StoreLocally {
		if err := fscred.PrepareCredentialTarget(homeDir, profileName(opts.Profile), resolved.fileSystemID); err != nil {
			return GenerateScopedResult{}, apperr.Wrap("fs.token_store_preflight", "config", 1, "prepare local FS token storage", err)
		}
		if _, getErr := fscred.GetCredential(homeDir, profileName(opts.Profile), resolved.fileSystemID); getErr == nil && !opts.Replace {
			return GenerateScopedResult{}, apperr.New("fs.token_local_conflict", "config", 2, fmt.Sprintf("a local token is already stored for file system %q; add --replace to select the scoped token locally", resolved.fileSystemID))
		} else if getErr != nil && apperr.CodeFor(getErr) != "fs.credential_not_found" {
			return GenerateScopedResult{}, getErr
		}
	}
	client, endpoint, err := s.bearerClient(opts.Profile, resolved, authz.FSTokenIssueScoped, "generate a scoped file system token")
	if err != nil {
		return GenerateScopedResult{}, err
	}
	requestScopes := make([]apifs.TokenScope, 0, len(scopes))
	for _, scope := range scopes {
		requestScopes = append(requestScopes, apifs.TokenScope{Prefix: scope.Prefix, Ops: append([]string(nil), scope.Ops...)})
	}
	response, err := client.IssueScopedToken(ctx, apifs.IssueScopedTokenRequest{Subject: subject, TTLSeconds: ttlSeconds, Scopes: requestScopes})
	if err != nil {
		return GenerateScopedResult{}, err
	}
	responseFileSystemID, err := fscred.FileSystemIDFromToken(response.Token)
	if err != nil || responseFileSystemID != resolved.fileSystemID {
		return GenerateScopedResult{}, apperr.New("fs.token_response_mismatch", "api", 1, "scoped token response belongs to a different file system")
	}
	result := GenerateScopedResult{FileSystemID: resolved.fileSystemID, TokenID: response.TokenID, Subject: response.Subject, ScopeKind: response.ScopeKind, ExpiresAt: response.ExpiresAt, FSToken: response.Token, Scopes: make([]TokenScope, 0, len(response.Scopes))}
	for _, scope := range response.Scopes {
		result.Scopes = append(result.Scopes, TokenScope{Prefix: scope.Prefix, Ops: append([]string(nil), scope.Ops...)})
	}
	if !opts.StoreLocally {
		return result, nil
	}
	storedScopes := make([]fscred.TokenScope, 0, len(result.Scopes))
	for _, scope := range result.Scopes {
		storedScopes = append(storedScopes, fscred.TokenScope{Prefix: scope.Prefix, Ops: append([]string(nil), scope.Ops...)})
	}
	credential := fscred.Credential{FileSystemID: resolved.fileSystemID, RegionCode: endpoint.RegionName, APIKey: response.Token, TokenID: response.TokenID, ScopeKind: response.ScopeKind, TokenName: response.Subject, ExpiresAt: response.ExpiresAt, Scopes: storedScopes}
	if _, storeErr := s.storeCredentialRecord(homeDir, opts.Profile, credential, opts.Replace); storeErr != nil {
		rollbackErr := client.DeleteTokenWithBearer(ctx, response.TokenID)
		if rollbackErr == nil {
			return GenerateScopedResult{}, apperr.Wrap("fs.token_store_failed", "runtime", 1, "store generated scoped token locally; the generated remote token was revoked", storeErr)
		}
		return result, &PartialResultError{Code: "fs.token_partial_success", Message: "the scoped token was generated but local storage and remote rollback both failed; preserve fs_token from stdout, then import or revoke it explicitly", Result: result}
	}
	result.CredentialsStored = true
	if opts.Replace {
		result.PreviousTokenNote = "the previously selected remote token remains active until explicitly disabled or deleted"
	}
	return result, nil
}

func (s Service) generate(ctx context.Context, opts GenerateOptions, fileSystemID, tokenName string, ttlSeconds *int64) (GenerateResult, error) {
	homeDir := s.homeDir(opts.Profile)
	if opts.StoreLocally {
		if err := fscred.PrepareCredentialTarget(homeDir, profileName(opts.Profile), fileSystemID); err != nil {
			return GenerateResult{}, apperr.Wrap("fs.token_store_preflight", "config", 1, "prepare local FS token storage", err)
		}
		if _, getErr := fscred.GetCredential(homeDir, profileName(opts.Profile), fileSystemID); getErr == nil && !opts.Replace {
			return GenerateResult{}, apperr.New("fs.token_local_conflict", "config", 2, fmt.Sprintf("a local token is already stored for file system %q; add --replace to select the new token locally", fileSystemID))
		} else if getErr != nil && apperr.CodeFor(getErr) != "fs.credential_not_found" {
			return GenerateResult{}, getErr
		}
	}
	client, creds, endpoint, err := s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, authz.FSTokenGenerate, "generate a file system token")
	if err != nil {
		return GenerateResult{}, err
	}
	response, err := client.GenerateToken(ctx, creds, apifs.GenerateTokenRequest{FileSystemID: fileSystemID, TokenName: tokenName, TTLSeconds: ttlSeconds})
	if err != nil {
		return GenerateResult{}, err
	}
	result := mapGenerate(response)
	if !opts.StoreLocally {
		return result, nil
	}
	credential := fscred.Credential{
		FileSystemID: fileSystemID,
		RegionCode:   endpoint.RegionName,
		APIKey:       response.Token,
		TokenID:      response.TokenID,
		ScopeKind:    response.ScopeKind,
		TokenName:    response.TokenName,
		ExpiresAt:    response.ExpiresAt,
	}
	if _, storeErr := s.storeCredentialRecord(homeDir, opts.Profile, credential, opts.Replace); storeErr != nil {
		_, rollbackErr := client.DeleteToken(ctx, creds, fileSystemID, response.TokenID)
		if rollbackErr == nil {
			return GenerateResult{}, apperr.Wrap("fs.token_store_failed", "runtime", 1, "store generated token locally; the generated remote token was revoked", storeErr)
		}
		result.CredentialsStored = false
		return result, &PartialResultError{
			Code:    "fs.token_partial_success",
			Message: "the token was generated but local storage and remote rollback both failed; preserve fs_token from stdout, then import or revoke it explicitly",
			Result:  result,
		}
	}
	result.CredentialsStored = true
	if opts.Replace {
		result.PreviousTokenNote = "the previously selected remote token remains active until explicitly disabled or deleted"
	}
	return result, nil
}

func (s Service) List(ctx context.Context, opts ListOptions) (ListResult, error) {
	fileSystemID, err := fscred.ValidateFileSystemID(opts.FileSystemID)
	if err != nil {
		return ListResult{}, err
	}
	if opts.Offset < 0 {
		return ListResult{}, apperr.New("fs.invalid_token_offset", "usage", 2, "--offset must be non-negative")
	}
	if opts.Limit <= 0 || opts.Limit > MaxListLimit {
		return ListResult{}, apperr.New("fs.invalid_token_limit", "usage", 2, fmt.Sprintf("--limit must be between 1 and %d", MaxListLimit))
	}
	apiOpts := apifs.ListTokensOptions{FileSystemID: fileSystemID, IncludeExpired: opts.IncludeExpired, Offset: opts.Offset, Limit: opts.Limit}
	var response apifs.ListTokensResponse
	useBearer, err := tokenInputPresent(opts.Token, opts.TokenExplicit)
	if err != nil {
		return ListResult{}, err
	}
	if useBearer {
		resolved, resolveErr := s.resolveBearer(opts.Profile, fileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
		if resolveErr != nil {
			return ListResult{}, resolveErr
		}
		client, _, clientErr := s.bearerClient(opts.Profile, resolved, authz.FSTokenList, "list file system tokens")
		if clientErr != nil {
			return ListResult{}, clientErr
		}
		response, err = client.ListTokensWithBearer(ctx, apiOpts)
	} else {
		client, creds, _, clientErr := s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, authz.FSTokenList, "list file system tokens")
		if clientErr != nil {
			return ListResult{}, clientErr
		}
		response, err = client.ListTokens(ctx, creds, apiOpts)
	}
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{FileSystemID: fileSystemID, Tokens: make([]TokenMetadata, 0, len(response.Tokens)), NextOffset: response.NextOffset}
	for _, item := range response.Tokens {
		if item.FileSystemID != "" && item.FileSystemID != fileSystemID {
			return ListResult{}, apperr.New("fs.token_response_mismatch", "api", 1, "token list response contained a different file system ID")
		}
		result.Tokens = append(result.Tokens, TokenMetadata{
			TokenID: item.TokenID, TokenName: item.TokenName, ScopeKind: item.ScopeKind, Status: item.Status, Expired: item.Expired,
			IssuedByProvider: item.IssuedByProvider, IssuedBySubjectKey: item.IssuedBySubjectKey,
			IssuedAt: item.IssuedAt, ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		})
	}
	return result, nil
}

func (s Service) Enable(ctx context.Context, opts MutationOptions) (MutationResult, error) {
	return s.setEnabled(ctx, opts, true)
}

func (s Service) Disable(ctx context.Context, opts MutationOptions) (MutationResult, error) {
	return s.setEnabled(ctx, opts, false)
}

func (s Service) setEnabled(ctx context.Context, opts MutationOptions, enabled bool) (MutationResult, error) {
	fileSystemID, tokenID, err := validateMutation(opts)
	if err != nil {
		return MutationResult{}, err
	}
	if !enabled {
		if err := s.guardTokenIDMount(fileSystemID, tokenID); err != nil {
			return MutationResult{}, err
		}
	}
	permission, action := authz.FSTokenDisable, "disable a file system token"
	if enabled {
		permission, action = authz.FSTokenEnable, "enable a file system token"
	}
	var response apifs.TokenMutationResponse
	useBearer, err := tokenInputPresent(opts.Token, opts.TokenExplicit)
	if err != nil {
		return MutationResult{}, err
	}
	if useBearer {
		resolved, resolveErr := s.resolveBearer(opts.Profile, fileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
		if resolveErr != nil {
			return MutationResult{}, resolveErr
		}
		client, _, clientErr := s.bearerClient(opts.Profile, resolved, permission, action)
		if clientErr != nil {
			return MutationResult{}, clientErr
		}
		response, err = client.SetTokenEnabledWithBearer(ctx, fileSystemID, tokenID, enabled)
	} else {
		client, creds, _, clientErr := s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, permission, action)
		if clientErr != nil {
			return MutationResult{}, clientErr
		}
		response, err = client.SetTokenEnabled(ctx, creds, fileSystemID, tokenID, enabled)
	}
	if err != nil {
		return MutationResult{}, err
	}
	return MutationResult{FileSystemID: response.FileSystemID, TokenID: response.TokenID, Status: response.Status, CacheConvergenceNote: "authentication changes can take approximately 10 seconds to converge"}, nil
}

func (s Service) Delete(ctx context.Context, opts MutationOptions) (MutationResult, error) {
	fileSystemID, tokenID, err := validateMutation(opts)
	if err != nil {
		return MutationResult{}, err
	}
	if err := s.guardTokenIDMount(fileSystemID, tokenID); err != nil {
		return MutationResult{}, err
	}
	var response apifs.TokenMutationResponse
	useBearer, err := tokenInputPresent(opts.Token, opts.TokenExplicit)
	if err != nil {
		return MutationResult{}, err
	}
	if useBearer {
		resolved, resolveErr := s.resolveBearer(opts.Profile, fileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
		if resolveErr != nil {
			return MutationResult{}, resolveErr
		}
		client, _, clientErr := s.bearerClient(opts.Profile, resolved, authz.FSTokenDelete, "delete a file system token")
		if clientErr != nil {
			return MutationResult{}, clientErr
		}
		err = client.DeleteTokenWithBearer(ctx, tokenID)
		response = apifs.TokenMutationResponse{FileSystemID: fileSystemID, TokenID: tokenID, Status: "revoked"}
	} else {
		client, creds, _, clientErr := s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, authz.FSTokenDelete, "delete a file system token")
		if clientErr != nil {
			return MutationResult{}, clientErr
		}
		response, err = client.DeleteToken(ctx, creds, fileSystemID, tokenID)
	}
	if err != nil {
		return MutationResult{}, err
	}
	var updated bool
	var reason string
	cleanupErr := fscred.WithCredentialLock(ctx, s.homeDir(opts.Profile), profileName(opts.Profile), fileSystemID, func() error {
		var err error
		updated, reason, err = fscred.DeleteCredentialIfTokenID(s.homeDir(opts.Profile), profileName(opts.Profile), fileSystemID, tokenID)
		return err
	})
	if cleanupErr != nil {
		return MutationResult{}, apperr.Wrap("fs.token_local_cleanup", "runtime", 1, "remote token was revoked but local credential cleanup failed", cleanupErr)
	}
	return MutationResult{FileSystemID: response.FileSystemID, TokenID: response.TokenID, Status: response.Status, LocalCredentialsUpdated: updated, LocalCredentialsReason: reason, CacheConvergenceNote: "authentication changes can take approximately 10 seconds to converge"}, nil
}

func (s Service) Refresh(ctx context.Context, opts RefreshOptions) (RefreshResult, error) {
	resolved, err := s.resolveRefresh(opts)
	if err != nil {
		return RefreshResult{}, err
	}
	if err := s.guardTokenFingerprintMount(resolved.fileSystemID, tokenFingerprint(resolved.token)); err != nil {
		return RefreshResult{}, err
	}
	if !resolved.local {
		return s.refreshRemote(ctx, opts.Profile, resolved, opts.TTL)
	}
	var result RefreshResult
	err = fscred.WithCredentialLock(ctx, s.homeDir(opts.Profile), profileName(opts.Profile), resolved.fileSystemID, func() error {
		current, err := fscred.GetCredential(s.homeDir(opts.Profile), profileName(opts.Profile), resolved.fileSystemID)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(current.APIKey), []byte(resolved.token)) != 1 {
			return apperr.New("fs.token_local_changed", "runtime", 1, "the selected local token changed before refresh; retry with the current credential")
		}
		if err := fscred.PrepareCredentialTarget(s.homeDir(opts.Profile), profileName(opts.Profile), resolved.fileSystemID); err != nil {
			return apperr.Wrap("fs.token_store_preflight", "config", 1, "prepare local FS token recovery storage", err)
		}
		if err := s.guardTokenFingerprintMount(resolved.fileSystemID, tokenFingerprint(current.APIKey)); err != nil {
			return err
		}
		result, err = s.refreshRemote(ctx, opts.Profile, resolved, opts.TTL)
		if err != nil {
			return err
		}
		credential := current
		credential.APIKey = result.FSToken
		credential.TokenID = result.TokenID
		credential.ScopeKind = result.ScopeKind
		credential.ExpiresAt = result.ExpiresAt
		recoveryPath, writeErr := s.writeRecoveryCredential(s.homeDir(opts.Profile), profileName(opts.Profile), credential)
		if writeErr != nil {
			result.CredentialsStored = false
			return &PartialResultError{Code: "fs.token_partial_success", Message: "the token was refreshed but the new credential could not be written; preserve fs_token from stdout and import it explicitly", Result: result}
		}
		result.RecoveryPath = recoveryPath
		if commitErr := s.commitRecoveryCredential(s.homeDir(opts.Profile), profileName(opts.Profile), resolved.fileSystemID, recoveryPath); commitErr != nil {
			result.CredentialsStored = false
			return &PartialResultError{Code: "fs.token_partial_success", Message: fmt.Sprintf("the token was refreshed but final credential replacement failed; recovery state remains at %s", recoveryPath), Result: result}
		}
		result.CredentialsStored = true
		result.RecoveryPath = ""
		return nil
	})
	return result, err
}

func (s Service) refreshRemote(ctx context.Context, profile *config.Profile, resolved bearerInput, ttl *time.Duration) (RefreshResult, error) {
	ttlSeconds, err := optionalTTLSeconds(ttl)
	if err != nil {
		return RefreshResult{}, err
	}
	endpoint, err := s.resolveEndpoint(profile, resolved.regionCode)
	if err != nil {
		return RefreshResult{}, err
	}
	raw, err := api.NewBearerClient(profileName(profile), resolved.token, endpoint, authz.FSTokenRefresh, api.Options{
		Action: "refresh a file system token", HTTPClient: s.HTTPClient, Transport: s.Transport, Timeout: s.Timeout,
		Debug: s.Debug, DebugWriter: s.DebugWriter, UserAgent: "ti fs token management", MaxRetries: -1,
	})
	if err != nil {
		return RefreshResult{}, err
	}
	response, err := apifs.New(raw).RefreshToken(ctx, apifs.RefreshTokenRequest{TTLSeconds: ttlSeconds})
	if err != nil {
		if apperr.CodeFor(err) == "api.network_error" {
			return RefreshResult{}, apperr.Wrap("fs.token_refresh_ambiguous", "api", 1, "token refresh may have committed but its response was lost; do not retry with the old token, generate another owner token or inspect local recovery state", err)
		}
		return RefreshResult{}, err
	}
	if response.FileSystemID != resolved.fileSystemID {
		return RefreshResult{}, apperr.New("fs.token_response_mismatch", "api", 1, "refresh response returned a different file system ID")
	}
	return RefreshResult{FileSystemID: response.FileSystemID, TokenID: response.TokenID, ScopeKind: response.ScopeKind, ExpiresAt: response.ExpiresAt, FSToken: response.Token}, nil
}

type bearerInput struct {
	fileSystemID string
	token        string
	regionCode   string
	local        bool
	scopeKind    string
}

func (s Service) resolveRefresh(opts RefreshOptions) (bearerInput, error) {
	return s.resolveBearer(opts.Profile, opts.FileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, false)
}

func (s Service) resolveBearer(profile *config.Profile, requestedFileSystemID, requestedToken string, tokenExplicit bool, regionOverride string, ownerRequired bool) (bearerInput, error) {
	optsProfile := profile
	if profile == nil {
		return bearerInput{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	token := strings.TrimSpace(requestedToken)
	sourceLocal := false
	if tokenExplicit && token == "" {
		return bearerInput{}, apperr.New("fs.empty_token", "usage", 2, "--fs-token cannot be empty")
	}
	if token == "" {
		envToken, _, _, err := envcompat.ResolveNames(nil, "TI_FS_TOKEN", envcompat.LegacyNameFor("TI_FS_TOKEN"))
		if err != nil {
			return bearerInput{}, err
		}
		token = strings.TrimSpace(envToken)
	}
	fileSystemID := strings.TrimSpace(requestedFileSystemID)
	var localCredential fscred.Credential
	if token == "" {
		if fileSystemID == "" {
			return bearerInput{}, apperr.New("fs.missing_file_system_id", "usage", 2, "--file-system-id is required when using a locally stored FS token")
		}
		credential, err := fscred.GetCredential(s.homeDir(profile), profileName(profile), fileSystemID)
		if err != nil {
			return bearerInput{}, err
		}
		localCredential = credential
		token = credential.APIKey
		sourceLocal = true
	}
	tokenFileSystemID, err := fscred.FileSystemIDFromToken(token)
	if err != nil {
		return bearerInput{}, err
	}
	if fileSystemID == "" {
		fileSystemID = tokenFileSystemID
	} else if fileSystemID != tokenFileSystemID {
		return bearerInput{}, apperr.New("fs.token_file_system_mismatch", "authentication", 3, fmt.Sprintf("FS token belongs to file system %q, not %q", tokenFileSystemID, fileSystemID))
	}
	if ownerRequired && sourceLocal && localCredential.ScopeKind == "fs_scoped" {
		return bearerInput{}, apperr.New("fs.owner_token_required", "authorization", 4, "the selected local token is scoped and cannot generate another scoped token; pass an owner token with --fs-token or TI_FS_TOKEN")
	}
	regionCode := strings.TrimSpace(regionOverride)
	if sourceLocal {
		if regionCode == "" {
			regionCode = localCredential.RegionCode
		}
	}
	if regionCode == "" {
		regionCode = optsProfile.PlacementRegionCode
	}
	return bearerInput{fileSystemID: fileSystemID, token: token, regionCode: regionCode, local: sourceLocal, scopeKind: localCredential.ScopeKind}, nil
}

func tokenInputPresent(token string, explicit bool) (bool, error) {
	if explicit || strings.TrimSpace(token) != "" {
		return true, nil
	}
	envToken, _, _, err := envcompat.ResolveNames(nil, "TI_FS_TOKEN", envcompat.LegacyNameFor("TI_FS_TOKEN"))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(envToken) != "", nil
}

func (s Service) bearerClient(profile *config.Profile, resolved bearerInput, permission authz.Permission, action string) (*apifs.Client, endpoints.Endpoint, error) {
	endpoint, err := s.resolveEndpoint(profile, resolved.regionCode)
	if err != nil {
		return nil, endpoints.Endpoint{}, err
	}
	raw, err := api.NewBearerClient(profileName(profile), resolved.token, endpoint, permission, api.Options{
		Action: action, HTTPClient: s.HTTPClient, Transport: s.Transport, Timeout: s.Timeout,
		Debug: s.Debug, DebugWriter: s.DebugWriter, UserAgent: "ti fs token management", MaxRetries: -1,
	})
	if err != nil {
		return nil, endpoints.Endpoint{}, err
	}
	return apifs.New(raw), endpoint, nil
}

func (s Service) DryRunGenerate(commandPath string, opts GenerateOptions) (dryrun.Result, error) {
	fileSystemID, _, _, err := validateGenerate(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	_, _, endpoint, err := s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, authz.FSTokenGenerate, "generate a file system token")
	if err != nil {
		return dryrun.Result{}, err
	}
	if opts.StoreLocally {
		if _, getErr := fscred.GetCredential(s.homeDir(opts.Profile), profileName(opts.Profile), fileSystemID); getErr == nil && !opts.Replace {
			return dryrun.Result{}, apperr.New("fs.token_local_conflict", "config", 2, "a local token is already stored; add --replace")
		} else if getErr != nil && apperr.CodeFor(getErr) != "fs.credential_not_found" {
			return dryrun.Result{}, getErr
		}
	}
	return tokenDryRun(commandPath, "generate_file_system_token", http.MethodPost, "/v1/tokens/generate", fileSystemID, opts.Profile, endpoint, authz.FSTokenGenerate), nil
}

func (s Service) DryRunGenerateScoped(commandPath string, opts GenerateScopedOptions) (dryrun.Result, error) {
	_, _, _, err := validateGenerateScoped(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	resolved, err := s.resolveBearer(opts.Profile, opts.FileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
	if err != nil {
		return dryrun.Result{}, err
	}
	if opts.StoreLocally {
		if _, getErr := fscred.GetCredential(s.homeDir(opts.Profile), profileName(opts.Profile), resolved.fileSystemID); getErr == nil && !opts.Replace {
			return dryrun.Result{}, apperr.New("fs.token_local_conflict", "config", 2, "a local token is already stored; add --replace")
		} else if getErr != nil && apperr.CodeFor(getErr) != "fs.credential_not_found" {
			return dryrun.Result{}, getErr
		}
	}
	endpoint, err := s.resolveEndpoint(opts.Profile, resolved.regionCode)
	if err != nil {
		return dryrun.Result{}, err
	}
	return tokenDryRun(commandPath, "generate_file_system_scoped_token", http.MethodPost, "/v1/tokens", resolved.fileSystemID, opts.Profile, endpoint, authz.FSTokenIssueScoped), nil
}

func (s Service) DryRunMutation(commandPath, operation, method, path string, opts MutationOptions, permission authz.Permission, mountGuard bool) (dryrun.Result, error) {
	fileSystemID, tokenID, err := validateMutation(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	if mountGuard {
		if err := s.guardTokenIDMount(fileSystemID, tokenID); err != nil {
			return dryrun.Result{}, err
		}
	}
	var endpoint endpoints.Endpoint
	useBearer, err := tokenInputPresent(opts.Token, opts.TokenExplicit)
	if err != nil {
		return dryrun.Result{}, err
	}
	if useBearer {
		resolved, resolveErr := s.resolveBearer(opts.Profile, fileSystemID, opts.Token, opts.TokenExplicit, opts.RegionOverride, true)
		if resolveErr != nil {
			return dryrun.Result{}, resolveErr
		}
		endpoint, err = s.resolveEndpoint(opts.Profile, resolved.regionCode)
	} else {
		_, _, endpoint, err = s.controlClient(opts.Profile, fileSystemID, opts.RegionOverride, permission, operation)
	}
	if err != nil {
		return dryrun.Result{}, err
	}
	return tokenDryRun(commandPath, operation, method, path, fileSystemID, opts.Profile, endpoint, permission), nil
}

func (s Service) DryRunRefresh(commandPath string, opts RefreshOptions) (dryrun.Result, error) {
	resolved, err := s.resolveRefresh(opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	if _, err := optionalTTLSeconds(opts.TTL); err != nil {
		return dryrun.Result{}, err
	}
	if err := s.guardTokenFingerprintMount(resolved.fileSystemID, tokenFingerprint(resolved.token)); err != nil {
		return dryrun.Result{}, err
	}
	endpoint, err := s.resolveEndpoint(opts.Profile, resolved.regionCode)
	if err != nil {
		return dryrun.Result{}, err
	}
	return tokenDryRun(commandPath, "refresh_file_system_token", http.MethodPost, "/v1/tokens/refresh", resolved.fileSystemID, opts.Profile, endpoint, authz.FSTokenRefresh), nil
}

func tokenDryRun(commandPath, operation, method, path, fileSystemID string, profile *config.Profile, endpoint endpoints.Endpoint, permission authz.Permission) dryrun.Result {
	return dryrun.New(commandPath, operation, dryrun.RequestSummary{Method: method, Path: path, Description: "credentials and token plaintext are redacted"},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(profile))},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: endpoint.BaseURL},
		dryrun.Check{Name: "file_system_id", Status: "passed", Message: fileSystemID},
		dryrun.Check{Name: "permission_requirement", Status: "passed", Message: string(permission)})
}

func (s Service) controlClient(profile *config.Profile, fileSystemID, regionOverride string, permission authz.Permission, action string) (*apifs.Client, apifs.TiDBCloudCredentials, endpoints.Endpoint, error) {
	creds, err := auth.ValidateProfile(profile)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	placementCode := strings.TrimSpace(regionOverride)
	if placementCode == "" && strings.TrimSpace(fileSystemID) != "" {
		if credential, getErr := fscred.GetCredential(s.homeDir(profile), profileName(profile), fileSystemID); getErr == nil {
			placementCode = credential.RegionCode
		} else if apperr.CodeFor(getErr) != "fs.credential_not_found" {
			return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, getErr
		}
	}
	endpoint, err := s.resolveEndpoint(profile, placementCode)
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	raw, err := api.New(api.Options{
		Endpoint: endpoint, ProfileName: creds.ProfileName, Permission: permission, Action: action,
		HTTPClient: s.HTTPClient, Transport: s.Transport, Timeout: s.Timeout, Debug: s.Debug, DebugWriter: s.DebugWriter,
		Redactor: apiRedactor(creds.PublicKey, creds.PrivateKey), UserAgent: "ti fs token management",
	})
	if err != nil {
		return nil, apifs.TiDBCloudCredentials{}, endpoints.Endpoint{}, err
	}
	return apifs.New(raw), apifs.TiDBCloudCredentials{PublicKey: creds.PublicKey, PrivateKey: creds.PrivateKey}, endpoint, nil
}

func apiRedactor(secrets ...string) apitransport.Redactor {
	return apitransport.Redactor{Secrets: secrets}
}

func (s Service) resolveEndpoint(profile *config.Profile, override string) (endpoints.Endpoint, error) {
	placementCode := strings.TrimSpace(override)
	if placementCode == "" && profile != nil {
		placementCode = profile.PlacementRegionCode
	}
	if placementCode == "" {
		return endpoints.Endpoint{}, apperr.New("fs.missing_region", "config", 2, "ti fs region is required; pass --region, set TI_REGION_CODE, or configure a profile region")
	}
	placement, err := region.ParsePlacementCode(placementCode)
	if err != nil {
		return endpoints.Endpoint{}, apperr.Wrap("config.invalid_region", "config", 2, err.Error(), err)
	}
	resolver := s.Resolver
	if resolver.IsZero() {
		resolver = endpoints.NewResolver()
	}
	return resolver.ResolveFS(placement.Provider, placement.NativeCode)
}

func (s Service) guardTokenIDMount(fileSystemID, tokenID string) error {
	locators, err := mountlocator.List(s.homeDir(nil))
	if err != nil {
		return apperr.Wrap("fs.mount_inventory", "runtime", 1, "inspect local mount state", err)
	}
	for _, locator := range locators {
		locatorFSID := locator.FileSystemID
		if locatorFSID == "" {
			locatorFSID = locator.FileSystemName
		}
		if locatorFSID == fileSystemID && locator.TokenID != "" && locator.TokenID == tokenID {
			return activeMountError(locator.MountPath)
		}
	}
	return nil
}

func (s Service) guardTokenFingerprintMount(fileSystemID, fingerprint string) error {
	locators, err := mountlocator.List(s.homeDir(nil))
	if err != nil {
		return apperr.Wrap("fs.mount_inventory", "runtime", 1, "inspect local mount state", err)
	}
	for _, locator := range locators {
		locatorFSID := locator.FileSystemID
		if locatorFSID == "" {
			locatorFSID = locator.FileSystemName
		}
		if locatorFSID == fileSystemID && locator.TokenFingerprint != "" && locator.TokenFingerprint == fingerprint {
			return activeMountError(locator.MountPath)
		}
	}
	return nil
}

func activeMountError(path string) error {
	return apperr.New("fs.token_mount_active", "runtime", 1, fmt.Sprintf("this token is used by the local mount at %s; run `ti fs drain-file-system --mount-path %s` and `ti fs unmount-file-system --mount-path %s` before changing it", path, path, path))
}

func validateGenerate(opts GenerateOptions) (string, string, *int64, error) {
	fileSystemID, err := fscred.ValidateFileSystemID(opts.FileSystemID)
	if err != nil {
		return "", "", nil, err
	}
	tokenName := strings.TrimSpace(opts.TokenName)
	if tokenName == "" {
		return "", "", nil, apperr.New("fs.token_name_required", "usage", 2, "--token-name is required")
	}
	if len(tokenName) > 64 {
		return "", "", nil, apperr.New("fs.invalid_token_name", "usage", 2, "--token-name must be at most 64 bytes")
	}
	if (opts.TTL != nil) == opts.NoExpiration {
		return "", "", nil, apperr.New("fs.token_lifetime_required", "usage", 2, "provide exactly one of --ttl or --no-expiration")
	}
	if opts.Replace && !opts.StoreLocally {
		return "", "", nil, apperr.New("fs.token_replace_without_store", "usage", 2, "--replace requires --store-locally")
	}
	ttlSeconds, err := optionalTTLSeconds(opts.TTL)
	if err != nil {
		return "", "", nil, err
	}
	return fileSystemID, tokenName, ttlSeconds, nil
}

func validateGenerateScoped(opts GenerateScopedOptions) (string, int64, []TokenScope, error) {
	subject := strings.TrimSpace(opts.Subject)
	if len(subject) > 64 {
		return "", 0, nil, apperr.New("fs.invalid_token_subject", "usage", 2, "--subject must be at most 64 bytes")
	}
	if opts.TTL == nil {
		return "", 0, nil, apperr.New("fs.token_ttl_required", "usage", 2, "--ttl is required for a scoped token")
	}
	ttlSeconds, err := scopedTTLSeconds(opts.TTL)
	if err != nil {
		return "", 0, nil, err
	}
	if len(opts.Allows) == 0 {
		return "", 0, nil, apperr.New("fs.token_scope_required", "usage", 2, "at least one --allow <prefix>:<ops> is required")
	}
	if opts.Replace && !opts.StoreLocally {
		return "", 0, nil, apperr.New("fs.token_replace_without_store", "usage", 2, "--replace requires --store-locally")
	}
	scopes := make([]TokenScope, 0, len(opts.Allows))
	seenPrefixes := make(map[string]struct{}, len(opts.Allows))
	for _, raw := range opts.Allows {
		scope, parseErr := parseAllow(raw)
		if parseErr != nil {
			return "", 0, nil, parseErr
		}
		if _, exists := seenPrefixes[scope.Prefix]; exists {
			return "", 0, nil, apperr.New("fs.duplicate_token_scope", "usage", 2, fmt.Sprintf("duplicate --allow prefix %q", scope.Prefix))
		}
		seenPrefixes[scope.Prefix] = struct{}{}
		scopes = append(scopes, scope)
	}
	return subject, *ttlSeconds, scopes, nil
}

func parseAllow(raw string) (TokenScope, error) {
	idx := strings.LastIndex(raw, ":")
	if idx <= 0 || idx == len(raw)-1 {
		return TokenScope{}, apperr.New("fs.invalid_token_scope", "usage", 2, fmt.Sprintf("invalid --allow %q: expected <prefix>:<ops>", raw))
	}
	prefix, err := canonicalScopePrefix(raw[:idx])
	if err != nil {
		return TokenScope{}, err
	}
	ops, err := parseScopeOps(raw[idx+1:])
	if err != nil {
		return TokenScope{}, err
	}
	return TokenScope{Prefix: prefix, Ops: ops}, nil
}

func canonicalScopePrefix(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == ":" {
		return "", apperr.New("fs.invalid_token_scope", "usage", 2, "scope prefix is required")
	}
	raw = strings.TrimPrefix(raw, ":")
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == 0 || (b < 0x20 && b != '\n' && b != '\t' && b != '\r') {
			return "", apperr.New("fs.invalid_token_scope", "usage", 2, "scope prefix contains an unsupported control character")
		}
	}
	if strings.ContainsRune(raw, '\\') || !utf8.ValidString(raw) {
		return "", apperr.New("fs.invalid_token_scope", "usage", 2, "scope prefix must be valid UTF-8 and cannot contain backslashes")
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	for _, segment := range strings.Split(strings.Trim(raw, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", apperr.New("fs.invalid_token_scope", "usage", 2, "scope prefix cannot contain . or .. path segments")
		}
	}
	prefix := pathpkg.Clean(raw)
	if prefix == "." {
		prefix = "/"
	}
	if prefix == "/" {
		return prefix, nil
	}
	return strings.TrimSuffix(prefix, "/"), nil
}

func parseScopeOps(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		op := strings.TrimSpace(item)
		switch op {
		case "read", "list", "search", "write", "delete":
			seen[op] = struct{}{}
		case "":
			return nil, apperr.New("fs.invalid_token_scope", "usage", 2, "scope operations cannot contain an empty value")
		default:
			return nil, apperr.New("fs.invalid_token_scope", "usage", 2, fmt.Sprintf("unknown scope operation %q; use read, list, search, write, or delete", op))
		}
	}
	if _, search := seen["search"]; search {
		if _, read := seen["read"]; !read {
			return nil, apperr.New("fs.invalid_token_scope", "usage", 2, "the search operation requires read")
		}
	}
	ops := make([]string, 0, len(seen))
	for _, op := range []string{"read", "list", "search", "write", "delete"} {
		if _, ok := seen[op]; ok {
			ops = append(ops, op)
		}
	}
	return ops, nil
}

func validateMutation(opts MutationOptions) (string, string, error) {
	fileSystemID, err := fscred.ValidateFileSystemID(opts.FileSystemID)
	if err != nil {
		return "", "", err
	}
	tokenID := strings.TrimSpace(opts.TokenID)
	if tokenID == "" {
		return "", "", apperr.New("fs.token_id_required", "usage", 2, "--token-id is required")
	}
	if strings.ContainsAny(tokenID, "/\\") {
		return "", "", apperr.New("fs.invalid_token_id", "usage", 2, "--token-id must be one path-safe identifier")
	}
	return fileSystemID, tokenID, nil
}

func optionalTTLSeconds(ttl *time.Duration) (*int64, error) {
	if ttl == nil {
		return nil, nil
	}
	if *ttl <= 0 {
		return nil, apperr.New("fs.token_ttl_invalid", "usage", 2, "--ttl must be positive")
	}
	if *ttl > MaxTTL {
		return nil, apperr.New("fs.token_ttl_invalid", "usage", 2, "--ttl must not exceed 365 days")
	}
	if *ttl%time.Second != 0 {
		return nil, apperr.New("fs.token_ttl_invalid", "usage", 2, "--ttl must resolve to whole seconds")
	}
	seconds := int64(*ttl / time.Second)
	return &seconds, nil
}

func scopedTTLSeconds(ttl *time.Duration) (*int64, error) {
	if ttl == nil {
		return nil, apperr.New("fs.token_ttl_required", "usage", 2, "--ttl is required for a scoped token")
	}
	if *ttl <= 0 {
		return nil, apperr.New("fs.token_ttl_invalid", "usage", 2, "--ttl must be positive")
	}
	if *ttl%time.Second != 0 {
		return nil, apperr.New("fs.token_ttl_invalid", "usage", 2, "--ttl must resolve to whole seconds")
	}
	seconds := int64(*ttl / time.Second)
	return &seconds, nil
}

func mapGenerate(response apifs.GenerateTokenResponse) GenerateResult {
	return GenerateResult{FileSystemID: response.FileSystemID, TokenID: response.TokenID, TokenName: response.TokenName, ScopeKind: response.ScopeKind, Status: response.Status, IssuedAt: response.IssuedAt, ExpiresAt: response.ExpiresAt, FSToken: response.Token}
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

func (s Service) homeDir(profile *config.Profile) string {
	if s.HomeDir != "" {
		return s.HomeDir
	}
	if profile != nil && profile.HomeDir != "" {
		return profile.HomeDir
	}
	return ""
}

func (s Service) storeCredentialRecord(homeDir string, profile *config.Profile, credential fscred.Credential, replace bool) (fscred.Credential, error) {
	if s.storeCredential != nil {
		return s.storeCredential(homeDir, profile, credential, replace)
	}
	return fscred.StoreCredentialRecord(homeDir, profile, credential, replace)
}

func (s Service) writeRecoveryCredential(homeDir, profileName string, credential fscred.Credential) (string, error) {
	if s.writeRecovery != nil {
		return s.writeRecovery(homeDir, profileName, credential)
	}
	return fscred.WriteRecoveryCredential(homeDir, profileName, credential)
}

func (s Service) commitRecoveryCredential(homeDir, profileName, fileSystemID, recoveryPath string) error {
	if s.commitRecovery != nil {
		return s.commitRecovery(homeDir, profileName, fileSystemID, recoveryPath)
	}
	return fscred.CommitRecoveryCredential(homeDir, profileName, fileSystemID, recoveryPath)
}

func profileName(profile *config.Profile) string {
	if profile == nil || profile.Name == "" {
		return config.DefaultProfile
	}
	return profile.Name
}

func (r ListResult) Human() string {
	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "TOKEN_ID\tNAME\tSCOPE\tSTATUS\tEXPIRES_AT")
	for _, token := range r.Tokens {
		status := token.Status
		if token.Expired {
			status = "expired"
		}
		expiresAt := "never"
		if token.ExpiresAt != nil {
			expiresAt = token.ExpiresAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", token.TokenID, token.TokenName, token.ScopeKind, status, expiresAt)
	}
	_ = w.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r GenerateResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Token ID: " + r.TokenID,
		"Token name: " + r.TokenName,
		"Scope: " + r.ScopeKind,
		"Status: " + r.Status,
		"Expires: " + formatTokenExpiry(r.ExpiresAt),
		fmt.Sprintf("Credentials stored: %t", r.CredentialsStored),
		"FS token: " + r.FSToken,
	}
	if r.PreviousTokenNote != "" {
		lines = append(lines, "Note: "+r.PreviousTokenNote)
	}
	return strings.Join(lines, "\n")
}

func (r GenerateScopedResult) Human() string {
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "File system ID: %s\nToken ID: %s\nScope: %s\nExpires: %s\nCredentials stored: %t\nFS token: %s\n", r.FileSystemID, r.TokenID, r.ScopeKind, formatTokenExpiry(r.ExpiresAt), r.CredentialsStored, r.FSToken)
	if r.Subject != "" {
		_, _ = fmt.Fprintf(&out, "Subject: %s\n", r.Subject)
	}
	if r.PreviousTokenNote != "" {
		_, _ = fmt.Fprintf(&out, "Note: %s\n", r.PreviousTokenNote)
	}
	if len(r.Scopes) > 0 {
		writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "PREFIX\tOPERATIONS")
		for _, scope := range r.Scopes {
			_, _ = fmt.Fprintf(writer, "%s\t%s\n", scope.Prefix, strings.Join(scope.Ops, ","))
		}
		_ = writer.Flush()
	}
	return strings.TrimRight(out.String(), "\n")
}

func (r MutationResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Token ID: " + r.TokenID,
		"Status: " + r.Status,
		fmt.Sprintf("Local credentials updated: %t", r.LocalCredentialsUpdated),
	}
	if r.LocalCredentialsReason != "" {
		lines = append(lines, "Local credentials: "+r.LocalCredentialsReason)
	}
	if r.CacheConvergenceNote != "" {
		lines = append(lines, "Note: "+r.CacheConvergenceNote)
	}
	return strings.Join(lines, "\n")
}

func (r RefreshResult) Human() string {
	lines := []string{
		"File system ID: " + r.FileSystemID,
		"Token ID: " + r.TokenID,
		"Scope: " + r.ScopeKind,
		"Expires: " + formatTokenExpiry(r.ExpiresAt),
		fmt.Sprintf("Credentials stored: %t", r.CredentialsStored),
		"FS token: " + r.FSToken,
	}
	if r.RecoveryPath != "" {
		lines = append(lines, "Recovery path: "+r.RecoveryPath)
	}
	return strings.Join(lines, "\n")
}

func formatTokenExpiry(expiresAt *time.Time) string {
	if expiresAt == nil {
		return "never"
	}
	return expiresAt.UTC().Format(time.RFC3339)
}
