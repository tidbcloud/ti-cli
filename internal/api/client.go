package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/auth"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
)

const defaultUserAgent = "ti"

const defaultMaxRetries = 2

type Client struct {
	BaseURL     *url.URL
	HTTPClient  *http.Client
	ProfileName string
	Permission  authz.Permission
	Action      string
	Provider    string
	RegionCode  string
	Service     endpoints.Service
	UserAgent   string
	MaxRetries  int
	Redactor    apitransport.Redactor
	BearerAuth  bool
}

type Options struct {
	Endpoint    endpoints.Endpoint
	ProfileName string
	Permission  authz.Permission
	Action      string
	HTTPClient  *http.Client
	Transport   http.RoundTripper
	Timeout     time.Duration
	Debug       bool
	DebugWriter io.Writer
	Redactor    apitransport.Redactor
	UserAgent   string
	MaxRetries  int
}

func New(opts Options) (*Client, error) {
	if opts.Endpoint.BaseURL == "" {
		return nil, apperr.New("api.missing_endpoint", "config", 2, "internal API endpoint is missing")
	}
	baseURL, err := url.Parse(opts.Endpoint.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, apperr.Wrap("api.invalid_endpoint", "config", 2, fmt.Sprintf("invalid internal endpoint %q", opts.Endpoint.BaseURL), err)
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		transport := opts.Transport
		if opts.Debug {
			transport = apitransport.NewDebugRoundTripper(transport, opts.DebugWriter, opts.Redactor)
		}
		transport = newOplogRoundTripper(transport, opts)
		httpClient = &http.Client{
			Transport: transport,
			Timeout:   opts.Timeout,
		}
	}

	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	} else if maxRetries < 0 {
		maxRetries = 0
	}

	return &Client{
		BaseURL:     baseURL,
		HTTPClient:  httpClient,
		ProfileName: opts.ProfileName,
		Permission:  opts.Permission,
		Action:      opts.Action,
		Provider:    opts.Endpoint.Provider,
		RegionCode:  opts.Endpoint.RegionCode,
		Service:     opts.Endpoint.Service,
		UserAgent:   userAgent,
		MaxRetries:  maxRetries,
		Redactor:    opts.Redactor,
	}, nil
}

func NewDigestClient(profile *config.Profile, endpoint endpoints.Endpoint, permission authz.Permission, opts Options) (*Client, error) {
	creds, err := auth.ValidateProfile(profile)
	if err != nil {
		return nil, err
	}
	opts.Endpoint = endpoint
	opts.ProfileName = creds.ProfileName
	opts.Permission = permission
	opts.Transport = auth.NewDigestTransport(creds, opts.Transport)
	opts.Redactor.Secrets = append(opts.Redactor.Secrets, creds.PublicKey, creds.PrivateKey)
	return New(opts)
}

func NewBearerClient(profileName, apiKey string, endpoint endpoints.Endpoint, permission authz.Permission, opts Options) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, apperr.New(
			"auth.missing_fs_api_key",
			"authentication",
			3,
			fmt.Sprintf("authentication required: missing fs_api_key for profile %q. Create or configure a ti fs resource first.", profileName),
		)
	}
	opts.Endpoint = endpoint
	opts.ProfileName = profileName
	opts.Permission = permission
	opts.Transport = apitransport.NewBearer(apiKey, opts.Transport)
	opts.Redactor.Secrets = append(opts.Redactor.Secrets, apiKey)
	client, err := New(opts)
	if err != nil {
		return nil, err
	}
	client.BearerAuth = true
	return client, nil
}

func (c *Client) NewRequest(ctx context.Context, method, requestPath string, body any) (*http.Request, error) {
	requestURL, err := c.resolveURL(requestPath)
	if err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, apperr.Wrap("api.encode_request", "runtime", 1, "encode API request body", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, apperr.Wrap("api.build_request", "runtime", 1, "build API request", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) DoJSON(req *http.Request, out any) error {
	res, err := c.do(req)
	if err != nil {
		return &Error{
			Code:     "api.network_error",
			Category: "api",
			ExitCode: 1,
			Message:  "API request failed: check network connectivity and try again",
			Cause:    err,
		}
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return c.statusError(req, res)
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	decoder := json.NewDecoder(res.Body)
	if err := decoder.Decode(out); err != nil {
		return &Error{
			Code:       "api.decode_response",
			Category:   "api",
			ExitCode:   1,
			StatusCode: res.StatusCode,
			Message:    "API response was not valid JSON",
			Cause:      err,
		}
	}
	return nil
}

func (c *Client) DoRaw(req *http.Request) (*http.Response, error) {
	res, err := c.do(req)
	if err != nil {
		return nil, &Error{
			Code:     "api.network_error",
			Category: "api",
			ExitCode: 1,
			Message:  "API request failed: check network connectivity and try again",
			Cause:    err,
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, c.statusError(req, res)
	}
	return res, nil
}

func (c *Client) DoRawNoRedirect(req *http.Request) (*http.Response, error) {
	httpClient := *c.HTTPClient
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client := *c
	client.HTTPClient = &httpClient
	res, err := client.do(req)
	if err != nil {
		return nil, &Error{
			Code:     "api.network_error",
			Category: "api",
			ExitCode: 1,
			Message:  "API request failed: check network connectivity and try again",
			Cause:    err,
		}
	}
	if res.StatusCode >= 400 || res.StatusCode < 200 {
		defer res.Body.Close()
		return nil, client.statusError(req, res)
	}
	return res, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	attempts := c.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	canRetry := retryableRequest(req)
	var lastRes *http.Response
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		nextReq := req
		if attempt > 0 {
			clone := req.Clone(req.Context())
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				clone.Body = body
			}
			nextReq = clone
		}
		res, err := c.HTTPClient.Do(nextReq)
		if err != nil {
			lastErr = err
			if !canRetry {
				return nil, err
			}
			continue
		}
		if !canRetry || !retryable(res.StatusCode) || attempt == attempts-1 {
			return res, nil
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		lastRes = res
	}
	if lastRes != nil {
		return lastRes, nil
	}
	return nil, lastErr
}

func retryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

func retryableRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func (c *Client) statusError(req *http.Request, res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	body = []byte(c.Redactor.Redact(string(body)))
	apiMessage := responseMessage(body)
	requestID := responseRequestID(res.Header)
	switch res.StatusCode {
	case http.StatusBadRequest:
		message := messageOrDefault(apiMessage, "remote API rejected the request: check command flags and try again")
		code := "api.invalid_request"
		lowerMessage := strings.ToLower(message)
		if strings.Contains(lowerMessage, "quota") || strings.Contains(lowerMessage, "capacity") || strings.Contains(lowerMessage, "limit") {
			code = "api.quota_or_capacity"
			message = "quota or capacity limit hit: " + message
		}
		return &Error{
			Code:       code,
			Category:   "api",
			ExitCode:   2,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    message,
			Body:       string(body),
		}
	case http.StatusUnauthorized:
		message := fmt.Sprintf("authentication failed: TiDB Cloud rejected the API key pair for profile %q. Check ~/.ti/credentials or create a new API key.", profileName(c.ProfileName))
		if c.Service == endpoints.ServiceFS {
			if !c.BearerAuth {
				message = fmt.Sprintf("authentication failed: TiDB Cloud rejected the API key pair for profile %q. Check ~/.ti/credentials or create a new API key.", profileName(c.ProfileName))
			} else {
				message = fmt.Sprintf("authentication failed: ti fs rejected the selected token for profile %q. It might be disabled, expired, refreshed elsewhere, or revoked; generate or import a valid token and try again.", profileName(c.ProfileName))
			}
		}
		return &Error{
			Code:       "auth.invalid_credentials",
			Category:   "authentication",
			ExitCode:   3,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    message,
			Body:       string(body),
		}
	case http.StatusForbidden:
		message := permissionDeniedMessage(profileName(c.ProfileName), c.Permission, c.Action, c.Provider, c.RegionCode)
		if c.Service == endpoints.ServiceFS {
			switch c.Permission {
			case authz.FSTokenIssueScoped:
				message = "permission denied: generating a scoped file system token requires an owner FS token; TI_FS_TOKEN or --fs-token currently contains a scoped token or another token without owner permission"
			case authz.FSTokenList, authz.FSTokenDelete:
				if c.BearerAuth {
					message = "permission denied: this token management operation requires an owner FS token; TI_FS_TOKEN or --fs-token currently contains a scoped token or another token without owner permission"
				}
			case authz.FSTokenEnable, authz.FSTokenDisable:
				if c.BearerAuth {
					message = "permission denied: enabling or disabling a token requires an owner FS token and the target must be an fs_scoped token"
				}
			case authz.FSFileRead, authz.FSFileWrite, authz.FSMount:
				message = "permission denied: the selected FS token does not allow this operation or path; use an owner token or a scoped token whose prefix and operations include the request"
			}
			if !c.BearerAuth && apiMessage != "" {
				message += " Backend: " + apiMessage
			}
		}
		return &Error{
			Code:       "authz.permission_denied",
			Category:   "authorization",
			ExitCode:   4,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    message,
			Body:       string(body),
		}
	case http.StatusNotFound:
		return &Error{
			Code:       "api.not_found",
			Category:   "api",
			ExitCode:   5,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    fmt.Sprintf("remote resource not found: %s %s", req.Method, req.URL.Path),
			Body:       string(body),
		}
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return &Error{
			Code:       "api.contract_gap",
			Category:   "api",
			ExitCode:   1,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    fmt.Sprintf("API gap: %s %s is not available from the remote service; keep this command behind its service-specific client until the API contract is confirmed", req.Method, req.URL.Path),
			Body:       string(body),
		}
	case http.StatusPaymentRequired:
		return &Error{
			Code:       "api.payment_required",
			Category:   "api",
			ExitCode:   1,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    "payment required: " + messageOrDefault(apiMessage, "the remote service rejected the request because payment could not be processed"),
			Body:       string(body),
		}
	case http.StatusTooManyRequests:
		return &Error{
			Code:       "api.rate_limited",
			Category:   "api",
			ExitCode:   1,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    messageOrDefault(apiMessage, "API rate limit exceeded: retry later"),
			Body:       string(body),
		}
	case http.StatusConflict:
		message := messageOrDefault(apiMessage, "remote lifecycle conflict: inspect the resource state and retry only when safe")
		if c.Permission == authz.FSTokenEnable && strings.Contains(strings.ToLower(message), "expired") {
			message = "the token is expired and cannot be enabled; generate a new token instead"
		}
		return &Error{Code: "api.conflict", Category: "api", ExitCode: 1, StatusCode: res.StatusCode, RequestID: requestID, Message: message, Body: string(body)}
	default:
		return &Error{
			Code:       "api.remote_error",
			Category:   "api",
			ExitCode:   1,
			StatusCode: res.StatusCode,
			RequestID:  requestID,
			Message:    messageOrDefault(apiMessage, fmt.Sprintf("API request failed with HTTP %d", res.StatusCode)),
			Body:       string(body),
		}
	}
}

func (c *Client) resolveURL(requestPath string) (string, error) {
	if requestPath == "" {
		requestPath = "/"
	}
	parsedPath, err := url.Parse(requestPath)
	if err != nil {
		return "", apperr.Wrap("api.invalid_request_path", "usage", 2, fmt.Sprintf("invalid API request path %q", requestPath), err)
	}
	base := *c.BaseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsedPath.Path, "/")
	base.RawQuery = parsedPath.RawQuery
	return base.String(), nil
}

func responseMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if payload.Message != "" {
		return payload.Message
	}
	return payload.Error
}

func messageOrDefault(message, fallback string) string {
	if message != "" {
		return message
	}
	return fallback
}

func profileName(name string) string {
	if name == "" {
		return config.DefaultProfile
	}
	return name
}

func permissionDeniedMessage(profileName string, permission authz.Permission, action, provider, regionCode string) string {
	if action == "" {
		action = string(permission)
	}
	location := provider
	if regionCode != "" {
		location = provider + "/" + regionCode
	}
	if location == "" {
		location = "the selected scope"
	}
	return fmt.Sprintf("permission denied: profile %q is not allowed to %s in %s. Ask an organization admin for %s permission or use another profile.", profileName, action, location, permission)
}
