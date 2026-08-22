package trace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTracer_BasicOperations(t *testing.T) {
	tracer := NewTracer(3)
	if !tracer.IsEnabled() {
		t.Errorf("Expected tracer to be enabled by default")
	}

	tracer.Record(Span{TraceID: "t1", DurationMs: 10.0, Method: "GET", Endpoint: "/test"})
	tracer.Record(Span{TraceID: "t2", DurationMs: 20.0, Method: "GET", Endpoint: "/test"})

	spans := tracer.Spans()
	if len(spans) != 2 {
		t.Fatalf("Expected 2 spans, got %d", len(spans))
	}

	// Test capacity overflow FIFO eviction
	tracer.Record(Span{TraceID: "t3", DurationMs: 30.0, Method: "GET", Endpoint: "/test"})
	tracer.Record(Span{TraceID: "t4", DurationMs: 40.0, Method: "GET", Endpoint: "/test"})

	spans = tracer.Spans()
	if len(spans) != 3 {
		t.Fatalf("Expected 3 spans after overflow, got %d", len(spans))
	}
	if spans[0].TraceID != "t2" || spans[2].TraceID != "t4" {
		t.Errorf("Expected oldest span evicted, got first=%s last=%s", spans[0].TraceID, spans[2].TraceID)
	}

	// Test Clear
	tracer.Clear()
	if len(tracer.Spans()) != 0 {
		t.Errorf("Expected 0 spans after Clear")
	}

	// Test Disabled state
	tracer.SetEnabled(false)
	if tracer.IsEnabled() {
		t.Errorf("Expected tracer to be disabled")
	}
	tracer.Record(Span{TraceID: "t5"})
	if len(tracer.Spans()) != 0 {
		t.Errorf("Expected span ignored when disabled")
	}
}

func TestTracer_Summary(t *testing.T) {
	tracer := NewTracer(200)
	for i := 1; i <= 100; i++ {
		tracer.Record(Span{
			TraceID:    "t",
			Method:     "GET",
			Endpoint:   "/items",
			DurationMs: float64(i),
			BytesIn:    10,
			BytesOut:   100,
			StatusCode: 200,
		})
	}
	// Add one error span
	tracer.Record(Span{
		TraceID:    "t-err",
		Method:     "POST",
		Endpoint:   "/items",
		DurationMs: 50.0,
		StatusCode: 500,
	})

	summary := tracer.Summary("test-service")
	if summary.Service != "test-service" {
		t.Errorf("Expected service test-service, got %s", summary.Service)
	}
	if summary.TotalSpans != 101 {
		t.Errorf("Expected 101 total spans, got %d", summary.TotalSpans)
	}
	if summary.ErrorCount != 1 {
		t.Errorf("Expected 1 error count, got %d", summary.ErrorCount)
	}

	stat, ok := summary.Endpoints["GET /items"]
	if !ok {
		t.Fatalf("Missing endpoint stat for GET /items")
	}
	if stat.Count != 100 {
		t.Errorf("Expected 100 count, got %d", stat.Count)
	}
	if stat.MinMs != 1.0 || stat.MaxMs != 100.0 {
		t.Errorf("Expected Min 1.0 and Max 100.0, got min=%v max=%v", stat.MinMs, stat.MaxMs)
	}
	if stat.P50Ms != 51.0 {
		t.Errorf("Expected P50 51.0, got %v", stat.P50Ms)
	}
	if stat.P95Ms != 96.0 {
		t.Errorf("Expected P95 96.0, got %v", stat.P95Ms)
	}
}

func TestMiddleware_TraceEndpoints(t *testing.T) {
	tracer := NewTracer(100)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response body"))
	})

	wrapped := Middleware("test-svc", tracer)(handler)
	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	// 1. Make a traced application request
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/resource/123", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	traceID := resp.Header.Get(HeaderTraceID)
	spanID := resp.Header.Get(HeaderSpanID)
	if traceID == "" || spanID == "" {
		t.Errorf("Expected trace headers in response: traceID=%s spanID=%s", traceID, spanID)
	}

	// 2. Query GET /trace
	traceResp, err := http.Get(ts.URL + "/trace")
	if err != nil {
		t.Fatalf("GET /trace failed: %v", err)
	}
	defer traceResp.Body.Close()

	var spans []Span
	if err := json.NewDecoder(traceResp.Body).Decode(&spans); err != nil {
		t.Fatalf("Failed to decode spans JSON: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("Expected 1 recorded span, got %d", len(spans))
	}
	if spans[0].Service != "test-svc" || spans[0].Endpoint != "/resource/:node" {
		t.Errorf("Span fields mismatch: service=%s endpoint=%s", spans[0].Service, spans[0].Endpoint)
	}

	// 3. Query GET /trace/summary
	summaryResp, err := http.Get(ts.URL + "/trace/summary")
	if err != nil {
		t.Fatalf("GET /trace/summary failed: %v", err)
	}
	defer summaryResp.Body.Close()

	var summary Summary
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Fatalf("Failed to decode summary JSON: %v", err)
	}
	if summary.TotalSpans != 1 {
		t.Errorf("Expected 1 total span in summary, got %d", summary.TotalSpans)
	}

	// 4. POST /trace?enabled=false
	postReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/trace?enabled=false", nil)
	_, _ = http.DefaultClient.Do(postReq)
	if tracer.IsEnabled() {
		t.Errorf("Expected tracer to be disabled via POST /trace")
	}

	// 5. DELETE /trace
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/trace", nil)
	_, _ = http.DefaultClient.Do(delReq)
	if len(tracer.Spans()) != 0 {
		t.Errorf("Expected spans cleared via DELETE /trace")
	}
}

func TestClient_TraceContextPropagation(t *testing.T) {
	var receivedTraceID, receivedParentSpanID string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTraceID = r.Header.Get(HeaderTraceID)
		receivedParentSpanID = r.Header.Get(HeaderParentSpanID)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := WrapClient(ts.Client())

	ctx := ContextWithTrace(context.Background(), TraceContext{
		TraceID: "incoming-trace-abc",
		SpanID:  "current-span-123",
	})

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/call", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	defer resp.Body.Close()

	if receivedTraceID != "incoming-trace-abc" {
		t.Errorf("Expected TraceID 'incoming-trace-abc', got %q", receivedTraceID)
	}
	if receivedParentSpanID != "current-span-123" {
		t.Errorf("Expected ParentSpanID 'current-span-123', got %q", receivedParentSpanID)
	}
}

var _ = time.Now
var _ io.Reader
