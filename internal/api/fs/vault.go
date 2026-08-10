package fs

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type VaultSecret struct {
	Name       string    `json:"name"`
	SecretType string    `json:"secret_type,omitempty"`
	Revision   int64     `json:"revision,omitempty"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

type VaultGrantIssueRequest struct {
	Agent      string   `json:"agent"`
	Scope      []string `json:"scope"`
	Perm       string   `json:"perm"`
	TTLSeconds int      `json:"ttl_seconds"`
	LabelHint  string   `json:"label_hint,omitempty"`
}

type VaultGrantIssueResponse struct {
	Token     string    `json:"token"`
	GrantID   string    `json:"grant_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     []string  `json:"scope,omitempty"`
	Perm      string    `json:"perm,omitempty"`
}

type VaultAuditEvent struct {
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	TokenID    string         `json:"token_id,omitempty"`
	AgentID    string         `json:"agent_id,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	SecretName string         `json:"secret_name,omitempty"`
	FieldName  string         `json:"field_name,omitempty"`
	Adapter    string         `json:"adapter,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
}

func (c *Client) CreateVaultSecret(ctx context.Context, name string, fields map[string]string) (VaultSecret, error) {
	body := map[string]any{
		"name":       name,
		"fields":     fields,
		"created_by": "ti",
	}
	var response VaultSecret
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/vault/secrets", body, &response); err != nil {
		return VaultSecret{}, err
	}
	return response, nil
}

func (c *Client) UpdateVaultSecret(ctx context.Context, name string, fields map[string]string) (VaultSecret, error) {
	body := map[string]any{
		"fields":     fields,
		"updated_by": "ti",
	}
	var response VaultSecret
	if err := c.doVaultJSON(ctx, http.MethodPut, "/v1/vault/secrets/"+url.PathEscape(name), body, &response); err != nil {
		return VaultSecret{}, err
	}
	return response, nil
}

func (c *Client) DeleteVaultSecret(ctx context.Context, name string) error {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/vault/secrets/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, nil)
}

func (c *Client) ListVaultSecrets(ctx context.Context) ([]VaultSecret, error) {
	var response struct {
		Secrets []VaultSecret `json:"secrets"`
	}
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/vault/secrets", nil, &response); err != nil {
		return nil, err
	}
	if response.Secrets == nil {
		response.Secrets = []VaultSecret{}
	}
	return response.Secrets, nil
}

func (c *Client) IssueVaultGrant(ctx context.Context, request VaultGrantIssueRequest) (VaultGrantIssueResponse, error) {
	var response VaultGrantIssueResponse
	if err := c.doVaultJSON(ctx, http.MethodPost, "/v1/vault/grants", request, &response); err != nil {
		return VaultGrantIssueResponse{}, err
	}
	return response, nil
}

func (c *Client) RevokeVaultGrant(ctx context.Context, grantID, revokedBy, reason string) error {
	body := map[string]any{
		"revoked_by": revokedBy,
		"reason":     reason,
	}
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1/vault/grants/"+url.PathEscape(grantID), body)
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, nil)
}

func (c *Client) QueryVaultAudit(ctx context.Context, secretName string, limit int) ([]VaultAuditEvent, error) {
	values := url.Values{}
	if strings.TrimSpace(secretName) != "" {
		values.Set("secret", strings.TrimSpace(secretName))
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	requestPath := "/v1/vault/audit"
	if encoded := values.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	var response struct {
		Events []VaultAuditEvent `json:"events"`
	}
	if err := c.doVaultJSON(ctx, http.MethodGet, requestPath, nil, &response); err != nil {
		return nil, err
	}
	if response.Events == nil {
		response.Events = []VaultAuditEvent{}
	}
	return response.Events, nil
}

func (c *Client) ListReadableVaultSecrets(ctx context.Context) ([]string, error) {
	var response struct {
		Secrets []string `json:"secrets"`
	}
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/vault/read", nil, &response); err != nil {
		return nil, err
	}
	if response.Secrets == nil {
		response.Secrets = []string{}
	}
	return response.Secrets, nil
}

func (c *Client) ReadVaultSecret(ctx context.Context, name string) (map[string]string, error) {
	var response map[string]string
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/vault/read/"+url.PathEscape(name), nil, &response); err != nil {
		return nil, err
	}
	if response == nil {
		response = map[string]string{}
	}
	return response, nil
}

func (c *Client) ReadVaultSecretField(ctx context.Context, name, field string) (string, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/vault/read/"+url.PathEscape(name)+"/"+url.PathEscape(field), nil)
	if err != nil {
		return "", err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", apperr.Wrap("api.read_response", "runtime", 1, "read vault secret field response", err)
	}
	return string(data), nil
}

func (c *Client) ReadVaultSecretAsOwner(ctx context.Context, name string) (map[string]string, error) {
	var response map[string]string
	if err := c.doVaultJSON(ctx, http.MethodGet, "/v1/vault/secrets/"+url.PathEscape(name)+"/value", nil, &response); err != nil {
		return nil, err
	}
	if response == nil {
		response = map[string]string{}
	}
	return response, nil
}

func (c *Client) ReadVaultSecretFieldAsOwner(ctx context.Context, name, field string) (string, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1/vault/secrets/"+url.PathEscape(name)+"/value/"+url.PathEscape(field), nil)
	if err != nil {
		return "", err
	}
	res, err := c.api.DoRaw(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return "", apperr.Wrap("api.read_response", "runtime", 1, "read owner vault secret field response", err)
	}
	return string(data), nil
}

func (c *Client) doVaultJSON(ctx context.Context, method, requestPath string, body any, out any) error {
	req, err := c.api.NewRequest(ctx, method, requestPath, body)
	if err != nil {
		return err
	}
	if err := c.api.DoJSON(req, out); err != nil {
		return err
	}
	return nil
}
