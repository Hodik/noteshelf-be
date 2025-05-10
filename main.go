package main

import (
	"context"
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
func main() {
  cfg = setup.Setup(30)
	defer func() {
		if cfg.DBPool != nil {
			log.Println("Closing database connection pool...")
			cfg.DBPool.Close()
		}
	}()

	router := gin.Default()

	router.Use(auth.AuthMiddleware(cfg.Queries))
	router.GET("/me", meHandler)
	router.POST("/upload-book", generateUploadUrlHandler)
	router.POST("/books", confirmBookUploadHandler)
	router.GET("/books", getLibraryHandler)
	router.GET("/books/:book_id", getBookHandler)
	router.PATCH("/books/:book_id/reading-progress", updateReadingProgressHandler)

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
