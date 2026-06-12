package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestContextFromPodAnnotationsUsesTraceparentAnnotation(t *testing.T) {
	const traceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const parentSpanID = "bbbbbbbbbbbbbbbb"
	ctx, ok := ContextFromPodAnnotations(context.Background(), map[string]string{
		TraceparentAnnotation: "00-" + traceID + "-" + parentSpanID + "-01",
	})
	if !ok {
		t.Fatalf("expected traceparent annotation to be parsed")
	}

	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatalf("expected valid span context")
	}
	if !sc.IsRemote() {
		t.Fatalf("expected span context to be marked remote")
	}
	if got := sc.TraceID().String(); got != traceID {
		t.Fatalf("expected trace ID %q, got %q", traceID, got)
	}
	if got := sc.SpanID().String(); got != parentSpanID {
		t.Fatalf("expected parent span ID %q, got %q", parentSpanID, got)
	}
	if sc.TraceFlags()&trace.FlagsSampled == 0 {
		t.Fatalf("expected sampled trace flag")
	}
}

func TestContextFromPodAnnotationsIgnoresMissingOrInvalidTraceparent(t *testing.T) {
	baseCtx := context.Background()

	tests := []struct {
		name        string
		annotations map[string]string
	}{
		{
			name:        "missing annotations",
			annotations: nil,
		},
		{
			name: "missing traceparent annotation",
			annotations: map[string]string{
				"other": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
			},
		},
		{
			name: "invalid traceparent annotation",
			annotations: map[string]string{
				TraceparentAnnotation: "invalid",
			},
		},
		{
			name: "zero trace ID",
			annotations: map[string]string{
				TraceparentAnnotation: "00-00000000000000000000000000000000-bbbbbbbbbbbbbbbb-01",
			},
		},
		{
			name: "zero parent span ID",
			annotations: map[string]string{
				TraceparentAnnotation: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-0000000000000000-01",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, ok := ContextFromPodAnnotations(baseCtx, tt.annotations)
			if ok {
				t.Fatalf("expected traceparent annotation to be ignored")
			}
			if ctx != baseCtx {
				t.Fatalf("expected original context to be returned")
			}
		})
	}
}

func TestContextFromPodAnnotationsUsesConfiguredAnnotationKey(t *testing.T) {
	oldAnnotationKey := annotationKey
	annotationKey = "example.com/traceparent"
	defer func() { annotationKey = oldAnnotationKey }()

	const traceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const parentSpanID = "bbbbbbbbbbbbbbbb"
	ctx, ok := ContextFromPodAnnotations(context.Background(), map[string]string{
		TraceparentAnnotation:     "00-11111111111111111111111111111111-2222222222222222-01",
		"example.com/traceparent": "00-" + traceID + "-" + parentSpanID + "-01",
	})
	if !ok {
		t.Fatalf("expected configured traceparent annotation to be parsed")
	}

	sc := trace.SpanContextFromContext(ctx)
	if got := sc.TraceID().String(); got != traceID {
		t.Fatalf("expected trace ID %q, got %q", traceID, got)
	}
	if got := sc.SpanID().String(); got != parentSpanID {
		t.Fatalf("expected parent span ID %q, got %q", parentSpanID, got)
	}
}
