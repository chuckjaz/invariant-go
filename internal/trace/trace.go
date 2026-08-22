// Package trace provides distributed tracing, metrics collection, and protocol endpoints
// for Invariant microservices.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Header constants for distributed trace propagation
const (
	HeaderTraceID      = "X-Trace-ID"
	HeaderSpanID       = "X-Span-ID"
	HeaderParentSpanID = "X-Parent-Span-ID"
)

type contextKey struct{}

// TraceContext holds the distributed trace and span IDs in a context.
type TraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
}

// ContextWithTrace injects a TraceContext into the given context.
func ContextWithTrace(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, contextKey{}, tc)
}

// FromContext extracts the TraceContext from the given context, if present.
func FromContext(ctx context.Context) (TraceContext, bool) {
	tc, ok := ctx.Value(contextKey{}).(TraceContext)
	return tc, ok
}

// Span represents a single traced operation or HTTP request.
type Span struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Service      string            `json:"service"`
	Endpoint     string            `json:"endpoint"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	StatusCode   int               `json:"status_code"`
	StartTime    time.Time         `json:"start_time"`
	DurationMs   float64           `json:"duration_ms"`
	BytesIn      int64             `json:"bytes_in,omitempty"`
	BytesOut     int64             `json:"bytes_out,omitempty"`
	Error        string            `json:"error,omitempty"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

// EndpointStat contains statistical summary metrics for a specific endpoint.
type EndpointStat struct {
	Count    int     `json:"count"`
	Errors   int     `json:"errors"`
	MinMs    float64 `json:"min_ms"`
	MeanMs   float64 `json:"mean_ms"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	P99Ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
	BytesIn  int64   `json:"bytes_in"`
	BytesOut int64   `json:"bytes_out"`
}

// Summary contains aggregated trace metrics for a service.
type Summary struct {
	Service    string                  `json:"service"`
	TotalSpans int                     `json:"total_spans"`
	ErrorCount int                     `json:"error_count"`
	Endpoints  map[string]EndpointStat `json:"endpoints"`
}

// Tracer manages trace collection in memory with thread safety.
type Tracer struct {
	mu       sync.RWMutex
	enabled  bool
	capacity int
	spans    []Span
}

// NewTracer creates a new Tracer with the given capacity limit.
func NewTracer(capacity int) *Tracer {
	if capacity <= 0 {
		capacity = 10000
	}
	return &Tracer{
		enabled:  true,
		capacity: capacity,
		spans:    make([]Span, 0, 100),
	}
}

// SetEnabled enables or disables trace recording.
func (t *Tracer) SetEnabled(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enabled = enabled
}

// IsEnabled returns whether trace recording is currently active.
func (t *Tracer) IsEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.enabled
}

// Record appends a completed span to the tracer if enabled.
func (t *Tracer) Record(span Span) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.enabled {
		return
	}

	if len(t.spans) >= t.capacity {
		// Evict oldest span (FIFO ring buffer behavior)
		t.spans = t.spans[1:]
	}
	t.spans = append(t.spans, span)
}

// Spans returns a copy of all recorded spans.
func (t *Tracer) Spans() []Span {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]Span, len(t.spans))
	copy(result, t.spans)
	return result
}

// Clear removes all recorded spans.
func (t *Tracer) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.spans = t.spans[:0]
}

// Summary calculates percentiles and aggregate metrics across recorded spans.
func (t *Tracer) Summary(serviceName string) Summary {
	spans := t.Spans()

	summary := Summary{
		Service:    serviceName,
		TotalSpans: len(spans),
		Endpoints:  make(map[string]EndpointStat),
	}

	type rawEndpointData struct {
		durations []float64
		errors    int
		bytesIn   int64
		bytesOut  int64
	}

	grouped := make(map[string]*rawEndpointData)

	for _, s := range spans {
		key := s.Method + " " + s.Endpoint
		data, exists := grouped[key]
		if !exists {
			data = &rawEndpointData{}
			grouped[key] = data
		}
		data.durations = append(data.durations, s.DurationMs)
		if s.StatusCode >= 400 || s.Error != "" {
			data.errors++
			summary.ErrorCount++
		}
		data.bytesIn += s.BytesIn
		data.bytesOut += s.BytesOut
	}

	for endpoint, data := range grouped {
		sort.Float64s(data.durations)
		n := len(data.durations)
		if n == 0 {
			continue
		}

		var sum float64
		for _, d := range data.durations {
			sum += d
		}

		p50 := data.durations[n*50/100]
		p95 := data.durations[n*95/100]
		p99 := data.durations[n*99/100]

		stat := EndpointStat{
			Count:    n,
			Errors:   data.errors,
			MinMs:    data.durations[0],
			MeanMs:   sum / float64(n),
			P50Ms:    p50,
			P95Ms:    p95,
			P99Ms:    p99,
			MaxMs:    data.durations[n-1],
			BytesIn:  data.bytesIn,
			BytesOut: data.bytesOut,
		}
		summary.Endpoints[endpoint] = stat
	}

	return summary
}

// responseWriterWrapper wraps http.ResponseWriter to capture status codes and output sizes.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (rw *responseWriterWrapper) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriterWrapper) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

func (rw *responseWriterWrapper) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RandomID generates a random 16-byte hex ID string.
func RandomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Middleware creates an HTTP middleware handler providing trace collection and trace protocol endpoints.
func Middleware(serviceName string, tracer *Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Protocol endpoint: GET /trace
			if r.URL.Path == "/trace" && r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				if tracer == nil {
					_ = json.NewEncoder(w).Encode([]Span{})
					return
				}
				_ = json.NewEncoder(w).Encode(tracer.Spans())
				return
			}

			// Protocol endpoint: GET /trace/summary
			if r.URL.Path == "/trace/summary" && r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				if tracer == nil {
					_ = json.NewEncoder(w).Encode(Summary{Service: serviceName})
					return
				}
				_ = json.NewEncoder(w).Encode(tracer.Summary(serviceName))
				return
			}

			// Protocol endpoint: POST /trace (toggle enabled state)
			if r.URL.Path == "/trace" && r.Method == http.MethodPost {
				if tracer != nil {
					enabledParam := r.URL.Query().Get("enabled")
					if enabledParam != "" {
						enabled, _ := strconv.ParseBool(enabledParam)
						tracer.SetEnabled(enabled)
					}
				}
				w.WriteHeader(http.StatusOK)
				return
			}

			// Protocol endpoint: DELETE /trace (clear recorded spans)
			if r.URL.Path == "/trace" && r.Method == http.MethodDelete {
				if tracer != nil {
					tracer.Clear()
				}
				w.WriteHeader(http.StatusOK)
				return
			}

			// Normal request tracing
			if tracer == nil || !tracer.IsEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			traceID := r.Header.Get(HeaderTraceID)
			if traceID == "" {
				traceID = RandomID()
			}
			parentSpanID := r.Header.Get(HeaderSpanID)
			spanID := RandomID()

			w.Header().Set(HeaderTraceID, traceID)
			w.Header().Set(HeaderSpanID, spanID)

			tc := TraceContext{
				TraceID:      traceID,
				SpanID:       spanID,
				ParentSpanID: parentSpanID,
			}
			ctx := ContextWithTrace(r.Context(), tc)
			r = r.WithContext(ctx)

			start := time.Now()
			wrappedWriter := &responseWriterWrapper{ResponseWriter: w, statusCode: http.StatusOK}

			var bytesIn int64
			if r.ContentLength > 0 {
				bytesIn = r.ContentLength
			}

			next.ServeHTTP(wrappedWriter, r)

			duration := time.Since(start)
			durationMs := float64(duration.Microseconds()) / 1000.0

			// Compute normalized endpoint pattern for grouping metrics
			endpoint := normalizeEndpoint(r.URL.Path)

			span := Span{
				TraceID:      traceID,
				SpanID:       spanID,
				ParentSpanID: parentSpanID,
				Service:      serviceName,
				Endpoint:     endpoint,
				Method:       r.Method,
				Path:         r.URL.Path,
				StatusCode:   wrappedWriter.statusCode,
				StartTime:    start,
				DurationMs:   durationMs,
				BytesIn:      bytesIn,
				BytesOut:     wrappedWriter.bytesWritten,
			}

			tracer.Record(span)
		})
	}
}

// normalizeEndpoint categorizes dynamic REST paths into generic pattern templates for aggregated stats.
func normalizeEndpoint(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}

	// Replace hex addresses or numerical IDs with :param
	for i, p := range parts {
		if len(p) == 64 && isHex(p) {
			parts[i] = ":id"
		} else if _, err := strconv.ParseUint(p, 10, 64); err == nil && len(p) > 0 {
			parts[i] = ":node"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Transport wraps an http.RoundTripper to propagate trace headers across outgoing HTTP requests.
type Transport struct {
	Base http.RoundTripper
}

// RoundTrip injects trace context into outbound requests.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if tc, ok := FromContext(req.Context()); ok {
		// Propagate Trace ID and assign current SpanID as ParentSpanID
		if req.Header.Get(HeaderTraceID) == "" {
			req.Header.Set(HeaderTraceID, tc.TraceID)
		}
		if req.Header.Get(HeaderParentSpanID) == "" && tc.SpanID != "" {
			req.Header.Set(HeaderParentSpanID, tc.SpanID)
		}
	}

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// WrapClient wraps an http.Client's transport with trace propagation.
func WrapClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	clientCopy := *client
	clientCopy.Transport = &Transport{Base: transport}
	return &clientCopy
}

var _ = slices.Equal[[]int]
var _ io.Reader
