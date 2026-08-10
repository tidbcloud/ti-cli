package telemetrybackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerAcceptsValidBatchWith202BeforeFlush(t *testing.T) {
	cfg := testConfig()
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), nil)

	response := performBatchRequest(server.Handler(), validRequestBody())
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("response is missing X-Request-ID")
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["accepted"] != true || result["accepted_events"] != float64(1) {
		t.Fatalf("response = %#v", result)
	}
	if batcher.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", batcher.Pending())
	}
}

func TestServerRejectsInvalidRequestsWithGenericErrors(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = int64(len(validRequestBody()))
	tests := []struct {
		name   string
		method string
		body   []byte
		header func(*http.Request)
		want   int
	}{
		{
			name:   "method",
			method: http.MethodGet,
			body:   validRequestBody(),
			want:   http.StatusMethodNotAllowed,
		},
		{
			name:   "content type",
			method: http.MethodPost,
			body:   validRequestBody(),
			header: func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") },
			want:   http.StatusBadRequest,
		},
		{
			name:   "user agent",
			method: http.MethodPost,
			body:   validRequestBody(),
			header: func(request *http.Request) { request.Header.Set("User-Agent", "curl/8") },
			want:   http.StatusBadRequest,
		},
		{
			name:   "oversized",
			method: http.MethodPost,
			body:   append(validRequestBody(), byte(' ')),
			want:   http.StatusRequestEntityTooLarge,
		},
		{
			name:   "invalid json",
			method: http.MethodPost,
			body:   []byte(`{"password":"do-not-echo"}`),
			want:   http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batcher := NewBatcher(cfg, nil, discardLogger(), nil)
			server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), nil)
			request := httptest.NewRequest(test.method, "/v1/telemetry/batch", bytes.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "ti/0.2.0")
			if test.header != nil {
				test.header(request)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "do-not-echo") {
				t.Fatalf("response exposed rejected value: %s", response.Body.String())
			}
		})
	}
}

func TestServerDoesNotLogRequestBodiesOrInstallationIDs(t *testing.T) {
	cfg := testConfig()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	batcher := NewBatcher(cfg, nil, logger, nil)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, logger, nil)

	body := bytes.Replace(
		validRequestBody(),
		[]byte(`"events":[`),
		[]byte(`"password":"highly-sensitive","events":[`),
		1,
	)
	response := performBatchRequest(server.Handler(), body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
	for _, prohibited := range []string{"highly-sensitive", "ti_01j0a0n8m9f4q2x6cn0b9q3k3z"} {
		if strings.Contains(logs.String(), prohibited) {
			t.Fatalf("logs contain prohibited value %q: %s", prohibited, logs.String())
		}
	}
}

func TestServerReturns503WhenBufferIsFull(t *testing.T) {
	cfg := testConfig()
	cfg.BufferMaxEvents = 1
	cfg.FlushMaxEvents = 1
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), nil)
	if response := performBatchRequest(server.Handler(), validRequestBody()); response.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", response.Code)
	}
	if response := performBatchRequest(server.Handler(), validRequestBody()); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
}

func TestServerRateLimitsByTrustedForwardedIP(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimitPerMinute = 1
	cfg.RateLimitBurst = 1
	cfg.TrustedProxyCIDRs, _ = parseTrustedProxyCIDRs("192.0.2.0/24")
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), nil)

	request := newBatchRequest(validRequestBody())
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, request)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d", first.Code)
	}

	request = newBatchRequest(validRequestBody())
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, request)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.Code)
	}
}

func TestServerHealthAndReadiness(t *testing.T) {
	cfg := testConfig()
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), nil)

	health := httptest.NewRecorder()
	server.Handler().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	ready := httptest.NewRecorder()
	server.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d", ready.Code)
	}

	unreadyServer := NewServer(
		cfg,
		batcher,
		readinessStub{err: errors.New("db unavailable")},
		readinessStub{},
		discardLogger(),
		nil,
	)
	unready := httptest.NewRecorder()
	unreadyServer.Handler().ServeHTTP(unready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if unready.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready status = %d, want 503", unready.Code)
	}
}

func TestServerExportsAggregateMetricsWithoutEventValues(t *testing.T) {
	cfg := testConfig()
	metrics := &Metrics{}
	batcher := NewBatcher(cfg, nil, discardLogger(), metrics)
	server := NewServer(cfg, batcher, readinessStub{}, readinessStub{}, discardLogger(), metrics)
	if response := performBatchRequest(server.Handler(), validRequestBody()); response.Code != http.StatusAccepted {
		t.Fatalf("ingest status = %d", response.Code)
	}

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "telemetry_events_accepted_total 1") ||
		!strings.Contains(response.Body.String(), "telemetry_buffer_events 1") {
		t.Fatalf("metrics body = %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "ti_01j0a0n8m9f4q2x6cn0b9q3k3z") {
		t.Fatalf("metrics exposed installation ID: %s", response.Body.String())
	}
}

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "198.51.100.4:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")
	trusted, _ := parseTrustedProxyCIDRs("192.0.2.0/24")
	if got := clientIP(request, trusted); got != "198.51.100.4" {
		t.Fatalf("clientIP = %q", got)
	}
}

func TestRateLimiterBoundsDistinctIPBuckets(t *testing.T) {
	limiter := newIPRateLimiter(60, 1)
	limiter.maxBuckets = 1
	if !limiter.Allow("192.0.2.1") {
		t.Fatal("first IP was rejected")
	}
	if limiter.Allow("192.0.2.2") {
		t.Fatal("new IP was accepted after bucket registry reached its limit")
	}
	if len(limiter.buckets) != 1 {
		t.Fatalf("bucket count = %d, want 1", len(limiter.buckets))
	}
}

func performBatchRequest(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newBatchRequest(body))
	return response
}

func newBatchRequest(body []byte) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/telemetry/batch", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "ti/0.2.0")
	return request
}

type blockingReadiness struct{}

func (blockingReadiness) Ready(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestReadyUsesSinkTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.SinkTimeout = 10 * time.Millisecond
	batcher := NewBatcher(cfg, nil, discardLogger(), nil)
	server := NewServer(cfg, batcher, blockingReadiness{}, readinessStub{}, discardLogger(), nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestServerConvertsUnexpectedPanicTo500(t *testing.T) {
	cfg := testConfig()
	server := NewServer(cfg, nil, readinessStub{}, readinessStub{}, discardLogger(), nil)
	response := performBatchRequest(server.Handler(), validRequestBody())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}
