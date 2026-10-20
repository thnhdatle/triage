// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const tracerName = "github.com/thnhdatle/triage"

// Options are plain values taken from the configuration.
type Options struct {
	// ServiceName is the OTel service.name (default "triage").
	ServiceName string
	// Version, Commit and GoVersion feed triage_build_info and the resource.
	Version, Commit, GoVersion string
	// TracingEnabled turns the OTLP exporter on. Endpoint, headers and
	// timeouts come from the standard OTEL_EXPORTER_OTLP_* variables.
	TracingEnabled bool
	// Protocol is "http/protobuf" (default) or "grpc".
	Protocol string
	// CaptureContent reports whether prompt content may be put on spans.
	CaptureContent bool
	// SpanExporter, when set, replaces the OTLP exporter and is used
	// synchronously (tests, in-memory exporters). It implies tracing on.
	SpanExporter sdktrace.SpanExporter
}

// Telemetry owns the metrics registry and the tracer provider.
type Telemetry struct {
	m       *metrics
	tracer  trace.Tracer
	tp      trace.TracerProvider
	sdk     *sdktrace.TracerProvider
	capture bool
}

// Setup builds the telemetry. With tracing disabled it installs no exporter
// and spans are no-ops, so it never touches the network.
func Setup(ctx context.Context, o Options) (*Telemetry, error) {
	if o.ServiceName == "" {
		o.ServiceName = "triage"
	}
	if o.GoVersion == "" {
		o.GoVersion = runtime.Version()
	}
	t := &Telemetry{m: newMetrics(o.Version, o.Commit, o.GoVersion), capture: o.CaptureContent}
	if !o.TracingEnabled && o.SpanExporter == nil {
		t.tp = noop.NewTracerProvider()
		t.tracer = t.tp.Tracer(tracerName)
		return t, nil
	}
	opts := []sdktrace.TracerProviderOption{}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", o.ServiceName), attribute.String("service.version", o.Version)))
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	opts = append(opts, sdktrace.WithResource(res))
	if o.SpanExporter != nil {
		opts = append(opts, sdktrace.WithSyncer(o.SpanExporter))
	} else {
		exp, err := newExporter(ctx, o.Protocol)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	t.sdk = sdktrace.NewTracerProvider(opts...)
	t.tp = t.sdk
	t.tracer = t.tp.Tracer(tracerName)
	return t, nil
}

func newExporter(ctx context.Context, protocol string) (sdktrace.SpanExporter, error) {
	switch protocol {
	case "", "http/protobuf", "http":
		e, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: otlp http exporter: %w", err)
		}
		return e, nil
	case "grpc":
		e, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("telemetry: otlp grpc exporter: %w", err)
		}
		return e, nil
	}
	return nil, fmt.Errorf("telemetry: unknown otlp protocol %q", protocol)
}

// Shutdown flushes and stops the tracer provider. It is safe to call on a
// Telemetry without an exporter.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t.sdk == nil {
		return nil
	}
	if err := t.sdk.Shutdown(ctx); err != nil {
		return errors.Join(errors.New("telemetry: shutdown"), err)
	}
	return nil
}

// Tracer returns the tracer for spans created outside this package.
func (t *Telemetry) Tracer() trace.Tracer { return t.tracer }

// CaptureContent reports tracing.capture_content.
func (t *Telemetry) CaptureContent() bool { return t.capture }

// StartAttempt opens the GenAI child span `<operation> <model>` of one
// upstream attempt. operation is "chat" or "embeddings"; the caller sets the
// usage and response attributes and ends the span.
func (t *Telemetry) StartAttempt(
	ctx context.Context, operation, provider, modelName string,
) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, operation+" "+modelName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", operation),
			attribute.String("gen_ai.provider.name", provider),
			attribute.String("gen_ai.request.model", modelName),
		))
}
