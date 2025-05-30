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
	"github.com/Hodik/noteshelf-be.git/setup"
	"github.com/gin-gonic/gin"
)

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

	cfg = setup.Setup(30)
	defer func() {
		if cfg.DBPool != nil {
			log.Println("Closing database connection pool...")
			cfg.DBPool.Close()
		}
	}()

	router := gin.New()

	router.Use(gin.Recovery())

	router.Use(RequestLoggingMiddleware())
	router.Use(RequestIDMiddleware())
	router.Use(RequestBodyCaptureMiddleware())
	router.Use(ErrorHandler())

	router.POST("/wait-list", registerWaitListEmail)

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
