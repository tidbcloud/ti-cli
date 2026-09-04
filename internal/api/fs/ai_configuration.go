package fs

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

type ExtractMediaType string

const (
	ExtractMediaTypeImage ExtractMediaType = "image"
	ExtractMediaTypeAudio ExtractMediaType = "audio"
	ExtractMediaTypeVideo ExtractMediaType = "video"
)

type AdminTenantExtractConfiguration struct {
	Enabled   bool       `json:"enabled"`
	APIBase   *string    `json:"api_base,omitempty"`
	APIKey    *string    `json:"api_key,omitempty"`
	Model     *string    `json:"model,omitempty"`
	Protocol  *string    `json:"protocol,omitempty"`
	Prompt    *string    `json:"prompt,omitempty"`
	Source    string     `json:"source"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type UpdateAdminTenantExtractConfigurationRequest struct {
	Enabled  *bool   `json:"enabled,omitempty"`
	APIBase  *string `json:"api_base,omitempty"`
	APIKey   *string `json:"api_key,omitempty"`
	Model    *string `json:"model,omitempty"`
	Protocol *string `json:"protocol,omitempty"`
	Prompt   *string `json:"prompt,omitempty"`
}

type AdminTenantEmbeddingConfiguration struct {
	Enabled    bool       `json:"enabled"`
	APIBase    *string    `json:"api_base,omitempty"`
	APIKey     *string    `json:"api_key,omitempty"`
	Model      *string    `json:"model,omitempty"`
	Source     string     `json:"source"`
	Generation uint64     `json:"generation,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

type UpdateAdminTenantEmbeddingConfigurationRequest struct {
	Enabled bool    `json:"enabled"`
	APIBase *string `json:"api_base,omitempty"`
	APIKey  *string `json:"api_key,omitempty"`
	Model   *string `json:"model,omitempty"`
}

func (c *Client) GetAdminTenantExtractConfiguration(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string, mediaType ExtractMediaType) (AdminTenantExtractConfiguration, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, adminTenantExtractConfigurationPath(fileSystemID, mediaType), nil)
	if err != nil {
		return AdminTenantExtractConfiguration{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenantExtractConfiguration
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenantExtractConfiguration{}, err
	}
	return response, nil
}

func (c *Client) UpdateAdminTenantExtractConfiguration(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string, mediaType ExtractMediaType, input UpdateAdminTenantExtractConfigurationRequest) (AdminTenantExtractConfiguration, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPut, adminTenantExtractConfigurationPath(fileSystemID, mediaType), input)
	if err != nil {
		return AdminTenantExtractConfiguration{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenantExtractConfiguration
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenantExtractConfiguration{}, err
	}
	return response, nil
}

func (c *Client) GetAdminTenantEmbeddingConfiguration(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string) (AdminTenantEmbeddingConfiguration, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, adminTenantEmbeddingConfigurationPath(fileSystemID), nil)
	if err != nil {
		return AdminTenantEmbeddingConfiguration{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenantEmbeddingConfiguration
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenantEmbeddingConfiguration{}, err
	}
	return response, nil
}

func (c *Client) UpdateAdminTenantEmbeddingConfiguration(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string, input UpdateAdminTenantEmbeddingConfigurationRequest) (AdminTenantEmbeddingConfiguration, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPut, adminTenantEmbeddingConfigurationPath(fileSystemID), input)
	if err != nil {
		return AdminTenantEmbeddingConfiguration{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenantEmbeddingConfiguration
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenantEmbeddingConfiguration{}, err
	}
	return response, nil
}

func adminTenantExtractConfigurationPath(fileSystemID string, mediaType ExtractMediaType) string {
	return "/v1/admin/tenants/" + url.PathEscape(fileSystemID) + "/extract-config/" + url.PathEscape(string(mediaType))
}

func adminTenantEmbeddingConfigurationPath(fileSystemID string) string {
	return "/v1/admin/tenants/" + url.PathEscape(fileSystemID) + "/embedding-config"
}
