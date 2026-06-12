package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const endpoint = "127.0.0.1:4317"

func main() {
	ctx := context.Background()
	tp, err := initTracer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init tracer: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "shutdown tracer: %v\n", err)
		}
	}()

	tracer := otel.Tracer("trace-span-demo")
	ctx, span := tracer.Start(ctx, "root")
	printSpan("root", span)
	defer span.End()

	work(ctx, tracer, "foo", func(ctx context.Context) {
		work(ctx, tracer, "bar", func(ctx context.Context) {
			work(ctx, tracer, "baz", nil)
		})
	})

	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tp.ForceFlush(flushCtx); err != nil {
		fmt.Fprintf(os.Stderr, "flush spans: %v\n", err)
		os.Exit(1)
	}
}

func initTracer(ctx context.Context) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			attribute.String("service.name", "trace-span-demo"),
			attribute.String("service.component", "demo"),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

func work(ctx context.Context, tracer trace.Tracer, name string, next func(context.Context)) {
	ctx, span := tracer.Start(ctx, name)
	printSpan(name, span)
	defer span.End()

	d := randomDuration()
	span.SetAttributes(attribute.Int64("demo.sleep_ms", d.Milliseconds()))
	time.Sleep(d)

	if next != nil {
		next(ctx)
	}
}

func randomDuration() time.Duration {
	jitter := rand.Intn(401) - 200 // 300ms to 700ms.
	return time.Duration(500+jitter) * time.Millisecond
}

func printSpan(name string, span trace.Span) {
	sc := span.SpanContext()
	fmt.Printf("span=%s traceID=%s spanID=%s\n", name, sc.TraceID(), sc.SpanID())
}
