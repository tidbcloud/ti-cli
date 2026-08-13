package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	tidbCloudPublicKeyHeader  = "X-TiDBCloud-Public-Key"
	tidbCloudPrivateKeyHeader = "X-TiDBCloud-Private-Key"
)

type TiDBCloudCredentials struct {
	PublicKey  string
	PrivateKey string
}

type GenerateTokenRequest struct {
	FileSystemID string
	TokenName    string
	TTLSeconds   *int64
}

type GenerateTokenResponse struct {
	Token        string     `json:"token"`
	TokenID      string     `json:"token_id"`
	FileSystemID string     `json:"tenant_id"`
	TokenName    string     `json:"key_name"`
	ScopeKind    string     `json:"scope_kind"`
	Status       string     `json:"status"`
	IssuedAt     time.Time  `json:"issued_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

type TokenMetadata struct {
	TokenID              string          `json:"token_id"`
	FileSystemID         string          `json:"tenant_id"`
	TokenName            string          `json:"key_name"`
	ScopeKind            string          `json:"scope_kind"`
	Status               string          `json:"status"`
	Expired              bool            `json:"expired"`
	IssuedByProvider     string          `json:"issued_by_provider,omitempty"`
	IssuedBySubjectKey   string          `json:"issued_by_subject_key,omitempty"`
	IssuedByMetadataJSON json.RawMessage `json:"issued_by_metadata_json,omitempty"`
	IssuedAt             time.Time       `json:"issued_at"`
	ExpiresAt            *time.Time      `json:"expires_at"`
	RevokedAt            *time.Time      `json:"revoked_at"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type ListTokensOptions struct {
	FileSystemID   string
	IncludeExpired bool
	Offset         int
	Limit          int
}

type ListTokensResponse struct {
	Tokens     []TokenMetadata `json:"tokens"`
	NextOffset *int            `json:"next_offset,omitempty"`
}

type TokenMutationResponse struct {
	TokenID      string `json:"token_id"`
	FileSystemID string `json:"tenant_id"`
	Status       string `json:"status"`
}

type RefreshTokenRequest struct {
	TTLSeconds *int64
}

type RefreshTokenResponse struct {
	Token        string     `json:"token"`
	TokenID      string     `json:"token_id"`
	FileSystemID string     `json:"tenant_id"`
	ScopeKind    string     `json:"scope_kind"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

func (c *Client) GenerateToken(ctx context.Context, creds TiDBCloudCredentials, input GenerateTokenRequest) (GenerateTokenResponse, error) {
	body := struct {
		FileSystemID string `json:"tenant_id"`
		TokenName    string `json:"key_name"`
		TTLSeconds   *int64 `json:"ttl_seconds,omitempty"`
	}{input.FileSystemID, input.TokenName, input.TTLSeconds}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/tokens/generate", body)
	if err != nil {
		return GenerateTokenResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response GenerateTokenResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return GenerateTokenResponse{}, err
	}
	return response, nil
}

func (c *Client) ListTokens(ctx context.Context, creds TiDBCloudCredentials, opts ListTokensOptions) (ListTokensResponse, error) {
	query := url.Values{}
	query.Set("tenant_id", opts.FileSystemID)
	query.Set("offset", strconv.Itoa(opts.Offset))
	query.Set("limit", strconv.Itoa(opts.Limit))
	if opts.IncludeExpired {
		query.Set("include_expired", "1")
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/tokens?"+query.Encode(), nil)
	if err != nil {
		return ListTokensResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response ListTokensResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListTokensResponse{}, err
	}
	if response.Tokens == nil {
		response.Tokens = []TokenMetadata{}
	}
	return response, nil
}

func (c *Client) SetTokenEnabled(ctx context.Context, creds TiDBCloudCredentials, fileSystemID, tokenID string, enabled bool) (TokenMutationResponse, error) {
	action := "deactivate"
	if enabled {
		action = "activate"
	}
	query := url.Values{"tenant_id": []string{fileSystemID}}
	path := "/v1/tokens/" + url.PathEscape(tokenID) + "/" + action + "?" + query.Encode()
	req, err := c.api.NewRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return TokenMutationResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response TokenMutationResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return TokenMutationResponse{}, err
	}
	return response, nil
}

func (c *Client) DeleteToken(ctx context.Context, creds TiDBCloudCredentials, fileSystemID, tokenID string) (TokenMutationResponse, error) {
	query := url.Values{"tenant_id": []string{fileSystemID}}
	path := "/v1/tokens/" + url.PathEscape(tokenID) + "?" + query.Encode()
	req, err := c.api.NewRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return TokenMutationResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response TokenMutationResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return TokenMutationResponse{}, err
	}
	return response, nil
}

func (c *Client) RefreshToken(ctx context.Context, input RefreshTokenRequest) (RefreshTokenResponse, error) {
	body := struct {
		TTLSeconds *int64 `json:"ttl_seconds,omitempty"`
	}{input.TTLSeconds}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/tokens/refresh", body)
	if err != nil {
		return RefreshTokenResponse{}, err
	}
	var response RefreshTokenResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return RefreshTokenResponse{}, err
	}
	return response, nil
}

func setTiDBCloudCredentialHeaders(req *http.Request, creds TiDBCloudCredentials) {
	req.Header.Set(tidbCloudPublicKeyHeader, strings.TrimSpace(creds.PublicKey))
	req.Header.Set(tidbCloudPrivateKeyHeader, strings.TrimSpace(creds.PrivateKey))
}
