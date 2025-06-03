package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log"

	"github.com/Hodik/noteshelf-be.git/auth"
	docs "github.com/Hodik/noteshelf-be.git/docs"
	"github.com/Hodik/noteshelf-be.git/observability"
	"github.com/Hodik/noteshelf-be.git/setup"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title           Noteshelf API
// @version         1.0
// @description     A digital bookshelf and note-taking API for managing books and reading progress.
// @termsOfService  http://swagger.io/terms/

// @contact.name   API Support
// @contact.url    http://www.swagger.io/support
// @contact.email  support@swagger.io

// @license.name  Apache 2.0
// @license.url   http://www.apache.org/licenses/LICENSE-2.0.html

// @host      localhost:8080
// @BasePath  /

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token.

var cfg setup.Config

func setupLogger() {
	opts := &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true, // Add file:line info
	}

	var handler slog.Handler
	if gin.Mode() == gin.ReleaseMode {
		// JSON format for production
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		// Human-readable for development
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

func main() {
	setupLogger()

	docs.SwaggerInfo.BasePath = "/"
	cfg = setup.Setup(30)
	defer func() {
		if cfg.DBPool != nil {
			log.Println("Closing database connection pool...")
			cfg.DBPool.Close()
		}
	}()

	// Initialize OpenTelemetry
	otelConfig := observability.DefaultConfig()
	otelShutdown, err := observability.Initialize(otelConfig)
	if err != nil {
		log.Fatalf("Failed to initialize OpenTelemetry: %v", err)
	}
	defer func() {
		log.Println("Shutting down OpenTelemetry...")
		otelShutdown()
	}()

	router := gin.New()

	// Add recovery middleware (equivalent to gin.Default())
	router.Use(gin.Recovery())

	// OpenTelemetry middleware - add this EARLY in the chain
	router.Use(observability.OtelGinMiddleware("noteshelf-api"))
	router.Use(observability.CustomObservabilityMiddleware())

	// CORS configuration for frontend at localhost:3000
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000"},
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With"},
		ExposeHeaders:    []string{"Content-Length", "X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	router.Use(RequestLoggingMiddleware())
	router.Use(RequestIDMiddleware())
	router.Use(RequestBodyCaptureMiddleware())
	router.Use(ErrorHandler())

	router.POST("/wait-list", registerWaitListEmail)

	// Swagger documentation route
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	authorized := router.Group("/")
	authorized.Use(auth.AuthMiddleware(cfg.Queries))
	authorized.GET("/me", meHandler)
	authorized.POST("/upload-book", generateUploadUrlHandler)
	authorized.POST("/books", confirmBookUploadHandler)
	authorized.GET("/books", getLibraryHandler)
	authorized.GET("/public-books", getSharedLibraryHandler)
	authorized.GET("/books/:book_id", getBookHandler)
	authorized.GET("/books/:book_id/notes", getNotes)
	authorized.POST("/books/:book_id/notes", createNote)
	authorized.PATCH("/books/:book_id/reading-progress", updateReadingProgressHandler)
	authorized.DELETE("/notes/:note_id", deleteNote)
	authorized.PATCH("/notes/:note_id", updateNote)

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited gracefully")
}
