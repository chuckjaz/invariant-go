package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"invariant/internal/trace"
)

// StartTraceSpan starts a new traced span in the repository workflow layer.
// It returns a child context and a completion function that should be deferred.
func StartTraceSpan(ctx context.Context, tracer *trace.Tracer, operation string) (context.Context, func(err error, keyValues ...string)) {
	if tracer == nil || !tracer.IsEnabled() {
		return ctx, func(err error, keyValues ...string) {}
	}

	startTime := time.Now()
	spanBytes := make([]byte, 8)
	rand.Read(spanBytes)
	spanID := hex.EncodeToString(spanBytes)

	var traceID string
	var parentSpanID string
	if parentTC, ok := trace.FromContext(ctx); ok {
		traceID = parentTC.TraceID
		parentSpanID = parentTC.SpanID
	} else {
		traceBytes := make([]byte, 16)
		rand.Read(traceBytes)
		traceID = hex.EncodeToString(traceBytes)
	}

	childTC := trace.TraceContext{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
	}
	childCtx := trace.ContextWithTrace(ctx, childTC)

	done := func(err error, keyValues ...string) {
		duration := time.Since(startTime)
		attrs := make(map[string]string)
		for i := 0; i+1 < len(keyValues); i += 2 {
			attrs[keyValues[i]] = keyValues[i+1]
		}

		errMsg := ""
		statusCode := 200
		if err != nil {
			errMsg = err.Error()
			statusCode = 500
		}

		span := trace.Span{
			TraceID:      traceID,
			SpanID:       spanID,
			ParentSpanID: parentSpanID,
			Service:      "repository",
			Endpoint:     operation,
			Method:       "CLI",
			Path:         operation,
			StatusCode:   statusCode,
			StartTime:    startTime,
			DurationMs:   float64(duration.Microseconds()) / 1000.0,
			Error:        errMsg,
			Attributes:   attrs,
		}
		tracer.Record(span)
	}

	return childCtx, done
}
