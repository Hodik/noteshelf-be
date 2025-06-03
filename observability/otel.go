package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	Tracer trace.Tracer
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	// Sampling
	TraceSampleRate float64
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() Config {
	return Config{
		ServiceName:     getEnvOrDefault("OTEL_SERVICE_NAME", "noteshelf-api"),
		ServiceVersion:  getEnvOrDefault("OTEL_SERVICE_VERSION", "1.0.0"),
		Environment:     getEnvOrDefault("ENVIRONMENT", "development"),
		TraceSampleRate: getEnvFloat("OTEL_TRACE_SAMPLE_RATE", 1.0), // 100% in dev, tune in prod
	}
}

// Initialize sets up OpenTelemetry with tracing only
func Initialize(cfg Config) (func(), error) {
	ctx := context.Background()

	// Create resource with service information
	res, err := createResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Set up trace provider
	traceShutdown, err := setupTracing(ctx, res, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to setup tracing: %w", err)
	}

	// Set up global propagators
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Initialize global tracer
	Tracer = otel.Tracer("noteshelf-api")

	log.Printf("OpenTelemetry tracing initialized for service: %s", cfg.ServiceName)

	// Return cleanup function
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := traceShutdown(ctx); err != nil {
			log.Printf("Error shutting down trace provider: %v", err)
		}
	}, nil
}

func createResource(cfg Config) (*resource.Resource, error) {
	return resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
			semconv.DeploymentEnvironmentKey.String(cfg.Environment),
			attribute.String("service.instance.id", getHostname()),
		),
	)
}

func setupTracing(ctx context.Context, res *resource.Resource, cfg Config) (func(context.Context) error, error) {
	// Check if OTLP endpoint is configured via environment variables
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		log.Println("No OTEL_EXPORTER_OTLP_ENDPOINT configured, traces will not be exported")
		return func(context.Context) error { return nil }, nil
	}

	// Create HTTP OTLP exporter - uses standard OpenTelemetry environment variables
	exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient())
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	// Create trace provider with sampling
	sampler := sdktrace.AlwaysSample()
	if cfg.TraceSampleRate < 1.0 {
		sampler = sdktrace.TraceIDRatioBased(cfg.TraceSampleRate)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// Utility functions
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	}
	return defaultValue
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}
