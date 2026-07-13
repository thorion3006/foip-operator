/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Config controls the runtime telemetry identity.
type Config struct {
	ServiceName string
	Component   string
}

// Setup configures OpenTelemetry tracing for the process.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	serviceName := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	if serviceName == "" {
		serviceName = strings.TrimSpace(cfg.ServiceName)
	}
	if serviceName == "" {
		serviceName = "foip-operator"
	}

	attrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
	}
	if cfg.Component != "" {
		attrs = append(attrs, attribute.String("foip.component", cfg.Component))
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		attrs = append(attrs, attribute.String("service.instance.id", host))
	}
	if podName := strings.TrimSpace(os.Getenv("POD_NAME")); podName != "" {
		attrs = append(attrs, attribute.String("k8s.pod.name", podName))
	}
	if podNamespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); podNamespace != "" {
		attrs = append(attrs, attribute.String("k8s.namespace.name", podNamespace))
	}
	if nodeName := strings.TrimSpace(os.Getenv("NODE_NAME")); nodeName != "" {
		attrs = append(attrs, attribute.String("k8s.node.name", nodeName))
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("building otel resource: %w", err)
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	exporter, err := newTraceExporter(ctx)
	if err != nil {
		return nil, err
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}
	if exporter != nil {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func newTraceExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	if endpoint == "" {
		return nil, nil
	}

	u, err := url.Parse(endpoint)
	insecure := false
	if err == nil && u.Scheme != "" {
		switch u.Scheme {
		case "http":
			insecure = true
		case "https":
			insecure = false
		default:
			return nil, fmt.Errorf("unsupported OTLP endpoint scheme %q", u.Scheme)
		}
		endpoint = u.Host
	}
	if endpoint == "" {
		return nil, errors.New("OTEL_EXPORTER_OTLP endpoint is empty")
	}
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); v != "" {
		insecure = strings.EqualFold(v, "true")
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	return otlptracegrpc.New(ctx, opts...)
}

// Logger returns a structured logger enriched with trace identifiers when a span is active.
func Logger(ctx context.Context, base logr.Logger) logr.Logger {
	fields := TraceFields(ctx)
	if len(fields) == 0 {
		return base
	}
	return base.WithValues(fields...)
}

// TraceFields returns trace correlation fields for structured logging.
func TraceFields(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []any{
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	}
}

// StartSpan starts a span using the named tracer and returns the derived context.
func StartSpan(ctx context.Context, tracerName, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, spanName, trace.WithAttributes(attrs...))
}

// RecordSpanError marks a span as failed when err is non-nil.
func RecordSpanError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
