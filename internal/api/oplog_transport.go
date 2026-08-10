package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/oplog"
)

type oplogRoundTripper struct {
	base       http.RoundTripper
	service    endpoints.Service
	operation  string
	profile    string
	provider   string
	regionCode string
	permission authz.Permission
}

func newOplogRoundTripper(base http.RoundTripper, opts Options) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return oplogRoundTripper{
		base:       base,
		service:    opts.Endpoint.Service,
		operation:  opts.Action,
		profile:    opts.ProfileName,
		provider:   opts.Endpoint.Provider,
		regionCode: opts.Endpoint.RegionCode,
		permission: opts.Permission,
	}
}

func (t oplogRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := t.base.RoundTrip(req)
	event := oplog.Event{
		Type:       "api",
		Profile:    t.profile,
		RegionCode: t.regionCode,
		Service:    serviceName(t.service),
		Operation:  operationName(t.operation, t.permission),
		Method:     req.Method,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if res != nil {
		event.StatusCode = res.StatusCode
		event.RequestID = responseRequestID(res.Header)
	}
	if err != nil {
		event.ErrorCode = "api.network_error"
		event.ErrorCategory = "api"
	}
	oplog.FromContext(req.Context()).Record(req.Context(), event)
	return res, err
}

func serviceName(service endpoints.Service) string {
	switch service {
	case endpoints.ServiceStarter:
		return "tidb_cloud_starter"
	case endpoints.ServiceIAM:
		return "tidb_cloud_iam"
	case endpoints.ServiceFS:
		return "ti_fs"
	default:
		if service == "" {
			return "api"
		}
		return string(service)
	}
}

func operationName(action string, permission authz.Permission) string {
	action = strings.TrimSpace(action)
	if action != "" {
		return action
	}
	if permission != "" {
		return string(permission)
	}
	return "api request"
}

func responseRequestID(header http.Header) string {
	for _, key := range []string{
		"X-Request-Id",
		"X-Request-ID",
		"X-Trace-Id",
		"X-Trace-ID",
		"X-Debug-Trace-Id",
		"X-Debug-Trace-ID",
		"Request-Id",
		"Traceparent",
	} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
