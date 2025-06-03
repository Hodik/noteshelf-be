package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Hodik/noteshelf-be.git/auth"
	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const maxBodyLogSize = 1024 // Maximum bytes to log from request body

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := uuid.New().String()
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// RequestBodyCaptureMiddleware captures request body for error logging
func RequestBodyCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only capture body for certain content types and methods
		if shouldCaptureBody(c) {
			bodyBytes, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.Next()
				return
			}

			// Restore the body for the actual handler
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			// Store sanitized body for potential error logging
			if len(bodyBytes) > 0 {
				sanitizedBody := sanitizeRequestBody(bodyBytes, c.Request.Header.Get("Content-Type"))
				c.Set("request_body", sanitizedBody)
			}
		}
		c.Next()
	}
}

func shouldCaptureBody(c *gin.Context) bool {
	// Only capture for POST, PUT, PATCH requests
	method := c.Request.Method
	if method != "POST" && method != "PUT" && method != "PATCH" {
		return false
	}

	// Only capture for JSON content
	contentType := c.Request.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return false
	}

	// Skip file uploads and large bodies
	if c.Request.ContentLength > maxBodyLogSize {
		return false
	}

	return true
}

func sanitizeRequestBody(bodyBytes []byte, contentType string) string {
	// Limit size
	if len(bodyBytes) > maxBodyLogSize {
		bodyBytes = bodyBytes[:maxBodyLogSize]
	}

	// For JSON, parse and sanitize sensitive fields
	if strings.Contains(contentType, "application/json") {
		var bodyMap map[string]interface{}
		if err := json.Unmarshal(bodyBytes, &bodyMap); err == nil {
			sanitizeJSONFields(bodyMap)
			if sanitizedBytes, err := json.Marshal(bodyMap); err == nil {
				return string(sanitizedBytes)
			}
		}
	}

	return string(bodyBytes)
}

func sanitizeJSONFields(data map[string]interface{}) {
	sensitiveFields := []string{
		"password", "token", "secret", "key", "auth",
		"credit_card", "ssn", "social_security",
	}

	for key, value := range data {
		keyLower := strings.ToLower(key)

		// Check if field is sensitive
		for _, sensitive := range sensitiveFields {
			if strings.Contains(keyLower, sensitive) {
				data[key] = "[REDACTED]"
				break
			}
		}

		// Recursively sanitize nested objects
		if nestedMap, ok := value.(map[string]interface{}); ok {
			sanitizeJSONFields(nestedMap)
		}
	}
}

func RequestLoggingMiddleware() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// Use structured logging for requests
		slog.Info("HTTP Request",
			"method", param.Method,
			"path", param.Path,
			"status", param.StatusCode,
			"latency", param.Latency.String(),
			"client_ip", param.ClientIP,
			"user_agent", param.Request.UserAgent(),
			"request_id", param.Keys["request_id"],
		)
		// Return empty string to prevent default Gin logging
		return ""
	})
}

func LoggingMiddleware() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// Custom log format or use structured logging here
		return ""
	})
}

func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// Only process the first error to avoid multiple responses
		if len(c.Errors) == 0 {
			return
		}

		// Get the first error (most relevant)
		err := c.Errors[0]
		requestID, _ := c.Get("request_id")
		userID := getUserIDFromContext(c)

		// Get captured request body if available
		var requestBody interface{}
		if body, exists := c.Get("request_body"); exists {
			requestBody = body
		}

		switch e := err.Err.(type) {
		case Http:
			if e.StatusCode >= 500 {
				// For server errors, log full details including request body
				slog.Error("Server error",
					"error", e.Description,
					"metadata", e.Metadata,
					"status_code", e.StatusCode,
					"request_id", requestID,
					"user_id", userID,
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"ip", c.ClientIP(),
					"user_agent", c.Request.UserAgent(),
					"request_body", requestBody,
					"query_params", c.Request.URL.RawQuery,
				)
			}
			c.AbortWithStatusJSON(e.StatusCode, e)
		case auth.AuthError:
			// Handle auth errors specifically
			if e.StatusCode >= 500 {
				slog.Error("Authentication server error",
					"error", e.Message,
					"metadata", e.Metadata,
					"status_code", e.StatusCode,
					"request_id", requestID,
					"user_id", userID,
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"ip", c.ClientIP(),
					"user_agent", c.Request.UserAgent(),
				)
			}
			c.AbortWithStatusJSON(e.StatusCode, map[string]interface{}{

				"description": e.Message,
				"metadata":    e.Metadata,
				"statusCode":  e.StatusCode,
				"request_id":  requestID,
			})
		default:
			// For unknown errors, always log as server error with full context
			slog.Error("Unhandled server error",
				"error", e.Error(),
				"request_id", requestID,
				"user_id", userID,
				"method", c.Request.Method,
				"path", c.Request.URL.Path,
				"ip", c.ClientIP(),
				"user_agent", c.Request.UserAgent(),
				"request_body", requestBody,
				"query_params", c.Request.URL.RawQuery,
			)

			if strings.Contains(e.Error(), "no rows") {
				c.AbortWithStatusJSON(http.StatusNotFound, map[string]string{
					"message":    "Resource not found",
					"request_id": requestID.(string),
				})
			} else {
				c.AbortWithStatusJSON(http.StatusInternalServerError, map[string]string{
					"message":    "Internal server error",
					"request_id": requestID.(string),
				})
			}
		}
	}
}

func getUserIDFromContext(c *gin.Context) string {
	if user, exists := c.Get("user"); exists {
		if dbUser, ok := user.(*repository.User); ok {
			return dbUser.ID
		}
	}
	return "anonymous"
}
