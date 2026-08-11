package iam

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/tidbcloud/ti-cli/internal/api"
)

type Client struct {
	api *api.Client
}

func New(client *api.Client) *Client {
	return &Client{api: client}
}

type ListSQLUsersOptions struct {
	PageSize  int32
	PageToken string
}

type ListSQLUsersResponse struct {
	SQLUsers      []SQLUser `json:"sql_users"`
	NextPageToken string    `json:"next_page_token,omitempty"`
}

type SQLUser struct {
	UserName    string   `json:"username"`
	AuthMethod  string   `json:"auth_method,omitempty"`
	BuiltinRole string   `json:"builtin_role,omitempty"`
	CustomRoles []string `json:"custom_roles,omitempty"`
}

type CreateSQLUserRequest struct {
	UserName    string
	Password    string
	AuthMethod  string
	AutoPrefix  bool
	BuiltinRole string
	CustomRoles []string
}

type UpdateSQLUserRequest struct {
	Password    string
	BuiltinRole string
	CustomRoles []string
}

func (c *Client) ListSQLUsers(ctx context.Context, clusterID string, opts ListSQLUsersOptions) (ListSQLUsersResponse, error) {
	requestPath := "/v1beta1/clusters/" + url.PathEscape(clusterID) + "/sqlUsers"
	query := url.Values{}
	if opts.PageToken != "" {
		query.Set("pageToken", opts.PageToken)
	}
	if opts.PageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(int64(opts.PageSize), 10))
	}
	if encoded := query.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}

	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return ListSQLUsersResponse{}, err
	}
	var response listSQLUsersWireResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListSQLUsersResponse{}, err
	}
	return response.toResponse(), nil
}

func (c *Client) CreateSQLUser(ctx context.Context, clusterID string, input CreateSQLUserRequest) (SQLUser, error) {
	body := createSQLUserWireRequest{
		UserName:    input.UserName,
		Password:    input.Password,
		AuthMethod:  input.AuthMethod,
		AutoPrefix:  &input.AutoPrefix,
		BuiltinRole: input.BuiltinRole,
		CustomRoles: input.CustomRoles,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1beta1/clusters/"+url.PathEscape(clusterID)+"/sqlUsers", body)
	if err != nil {
		return SQLUser{}, err
	}
	var response sqlUserWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return SQLUser{}, err
	}
	return response.toSQLUser(), nil
}

func (c *Client) GetSQLUser(ctx context.Context, clusterID, userName string) (SQLUser, error) {
	req, err := c.api.NewRequest(ctx, http.MethodGet, "/v1beta1/clusters/"+url.PathEscape(clusterID)+"/sqlUsers/"+url.PathEscape(userName), nil)
	if err != nil {
		return SQLUser{}, err
	}
	var response sqlUserWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return SQLUser{}, err
	}
	return response.toSQLUser(), nil
}

func (c *Client) UpdateSQLUser(ctx context.Context, clusterID, userName string, input UpdateSQLUserRequest) (SQLUser, error) {
	body := updateSQLUserWireRequest{
		Password:    input.Password,
		BuiltinRole: input.BuiltinRole,
		CustomRoles: input.CustomRoles,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPatch, "/v1beta1/clusters/"+url.PathEscape(clusterID)+"/sqlUsers/"+url.PathEscape(userName), body)
	if err != nil {
		return SQLUser{}, err
	}
	var response sqlUserWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return SQLUser{}, err
	}
	return response.toSQLUser(), nil
}

func (c *Client) DeleteSQLUser(ctx context.Context, clusterID, userName string) error {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1beta1/clusters/"+url.PathEscape(clusterID)+"/sqlUsers/"+url.PathEscape(userName), nil)
	if err != nil {
		return err
	}
	return c.api.DoJSON(req, nil)
}

type listSQLUsersWireResponse struct {
	SQLUsers         []sqlUserWire `json:"sqlUsers"`
	SQLUsersAlt      []sqlUserWire `json:"sql_users"`
	NextPageToken    string        `json:"nextPageToken"`
	NextPageTokenAlt string        `json:"next_page_token"`
}

func (r listSQLUsersWireResponse) toResponse() ListSQLUsersResponse {
	wireUsers := r.SQLUsers
	if wireUsers == nil {
		wireUsers = r.SQLUsersAlt
	}
	users := make([]SQLUser, 0, len(wireUsers))
	for _, user := range wireUsers {
		users = append(users, user.toSQLUser())
	}
	nextPageToken := r.NextPageToken
	if nextPageToken == "" {
		nextPageToken = r.NextPageTokenAlt
	}
	return ListSQLUsersResponse{
		SQLUsers:      users,
		NextPageToken: nextPageToken,
	}
}

type sqlUserWire struct {
	UserName    string   `json:"userName"`
	UserNameAlt string   `json:"username"`
	AuthMethod  string   `json:"authMethod"`
	BuiltinRole string   `json:"builtinRole"`
	CustomRoles []string `json:"customRoles,omitempty"`
}

func (u sqlUserWire) toSQLUser() SQLUser {
	userName := u.UserName
	if userName == "" {
		userName = u.UserNameAlt
	}
	return SQLUser{
		UserName:    userName,
		AuthMethod:  u.AuthMethod,
		BuiltinRole: u.BuiltinRole,
		CustomRoles: u.CustomRoles,
	}
}

type createSQLUserWireRequest struct {
	AuthMethod  string   `json:"authMethod"`
	AutoPrefix  *bool    `json:"autoPrefix,omitempty"`
	BuiltinRole string   `json:"builtinRole"`
	CustomRoles []string `json:"customRoles,omitempty"`
	Password    string   `json:"password"`
	UserName    string   `json:"userName"`
}

type updateSQLUserWireRequest struct {
	BuiltinRole string   `json:"builtinRole,omitempty"`
	CustomRoles []string `json:"customRoles,omitempty"`
	Password    string   `json:"password,omitempty"`
}
