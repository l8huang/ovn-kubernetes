package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracecollectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const endpoint = "127.0.0.1:4317"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <32-hex-trace-id> <16-hex-span-id>\n", os.Args[0])
		os.Exit(2)
	}

	traceIDHex := strings.ToLower(os.Args[1])
	spanIDHex := strings.ToLower(os.Args[2])

	traceID, err := decodeHex(traceIDHex, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid trace id: %v\n", err)
		os.Exit(2)
	}
	spanID, err := decodeHex(spanIDHex, 8)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid span id: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", endpoint, err)
		os.Exit(1)
	}
	defer conn.Close()

	now := uint64(time.Now().UnixNano())
	span := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            spanID,
		Name:              "manual.pod-parent",
		Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
		StartTimeUnixNano: now,
		EndTimeUnixNano:   now + uint64(time.Millisecond),
		Flags:             1,
		Attributes: []*commonpb.KeyValue{
			stringAttr("debug.source", "emit-trace-span"),
		},
	}

	req := &tracecollectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				stringAttr("service.name", "manual-trace-root"),
				stringAttr("service.component", "debug"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "emit-trace-span"},
				Spans: []*tracepb.Span{span},
			}},
		}},
	}

	if _, err := tracecollectorpb.NewTraceServiceClient(conn).Export(ctx, req); err != nil {
		fmt.Fprintf(os.Stderr, "export span: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("emitted traceparent=00-%s-%s-01\n", traceIDHex, spanIDHex)
}

func decodeHex(s string, bytes int) ([]byte, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != bytes*2 {
		return nil, fmt.Errorf("expected %d hex characters, got %d", bytes*2, len(s))
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if allZero(out) {
		return nil, fmt.Errorf("value must not be all zero")
	}
	return out, nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func stringAttr(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: value},
		},
	}
}
