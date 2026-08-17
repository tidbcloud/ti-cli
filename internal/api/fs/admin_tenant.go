package fs

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

type AdminTenantCreateRequest struct {
	DisplayName string            `json:"display_name,omitempty"`
	Labels      map[string]string `json:"label,omitempty"`
}

type AdminTenantCreateResponse struct {
	TenantID      string            `json:"tenant_id"`
	DisplayName   string            `json:"display_name"`
	Labels        map[string]string `json:"label"`
	APIKey        string            `json:"api_key"`
	Status        string            `json:"status"`
	CloudProvider string            `json:"cloud_provider,omitempty"`
	Region        string            `json:"region,omitempty"`
}

type AdminTenantQuotaConfig struct {
	MaxStorageSize         int64  `json:"max_storage_size"`
	MaxFileSize            int64  `json:"max_file_size"`
	MaxFileCount           int64  `json:"max_file_count"`
	TiDBCloudSpendingLimit *int64 `json:"tidbcloud_spending_limit"`
}

type AdminTenantQuotaUsage struct {
	StorageBytes  int64 `json:"storage_bytes"`
	ReservedBytes int64 `json:"reserved_bytes"`
	FileCount     int64 `json:"file_count"`
}

type AdminTenantQuota struct {
	Config AdminTenantQuotaConfig `json:"config"`
	Usage  AdminTenantQuotaUsage  `json:"usage"`
}

type AdminTenant struct {
	TenantID    string            `json:"tenant_id"`
	DisplayName string            `json:"display_name"`
	Labels      map[string]string `json:"label"`
	Status      string            `json:"status"`
	Kind        string            `json:"kind"`
	Quota       *AdminTenantQuota `json:"quota"`
}

type AdminTenantLabelFilter struct {
	Key   string
	Value string
}

type ListAdminTenantsOptions struct {
	Page        int
	PageSize    int
	DisplayName string
	Label       *AdminTenantLabelFilter
}

type ListAdminTenantsResponse struct {
	Tenants  []AdminTenant `json:"tenants"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	NextPage int           `json:"next_page,omitempty"`
}

type DeleteAdminTenantResponse struct {
	TenantID string `json:"tenant_id"`
	Status   string `json:"status"`
}

func (c *Client) CreateAdminTenant(ctx context.Context, creds TiDBCloudCredentials, input AdminTenantCreateRequest) (AdminTenantCreateResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1/admin/tenants", input)
	if err != nil {
		return AdminTenantCreateResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenantCreateResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenantCreateResponse{}, err
	}
	response.Labels = nonNilLabels(response.Labels)
	return response, nil
}

func (c *Client) ListAdminTenants(ctx context.Context, creds TiDBCloudCredentials, opts ListAdminTenantsOptions) (ListAdminTenantsResponse, error) {
	query := url.Values{}
	query.Set("page", strconv.Itoa(opts.Page))
	query.Set("page_size", strconv.Itoa(opts.PageSize))
	if opts.DisplayName != "" {
		query.Set("display_name", opts.DisplayName)
	}
	if opts.Label != nil {
		query.Set("label", opts.Label.Key+"=="+opts.Label.Value)
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/admin/tenants?"+query.Encode(), nil)
	if err != nil {
		return ListAdminTenantsResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response ListAdminTenantsResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListAdminTenantsResponse{}, err
	}
	if response.Tenants == nil {
		response.Tenants = []AdminTenant{}
	}
	for i := range response.Tenants {
		response.Tenants[i].Labels = nonNilLabels(response.Tenants[i].Labels)
	}
	return response, nil
}

func (c *Client) GetAdminTenant(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string) (AdminTenant, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/admin/tenants/"+url.PathEscape(fileSystemID), nil)
	if err != nil {
		return AdminTenant{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response AdminTenant
	if err := c.api.DoJSON(req, &response); err != nil {
		return AdminTenant{}, err
	}
	response.Labels = nonNilLabels(response.Labels)
	return response, nil
}

func (c *Client) DeleteAdminTenant(ctx context.Context, creds TiDBCloudCredentials, fileSystemID string) (DeleteAdminTenantResponse, error) {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/admin/tenants/"+url.PathEscape(fileSystemID), nil)
	if err != nil {
		return DeleteAdminTenantResponse{}, err
	}
	setTiDBCloudCredentialHeaders(req, creds)
	var response DeleteAdminTenantResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return DeleteAdminTenantResponse{}, err
	}
	return response, nil
}

func nonNilLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return labels
}
