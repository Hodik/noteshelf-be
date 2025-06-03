package observability

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// OtelGinMiddleware returns the standard OTel Gin middleware with custom configuration
func OtelGinMiddleware(serviceName string) gin.HandlerFunc {
	return otelgin.Middleware(serviceName,
		otelgin.WithTracerProvider(otel.GetTracerProvider()),
		otelgin.WithPropagators(otel.GetTextMapPropagator()),
	)
}

// CustomObservabilityMiddleware adds custom tracing on top of the standard OTel middleware
func CustomObservabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get current span from context (created by otelgin middleware)
		span := trace.SpanFromContext(c.Request.Context())

		// Add custom attributes to the span
		if span.IsRecording() {
			span.SetAttributes(
				attribute.String("http.route", c.FullPath()),
				attribute.String("http.user_agent", c.GetHeader("User-Agent")),
				attribute.String("http.remote_addr", c.ClientIP()),
			)

			// Add user ID if available (from auth middleware)
			if userID := c.GetString("user_id"); userID != "" {
				span.SetAttributes(attribute.String("user.id", userID))
			}
		}

		// Process request
		c.Next()

		// Update span with response information
		status := c.Writer.Status()
		statusStr := strconv.Itoa(status)

		if span.IsRecording() {
			span.SetAttributes(
				attribute.Int("http.status_code", status),
				attribute.Int64("http.response_size", int64(c.Writer.Size())),
			)

			// Set span status based on HTTP status
			if status >= 400 {
				span.SetStatus(codes.Error, "HTTP "+statusStr)
				if status >= 500 {
					span.RecordError(nil) // Record as error for 5xx status codes
				}
			} else {
				span.SetStatus(codes.Ok, "")
			}
		}
	}
}

// TraceOperation creates a new span for a custom operation
func TraceOperation(c *gin.Context, operationName string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx := c.Request.Context()

	// Create child span
	ctx, childSpan := Tracer.Start(ctx, operationName, trace.WithAttributes(attributes...))

	return ctx, childSpan
}

// StartSpan creates a new child span from the given context
func StartSpan(ctx context.Context, operationName string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer.Start(ctx, operationName, trace.WithAttributes(attributes...))
}

// TraceDatabase creates a span for database operations
func TraceDatabase(ctx context.Context, operation, table string) (context.Context, trace.Span) {
	return Tracer.Start(ctx, "database."+operation,
		trace.WithAttributes(
			attribute.String("db.operation", operation),
			attribute.String("db.table", table),
			attribute.String("db.system", "postgresql"),
		),
	)
}

// TraceS3Operation creates a span for S3 operations
func TraceS3Operation(ctx context.Context, operation, bucket, key string) (context.Context, trace.Span) {
	return Tracer.Start(ctx, "s3."+operation,
		trace.WithAttributes(
			attribute.String("s3.operation", operation),
			attribute.String("s3.bucket", bucket),
			attribute.String("s3.key", key),
		),
	)
}

// AddEvent adds an event to the span in the given context
func AddEvent(ctx context.Context, name string, attributes ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attributes...))
	}
}

// AddAttributes adds attributes to the span in the given context
func AddAttributes(ctx context.Context, attributes ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(attributes...)
	}
}

// RecordError records an error on the span in the given context
func RecordError(ctx context.Context, err error, description string) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, description)
	}
}

// SetSpanSuccess sets success status on the span in the given context
func SetSpanSuccess(ctx context.Context, description string) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetStatus(codes.Ok, description)
	}
}

// RecordEvent records an event with structured data
func RecordEvent(ctx context.Context, eventName string, data map[string]interface{}) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		attributes := make([]attribute.KeyValue, 0, len(data))
		for key, value := range data {
			switch v := value.(type) {
			case string:
				attributes = append(attributes, attribute.String(key, v))
			case int:
				attributes = append(attributes, attribute.Int(key, v))
			case int64:
				attributes = append(attributes, attribute.Int64(key, v))
			case float64:
				attributes = append(attributes, attribute.Float64(key, v))
			case bool:
				attributes = append(attributes, attribute.Bool(key, v))
			default:
				attributes = append(attributes, attribute.String(key, ""))
			}
		}

		span.AddEvent(eventName, trace.WithAttributes(attributes...))
	}
}

// AddUserContext adds user-related attributes to the span
func AddUserContext(ctx context.Context, userID, status string) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("user.id", userID),
			attribute.String("user.status", status),
		)
	}
}

// AddBookContext adds book-related attributes to the span
func AddBookContext(ctx context.Context, bookID, title string) {
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		span.SetAttributes(
			attribute.String("book.id", bookID),
			attribute.String("book.title", title),
		)
	}
}

// AddSpanEvent adds an event to the current span (gin context version)
func AddSpanEvent(c *gin.Context, name string, attributes ...attribute.KeyValue) {
	span := trace.SpanFromContext(c.Request.Context())
	if span.IsRecording() {
		span.AddEvent(name, trace.WithAttributes(attributes...))
	}
}

// AddSpanAttributes adds attributes to the current span (gin context version)
func AddSpanAttributes(c *gin.Context, attributes ...attribute.KeyValue) {
	span := trace.SpanFromContext(c.Request.Context())
	if span.IsRecording() {
		span.SetAttributes(attributes...)
	}
}

// SetSpanError sets error status on current span (gin context version)
func SetSpanError(c *gin.Context, err error, description string) {
	span := trace.SpanFromContext(c.Request.Context())
	if span.IsRecording() {
		span.RecordError(err)
		span.SetStatus(codes.Error, description)
	}
}

// S3OperationTracer wraps S3 operations with tracing
func S3OperationTracer(ctx context.Context, operation string, bucket, key string) (context.Context, trace.Span) {
	return Tracer.Start(ctx, "s3."+operation,
		trace.WithAttributes(
			attribute.String("s3.bucket", bucket),
			attribute.String("s3.key", key),
			attribute.String("s3.operation", operation),
		),
	)
}
