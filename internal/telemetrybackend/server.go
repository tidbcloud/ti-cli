package telemetrybackend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
)

type readinessCheck interface {
	Ready(context.Context) error
}

type Server struct {
	config  Config
	batcher *Batcher
	tidb    readinessCheck
	posthog readinessCheck
	limiter *ipRateLimiter
	logger  *slog.Logger
	metrics *Metrics
	now     func() time.Time
}

func NewServer(
	config Config,
	batcher *Batcher,
	tidb readinessCheck,
	posthog readinessCheck,
	logger *slog.Logger,
	metrics *Metrics,
) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if metrics == nil {
		metrics = &Metrics{}
	}
	return &Server{
		config:  config,
		batcher: batcher,
		tidb:    tidb,
		posthog: posthog,
		limiter: newIPRateLimiter(config.RateLimitPerMinute, config.RateLimitBurst),
		logger:  logger,
		metrics: metrics,
		now:     time.Now,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/telemetry/batch", s.handleBatch)
	return s.withRequestID(s.recoverPanics(s.logRequests(mux)))
}

func (s *Server) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(
		writer,
		"telemetry_events_accepted_total %d\n"+
			"telemetry_requests_rejected_total %d\n"+
			"telemetry_rate_limited_total %d\n"+
			"telemetry_buffer_events %d\n"+
			"telemetry_buffer_dropped_total %d\n"+
			"telemetry_flush_events_total %d\n"+
			"telemetry_sink_total{sink=\"tidb\",result=\"success\"} %d\n"+
			"telemetry_sink_total{sink=\"tidb\",result=\"failure\"} %d\n"+
			"telemetry_sink_total{sink=\"posthog\",result=\"success\"} %d\n"+
			"telemetry_sink_total{sink=\"posthog\",result=\"failure\"} %d\n",
		s.metrics.AcceptedEvents.Load(),
		s.metrics.RejectedRequests.Load(),
		s.metrics.RateLimited.Load(),
		s.batcher.Pending(),
		s.metrics.DroppedEvents.Load(),
		s.metrics.FlushedEvents.Load(),
		s.metrics.TiDBSuccesses.Load(),
		s.metrics.TiDBFailures.Load(),
		s.metrics.PostHogSuccesses.Load(),
		s.metrics.PostHogFailures.Load(),
	)
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.config.SinkTimeout)
	defer cancel()
	tidbReady := s.tidb != nil && s.tidb.Ready(ctx) == nil
	postHogReady := s.posthog != nil && s.posthog.Ready(ctx) == nil
	status := http.StatusOK
	if !tidbReady || !postHogReady {
		status = http.StatusServiceUnavailable
	}
	writeJSON(writer, status, map[string]any{
		"ok":                 tidbReady && postHogReady,
		"tidb_configured":    tidbReady,
		"posthog_configured": postHogReady,
	})
}

func (s *Server) handleBatch(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !validContentType(request.Header.Get("Content-Type")) ||
		!validUserAgent(request.Header.Get("User-Agent")) {
		s.reject(writer, http.StatusBadRequest, "invalid_request", "request headers are invalid")
		return
	}
	if !s.limiter.Allow(clientIP(request, s.config.TrustedProxyCIDRs)) {
		s.reject(writer, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded")
		return
	}

	body, err := readLimitedBody(request.Body, s.config.MaxBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			s.reject(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return
		}
		s.reject(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	events, err := decodeAndValidateBatch(body, s.config.MaxEventsPerRequest, s.now())
	if err != nil {
		s.reject(writer, http.StatusBadRequest, "invalid_request", "schema validation failed")
		return
	}
	if !s.batcher.Enqueue(events) {
		s.reject(writer, http.StatusServiceUnavailable, "buffer_full", "telemetry buffer is full")
		return
	}
	if recorder, ok := writer.(*statusRecorder); ok {
		recorder.acceptedEvents = len(events)
		recorder.result = "accepted"
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"accepted":        true,
		"accepted_events": len(events),
		"schema_version":  events[0].SchemaVersion,
	})
}

func (s *Server) reject(writer http.ResponseWriter, status int, code, message string) {
	s.metrics.RejectedRequests.Add(1)
	if status == http.StatusTooManyRequests {
		s.metrics.RateLimited.Add(1)
	}
	if recorder, ok := writer.(*statusRecorder); ok {
		recorder.result = code
	}
	writeJSON(writer, status, map[string]string{
		"error":   code,
		"message": message,
	})
}

type requestIDContextKey struct{}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := newRequestID()
		writer.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(request.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			if recover() != nil {
				s.logger.Error(
					"telemetry request panic",
					"request_id", requestIDFromContext(request.Context()),
					"route", request.URL.Path,
				)
				writeJSON(writer, http.StatusInternalServerError, map[string]string{
					"error":   "internal_error",
					"message": "unexpected backend error",
				})
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		s.logger.Info(
			"telemetry request completed",
			"route", request.URL.Path,
			"request_id", requestIDFromContext(request.Context()),
			"method", request.Method,
			"status_code", recorder.status,
			"result", recorder.result,
			"accepted_events", recorder.acceptedEvents,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status         int
	acceptedEvents int
	result         string
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{
		"error":   "method_not_allowed",
		"message": "HTTP method is not allowed",
	})
}

func validContentType(raw string) bool {
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && mediaType == "application/json"
}

func validUserAgent(value string) bool {
	prefixLength := 0
	switch {
	case strings.HasPrefix(value, "ti/"):
		prefixLength = len("ti/")
	case strings.HasPrefix(value, "tdc/"):
		prefixLength = len("tdc/")
	default:
		return false
	}
	return len(value) > prefixLength && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\t ")
}

var errBodyTooLarge = errors.New("request body too large")

func readLimitedBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func newRequestID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("%x", random[:])
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
