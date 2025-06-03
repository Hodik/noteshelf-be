package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Hodik/noteshelf-be.git/auth"
	"github.com/Hodik/noteshelf-be.git/observability"
	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/Hodik/noteshelf-be.git/setup"
	"github.com/Hodik/noteshelf-be.git/thumbnailgenerator"
	"github.com/Hodik/noteshelf-be.git/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ErrorResponse represents an API error response
type ErrorResponse struct {
	Description string `json:"description" example:"Invalid request"`
	Metadata    string `json:"metadata" example:"Additional error context"`
	StatusCode  int    `json:"statusCode" example:"400"`
	RequestID   string `json:"request_id,omitempty" example:"123e4567-e89b-12d3-a456-426614174000"`
}

// SuccessResponse represents a generic success response
type SuccessResponse struct {
	Message   string `json:"message" example:"Operation completed successfully"`
	RequestID string `json:"request_id,omitempty" example:"123e4567-e89b-12d3-a456-426614174000"`
}

// @Summary Get current user information
// @Description Retrieve the authenticated user's profile information
// @Tags Authentication
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} repository.User
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /me [get]
func meHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.get_user_profile")
	defer span.End()

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		observability.RecordEvent(ctx, "user_profile_access_denied", map[string]interface{}{
			"error": "authentication_failed",
		})
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")
	observability.RecordEvent(ctx, "user_profile_accessed", map[string]interface{}{
		"user_id": dbUser.ID,
	})
	observability.SetSpanSuccess(ctx, "User profile retrieved")

	c.JSON(http.StatusOK, dbUser)
}

// UploadBookRequest represents the request body for generating upload URL
type UploadBookRequest struct {
	Name string `json:"name" binding:"required" example:"my-book.pdf"`
}

// UploadUrlResponse represents the response for upload URL generation
type UploadUrlResponse struct {
	PresignedURL string `json:"presigned_url" example:"https://s3.amazonaws.com/bucket/presigned-url"`
	S3Key        string `json:"s3_key" example:"user123/my-book.pdf"`
}

// @Summary Generate presigned upload URL
// @Description Generate a presigned URL for uploading a PDF book to S3
// @Tags Books
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body UploadBookRequest true "Book upload request"
// @Success 200 {object} UploadUrlResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /upload-book [post]
func generateUploadUrlHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.generate_upload_url")
	defer span.End()

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	var req UploadBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		observability.RecordEvent(ctx, "upload_url_request_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"error":   "validation_failed",
			"reason":  err.Error(),
		})
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	if !strings.HasSuffix(req.Name, ".pdf") {
		observability.RecordEvent(ctx, "upload_url_request_failed", map[string]interface{}{
			"user_id":  dbUser.ID,
			"filename": req.Name,
			"error":    "invalid_format",
			"reason":   "not_pdf",
		})
		c.Error(NewHttpError("invalid book format", "file must be a PDF", http.StatusBadRequest))
		return
	}

	key := dbUser.ID + "/" + req.Name

	// S3 operation span
	s3Ctx, s3Span := observability.TraceS3Operation(ctx, "generate_presigned_url", cfg.BucketName, key)
	defer s3Span.End()

	url, err := utils.GeneratePresignedUploadURL(c, cfg.S3Client, cfg.BucketName, key, cfg.PresignedUrlExpirySeconds)
	if err != nil {
		observability.RecordError(s3Ctx, err, "Failed to generate presigned URL")
		observability.RecordEvent(ctx, "upload_url_generation_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"s3_key":  key,
			"error":   "s3_error",
		})
		c.Error(err)
		return
	}

	observability.SetSpanSuccess(s3Ctx, "Presigned URL generated")
	observability.RecordEvent(ctx, "upload_url_generated", map[string]interface{}{
		"user_id":  dbUser.ID,
		"filename": req.Name,
		"s3_key":   key,
	})
	observability.SetSpanSuccess(ctx, "Upload URL generated successfully")

	c.JSON(http.StatusOK, gin.H{"presigned_url": url, "s3_key": key})
}

// ConfirmBookUploadRequest represents the request body for confirming book upload
type ConfirmBookUploadRequest struct {
	Title      string `json:"title" binding:"required" example:"The Go Programming Language"`
	Author     string `json:"author" example:"Alan Donovan"`
	S3Key      string `json:"s3_key" example:"user123/my-book.pdf"`
	TotalPages int    `json:"total_pages" example:"380"`
}

// @Summary Confirm book upload and create book record
// @Description Confirm that a book has been uploaded to S3 and create a database record
// @Tags Books
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body ConfirmBookUploadRequest true "Book confirmation request"
// @Success 200 {object} repository.Book
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books [post]
func confirmBookUploadHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.confirm_book_upload")
	defer span.End()

	start := time.Now()

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	var req ConfirmBookUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx,
		attribute.String("book.title", req.Title),
		attribute.String("book.author", req.Author),
		attribute.Int("book.total_pages", req.TotalPages),
		attribute.String("book.s3_key", req.S3Key),
	)

	// S3 verification span
	s3Ctx, s3Span := observability.TraceS3Operation(ctx, "head_object", cfg.BucketName, req.S3Key)
	if !utils.KeyExists(c, cfg.S3Client, cfg.BucketName, req.S3Key) {
		observability.RecordError(s3Ctx, fmt.Errorf("s3 key not found"), "S3 file verification failed")
		s3Span.End()
		observability.RecordEvent(ctx, "book_upload_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"s3_key":  req.S3Key,
			"error":   "file_not_found",
		})
		c.Error(NewHttpError("s3 key doesn't exist", "uploaded file not found", http.StatusBadRequest))
		return
	}
	observability.SetSpanSuccess(s3Ctx, "S3 file verified")
	s3Span.End()

	// Database transaction span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "create_book_transaction", "books")
	defer dbSpan.End()

	tx, err := cfg.DBPool.Begin(c)
	if err != nil {
		observability.RecordError(dbCtx, err, "Failed to begin transaction")
		c.Error(err)
		return
	}
	defer tx.Rollback(c)

	localQueries := repository.New(tx)

	// Create book
	createBookCtx, createBookSpan := observability.StartSpan(dbCtx, "database.create_book")
	observability.AddEvent(createBookCtx, "db_query_executed",
		attribute.String("sql.operation", "CREATE_BOOK"),
		attribute.String("db.table", "books"),
	)

	book, err := localQueries.CreateBook(c, repository.CreateBookParams{
		ID:         uuid.New(),
		OwnerID:    dbUser.ID,
		S3Key:      req.S3Key,
		TotalPages: int32(req.TotalPages),
		Title:      req.Title,
	})
	createBookSpan.End()

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == setup.UniqueViolationCode {
			observability.RecordError(dbCtx, err, "Duplicate book entry")
			observability.RecordEvent(ctx, "book_upload_failed", map[string]interface{}{
				"user_id": dbUser.ID,
				"s3_key":  req.S3Key,
				"title":   req.Title,
				"error":   "duplicate_book",
			})
			c.Error(NewHttpError("Book already exists", "duplicate book entry", http.StatusConflict))
			return
		}
		observability.RecordError(dbCtx, err, "Database error creating book")
		c.Error(err)
		return
	}

	// Create reading progress
	progressCtx, progressSpan := observability.StartSpan(dbCtx, "database.create_reading_progress")
	observability.AddEvent(progressCtx, "db_query_executed",
		attribute.String("sql.operation", "CREATE_READING_PROGRESS"),
		attribute.String("db.table", "reading_progress"),
	)

	if _, err := localQueries.CreateReadingProgress(c, repository.CreateReadingProgressParams{BookID: book.ID, UserID: dbUser.ID}); err != nil {
		observability.RecordError(progressCtx, err, "Failed to create reading progress")
		progressSpan.End()
		c.Error(NewHttpError("error while creating reading progress for a book: "+err.Error(), "reading progress creation failed", http.StatusInternalServerError))
		return
	}
	observability.SetSpanSuccess(progressCtx, "Reading progress created")
	progressSpan.End()

	if err := tx.Commit(c); err != nil {
		observability.RecordError(dbCtx, err, "Transaction commit failed")
		c.Error(err)
		return
	}

	observability.SetSpanSuccess(dbCtx, "Book and reading progress created")

	processingDuration := time.Since(start)

	// Start background thumbnail generation with distributed tracing
	go func() {
		bgCtx := trace.ContextWithSpan(context.Background(), span)
		generateThumbnailWithTracing(bgCtx, book, dbUser.ID)
	}()

	// Final success event
	observability.RecordEvent(ctx, "book_uploaded", map[string]interface{}{
		"user_id":            dbUser.ID,
		"book_id":            book.ID.String(),
		"book_title":         book.Title,
		"book_author":        req.Author,
		"total_pages":        req.TotalPages,
		"s3_key":             req.S3Key,
		"processing_time_ms": processingDuration.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Book uploaded successfully")

	c.JSON(http.StatusOK, book)
}

// Background thumbnail generation with tracing
func generateThumbnailWithTracing(ctx context.Context, book repository.Book, userID string) {
	ctx, span := observability.StartSpan(ctx, "background.generate_thumbnail")
	defer span.End()

	observability.AddAttributes(ctx,
		attribute.String("book.id", book.ID.String()),
		attribute.String("user.id", userID),
		attribute.String("book.s3_key", book.S3Key),
	)

	start := time.Now()
	thumbnailWebpKey := strings.Replace(book.S3Key, ".pdf", ".webp", -1)

	slog.Info("Starting thumbnail generation",
		"book_id", book.ID,
		"user_id", userID,
		"s3_key", book.S3Key,
		"thumbnail_key", thumbnailWebpKey,
	)

	// Download from S3
	downloadCtx, downloadSpan := observability.TraceS3Operation(ctx, "get_object", cfg.BucketName, book.S3Key)
	localPath, err := utils.DownloadFileToTmp(downloadCtx, cfg.S3Client, cfg.BucketName, book.S3Key, "/tmp")
	if err != nil {
		observability.RecordError(downloadCtx, err, "Failed to download PDF from S3")
		downloadSpan.End()

		slog.Error("Failed to download PDF from S3",
			"error", err.Error(),
			"book_id", book.ID,
			"user_id", userID,
			"s3_key", book.S3Key,
		)
		return
	}
	observability.SetSpanSuccess(downloadCtx, "PDF downloaded from S3")
	downloadSpan.End()

	// Generate thumbnail
	thumbCtx, thumbSpan := observability.StartSpan(ctx, "thumbnail.generate")
	thumbnailPath, err := thumbnailgenerator.GeneratePdfThumbnail(localPath, "/tmp", userID)
	if err != nil {
		observability.RecordError(thumbCtx, err, "Failed to generate thumbnail")
		thumbSpan.End()

		slog.Error("Failed to generate PDF thumbnail",
			"error", err.Error(),
			"book_id", book.ID,
			"user_id", userID,
			"local_path", localPath,
		)
		return
	}
	observability.SetSpanSuccess(thumbCtx, "Thumbnail generated")
	thumbSpan.End()

	// Convert to WebP
	convertCtx, convertSpan := observability.StartSpan(ctx, "thumbnail.convert_webp")
	webpPath := strings.Replace(thumbnailPath, ".jpg", ".webp", -1)
	if err := thumbnailgenerator.ConvertToWebp(thumbnailPath, webpPath); err != nil {
		observability.RecordError(convertCtx, err, "Failed to convert to WebP")
		convertSpan.End()

		slog.Error("Failed to convert thumbnail to WebP",
			"error", err.Error(),
			"book_id", book.ID,
			"user_id", userID,
			"jpeg_path", thumbnailPath,
			"webp_path", webpPath,
		)
		return
	}
	observability.SetSpanSuccess(convertCtx, "Thumbnail converted to WebP")
	convertSpan.End()

	// Upload thumbnail to S3
	uploadCtx, uploadSpan := observability.TraceS3Operation(ctx, "put_object", cfg.BucketName, thumbnailWebpKey)
	if err := utils.UploadFileToS3(uploadCtx, cfg.S3Client, webpPath, cfg.BucketName, thumbnailWebpKey); err != nil {
		observability.RecordError(uploadCtx, err, "Failed to upload thumbnail to S3")
		uploadSpan.End()

		slog.Error("Failed to upload thumbnail to S3",
			"error", err.Error(),
			"book_id", book.ID,
			"user_id", userID,
			"thumbnail_key", thumbnailWebpKey,
			"local_path", webpPath,
		)
		return
	}
	observability.SetSpanSuccess(uploadCtx, "Thumbnail uploaded to S3")
	uploadSpan.End()

	// Update book with thumbnail
	updateCtx, updateSpan := observability.TraceDatabase(ctx, "update", "books")
	observability.AddEvent(updateCtx, "db_query_executed",
		attribute.String("sql.operation", "UPDATE_BOOK_THUMBNAIL"),
		attribute.String("db.table", "books"),
	)

	if _, err := cfg.Queries.UpdateBook(updateCtx, repository.UpdateBookParams{ThumbnailS3Key: &thumbnailWebpKey, BookID: book.ID}); err != nil {
		observability.RecordError(updateCtx, err, "Failed to update book with thumbnail")
		updateSpan.End()

		slog.Error("Failed to update book with thumbnail",
			"error", err.Error(),
			"book_id", book.ID,
			"user_id", userID,
			"thumbnail_key", thumbnailWebpKey,
		)
		return
	}
	observability.SetSpanSuccess(updateCtx, "Book updated with thumbnail")
	updateSpan.End()

	// Final success event
	processingTime := time.Since(start)
	observability.RecordEvent(ctx, "thumbnail_generated", map[string]interface{}{
		"book_id":            book.ID.String(),
		"user_id":            userID,
		"thumbnail_key":      thumbnailWebpKey,
		"processing_time_ms": processingTime.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Thumbnail generation completed")

	slog.Info("Thumbnail generation completed successfully",
		"book_id", book.ID,
		"user_id", userID,
		"thumbnail_key", thumbnailWebpKey,
	)
}

// BookWithDetails represents a book with reading URL and progress
type BookWithDetails struct {
	Book        repository.Book `json:"book"`
	CurrentPage int32           `json:"current_page" example:"150"`
	ReadURL     string          `json:"read_url" example:"https://cloudfront.net/signed-url"`
}

// @Summary Get book details
// @Description Retrieve a specific book by ID with reading URL and current progress
// @Tags Books
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param book_id path string true "Book ID" Format(uuid)
// @Success 200 {object} BookWithDetails
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books/{book_id} [get]
func getBookHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.get_book")
	defer span.End()

	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid book ID format")
		observability.RecordEvent(ctx, "book_access_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"book_id": bookID,
			"error":   "invalid_uuid",
		})
		c.Error(NewHttpError(bookID+" is not a valid uuid", "invalid book ID format", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx, attribute.String("book.id", uuidBookID.String()))

	// Database query span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_book_by_id", "books")
	observability.AddEvent(dbCtx, "db_query_executed",
		attribute.String("sql.operation", "GET_BOOK_BY_ID"),
		attribute.String("db.table", "books"),
	)

	bookRow, err := cfg.Queries.GetBookByID(dbCtx, uuidBookID)
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Book not found")
		observability.RecordEvent(ctx, "book_access_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"book_id": uuidBookID.String(),
			"error":   "not_found",
		})
		c.Error(err)
		return
	}

	if bookRow.Book.OwnerID != dbUser.ID {
		observability.RecordEvent(ctx, "book_access_denied", map[string]interface{}{
			"user_id":       dbUser.ID,
			"book_id":       uuidBookID.String(),
			"book_owner_id": bookRow.Book.OwnerID,
			"error":         "access_denied",
		})
		c.Error(NewHttpError("not an owner", "access denied", http.StatusForbidden))
		return
	}

	observability.AddBookContext(ctx, bookRow.Book.ID.String(), bookRow.Book.Title)

	// CloudFront URL generation span
	urlCtx, urlSpan := observability.StartSpan(ctx, "cloudfront.generate_signed_url")
	readURL, err := utils.GeneratePresignedReadURL(cfg.CloudfrontUrl, bookRow.Book.S3Key, cfg.KeyPairID, int(cfg.PresignedUrlExpirySeconds), cfg.PrivateSignKey)
	if err != nil {
		observability.RecordError(urlCtx, err, "Failed to generate signed URL")
		urlSpan.End()
		c.Error(err)
		return
	}
	observability.SetSpanSuccess(urlCtx, "Signed URL generated")
	urlSpan.End()

	observability.RecordEvent(ctx, "book_accessed", map[string]interface{}{
		"user_id":      dbUser.ID,
		"book_id":      bookRow.Book.ID.String(),
		"book_title":   bookRow.Book.Title,
		"current_page": bookRow.CurrentPage,
		"total_pages":  bookRow.Book.TotalPages,
	})
	observability.SetSpanSuccess(ctx, "Book details retrieved")

	c.JSON(http.StatusOK, gin.H{"book": bookRow.Book, "current_page": bookRow.CurrentPage, "read_url": readURL})
}

// UpdateReadingProgressRequest represents the request body for updating reading progress
type UpdateReadingProgressRequest struct {
	CurrentPage int  `json:"current_page" binding:"required" example:"150"`
	TotalPages  *int `json:"total_pages,omitempty" example:"380"`
}

// @Summary Update reading progress
// @Description Update the current page and optionally total pages for a book
// @Tags Reading Progress
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param book_id path string true "Book ID" Format(uuid)
// @Param request body UpdateReadingProgressRequest true "Reading progress update"
// @Success 200 {object} repository.ReadingProgress
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books/{book_id}/reading-progress [patch]
func updateReadingProgressHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.update_reading_progress")
	defer span.End()

	start := time.Now()
	bookID := c.Param("book_id")

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	var req UpdateReadingProgressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid book ID format")
		c.Error(NewHttpError(err.Error(), "invalid book ID format", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx,
		attribute.String("book.id", uuidBookID.String()),
		attribute.Int("reading.current_page", req.CurrentPage),
	)

	if req.TotalPages != nil {
		observability.AddAttributes(ctx, attribute.Int("reading.total_pages", *req.TotalPages))
	}

	// Get book information
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_book_by_id", "books")
	bookRow, err := cfg.Queries.GetBookByID(dbCtx, uuidBookID)
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Book not found")
		c.Error(err)
		return
	}

	observability.AddBookContext(ctx, bookRow.Book.ID.String(), bookRow.Book.Title)

	// Update total pages if provided
	if req.TotalPages != nil {
		totalPages := int32(*req.TotalPages)
		if bookRow.Book.TotalPages != totalPages {
			updateCtx, updateSpan := observability.TraceDatabase(ctx, "update_book", "books")
			observability.AddEvent(updateCtx, "db_query_executed",
				attribute.String("sql.operation", "UPDATE_BOOK_TOTAL_PAGES"),
				attribute.String("db.table", "books"),
			)

			if _, err := cfg.Queries.UpdateBook(updateCtx, repository.UpdateBookParams{TotalPages: &totalPages, BookID: bookRow.Book.ID}); err != nil {
				observability.RecordError(updateCtx, err, "Failed to update book total pages")
				updateSpan.End()
				c.Error(err)
				return
			}
			observability.SetSpanSuccess(updateCtx, "Book total pages updated")
			updateSpan.End()
			bookRow.Book.TotalPages = totalPages
		}
	}

	// Calculate percentage
	var percentageComplete float64
	if bookRow.Book.TotalPages < 1 {
		percentageComplete = 0.0
	} else {
		percentageComplete = float64(req.CurrentPage) / float64(bookRow.Book.TotalPages) * 100
	}

	// Update reading progress
	progressCtx, progressSpan := observability.TraceDatabase(ctx, "update_reading_progress", "reading_progress")
	observability.AddEvent(progressCtx, "db_query_executed",
		attribute.String("sql.operation", "UPDATE_READING_PROGRESS"),
		attribute.String("db.table", "reading_progress"),
	)

	readingProgress, err := cfg.Queries.UpdateReadingProgress(progressCtx, repository.UpdateReadingProgressParams{
		CurrentPage:        int32(req.CurrentPage),
		PercentageComplete: percentageComplete,
		BookID:             uuidBookID,
		UserID:             dbUser.ID,
	})
	progressSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to update reading progress")
		c.Error(err)
		return
	}

	processingDuration := time.Since(start)

	observability.RecordEvent(ctx, "reading_progress_updated", map[string]interface{}{
		"user_id":            dbUser.ID,
		"book_id":            uuidBookID.String(),
		"book_title":         bookRow.Book.Title,
		"current_page":       req.CurrentPage,
		"total_pages":        bookRow.Book.TotalPages,
		"percentage":         percentageComplete,
		"processing_time_ms": processingDuration.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Reading progress updated")

	c.JSON(http.StatusOK, readingProgress)
}

// @Summary Get user's library
// @Description Retrieve all books owned by the authenticated user
// @Tags Books
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {array} repository.GetBooksByOwnerIDRow
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books [get]
func getLibraryHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.get_library")
	defer span.End()

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	// Database query span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_books_by_owner", "books")
	observability.AddEvent(dbCtx, "db_query_executed",
		attribute.String("sql.operation", "GET_BOOKS_BY_OWNER"),
		attribute.String("db.table", "books"),
	)

	books, err := cfg.Queries.GetBooksByOwnerID(dbCtx, dbUser.ID)
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to retrieve user library")
		c.Error(err)
		return
	}

	observability.RecordEvent(ctx, "library_accessed", map[string]interface{}{
		"user_id":    dbUser.ID,
		"book_count": len(books),
	})
	observability.SetSpanSuccess(ctx, "Library retrieved")

	c.JSON(http.StatusOK, books)
}

// @Summary Get shared public library
// @Description Retrieve publicly shared books from other users
// @Tags Books
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {array} repository.Book
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /public-books [get]
func getSharedLibraryHandler(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.get_shared_library")
	defer span.End()

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	// Database query span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_public_shared_books", "books")
	observability.AddEvent(dbCtx, "db_query_executed",
		attribute.String("sql.operation", "GET_PUBLIC_SHARED_BOOKS"),
		attribute.String("db.table", "books"),
	)

	bookRows, err := cfg.Queries.GetPublicSharedBooks(dbCtx, dbUser.ID)
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to retrieve shared library")
		c.Error(err)
		return
	}

	var books []repository.Book
	for _, bookRow := range bookRows {
		books = append(books, bookRow.Book)
	}

	observability.RecordEvent(ctx, "shared_library_accessed", map[string]interface{}{
		"user_id":    dbUser.ID,
		"book_count": len(books),
	})
	observability.SetSpanSuccess(ctx, "Shared library retrieved")

	c.JSON(http.StatusOK, books)
}

// WaitListEmailRequest represents the request body for waitlist registration
type WaitListEmailRequest struct {
	Email string `json:"email" binding:"required" example:"user@example.com"`
}

// @Summary Register email for waitlist
// @Description Register an email address for the application waitlist
// @Tags Waitlist
// @Accept json
// @Produce json
// @Param request body WaitListEmailRequest true "Email registration request"
// @Success 200 {object} WaitListEmailRequest
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /wait-list [post]
func registerWaitListEmail(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.register_waitlist_email")
	defer span.End()

	var req WaitListEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		observability.RecordEvent(ctx, "waitlist_registration_failed", map[string]interface{}{
			"error":  "validation_failed",
			"reason": err.Error(),
		})
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx, attribute.String("email", req.Email))

	// Database insertion span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "register_waitlist_email", "waitlist")
	observability.AddEvent(dbCtx, "db_query_executed",
		attribute.String("sql.operation", "REGISTER_WAITLIST_EMAIL"),
		attribute.String("db.table", "waitlist"),
	)

	if _, err := cfg.Queries.RegisterWaitListEmail(dbCtx, req.Email); err != nil {
		dbSpan.End()
		if strings.Contains(err.Error(), "unique constraint") {
			observability.RecordEvent(ctx, "waitlist_registration_failed", map[string]interface{}{
				"email": req.Email,
				"error": "duplicate_email",
			})
			c.Error(NewHttpError("Email already registered", "duplicate email entry", http.StatusConflict))
			return
		}
		observability.RecordError(ctx, err, "Database error during waitlist registration")
		c.Error(err)
		return
	}
	observability.SetSpanSuccess(dbCtx, "Email registered to waitlist")
	dbSpan.End()

	observability.RecordEvent(ctx, "waitlist_email_registered", map[string]interface{}{
		"email": req.Email,
	})
	observability.SetSpanSuccess(ctx, "Email registered successfully")

	c.JSON(http.StatusOK, req)
}

// NoteWithReference represents a note with its PDF reference
type NoteWithReference struct {
	Note         repository.Note          `json:"note"`
	PDFReference *repository.PdfReference `json:"pdf_reference"`
}

func convertNotesToNotesWithReferences(notes []repository.GetNotesForBookUserRow) []NoteWithReference {
	var notesWithReferences []NoteWithReference
	for _, noteRow := range notes {
		noteWithReference := NoteWithReference{Note: noteRow.Note}
		if noteRow.Note.ReferenceType != nil && *noteRow.Note.ReferenceType == "pdf" && noteRow.PageNumber != nil {
			noteWithReference.PDFReference = &repository.PdfReference{ID: *noteRow.ID, PageNumber: *noteRow.PageNumber, XStart: *noteRow.XStart, XEnd: noteRow.XEnd, YStart: *noteRow.YStart, YEnd: noteRow.YEnd}
		}
		notesWithReferences = append(notesWithReferences, noteWithReference)
	}

	return notesWithReferences
}

// PDFReferenceRequest represents PDF coordinate reference for a note
type PDFReferenceRequest struct {
	PageNumber int      `json:"page_number" binding:"required" example:"1"`
	XStart     float32  `json:"x_start" binding:"required" example:"100.5"`
	XEnd       *float32 `json:"x_end,omitempty" example:"200.5"`
	YStart     float32  `json:"y_start" binding:"required" example:"300.5"`
	YEnd       *float32 `json:"y_end,omitempty" example:"400.5"`
}

func (req *PDFReferenceRequest) Validate(book *repository.Book) error {
	if book.TotalPages != 0 && req.PageNumber > int(book.TotalPages) {
		return NewHttpError(fmt.Sprintf("bigger than %d", book.TotalPages), "", http.StatusBadRequest)
	}

	return nil
}

// CreateNoteRequest represents the request body for creating a note
type CreateNoteRequest struct {
	Content      string               `json:"content" binding:"required" example:"This is an important point"`
	Color        *string              `json:"color,omitempty" example:"#ffff00"`
	PDFReference *PDFReferenceRequest `json:"pdf_reference,omitempty"`
}

// @Summary Create a note
// @Description Create a new note for a specific book, optionally with PDF coordinates
// @Tags Notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param book_id path string true "Book ID" Format(uuid)
// @Param request body CreateNoteRequest true "Note creation request"
// @Success 201 {object} NoteWithReference
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books/{book_id}/notes [post]
func createNote(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.create_note")
	defer span.End()

	start := time.Now()
	bookID := c.Param("book_id")

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	var req CreateNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid book ID format")
		c.Error(err)
		return
	}

	observability.AddAttributes(ctx,
		attribute.String("book.id", uuidBookID.String()),
		attribute.String("note.content_length", fmt.Sprintf("%d", len(req.Content))),
	)

	if req.PDFReference != nil {
		observability.AddAttributes(ctx,
			attribute.Int("note.page_number", req.PDFReference.PageNumber),
			attribute.String("note.reference_type", "pdf"),
		)
	}

	// Get book information
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_book_by_id", "books")
	bookRow, err := cfg.Queries.GetBookByID(dbCtx, uuidBookID)
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Book not found")
		c.Error(err)
		return
	}

	observability.AddBookContext(ctx, bookRow.Book.ID.String(), bookRow.Book.Title)

	var pdfReferenceUUID *uuid.UUID
	var referenceType string

	// Create PDF reference if provided
	if req.PDFReference != nil {
		if err := req.PDFReference.Validate(&bookRow.Book); err != nil {
			observability.RecordError(ctx, err, "PDF reference validation failed")
			c.Error(err)
			return
		}

		refCtx, refSpan := observability.TraceDatabase(ctx, "create_pdf_reference", "pdf_references")
		observability.AddEvent(refCtx, "db_query_executed",
			attribute.String("sql.operation", "CREATE_PDF_REFERENCE"),
			attribute.String("db.table", "pdf_references"),
		)

		pdfReference, err := cfg.Queries.CreatePDFReference(c, repository.CreatePDFReferenceParams{
			ID:         uuid.New(),
			PageNumber: int16(req.PDFReference.PageNumber),
			XStart:     req.PDFReference.XStart,
			XEnd:       req.PDFReference.XEnd,
			YStart:     req.PDFReference.YStart,
			YEnd:       req.PDFReference.YEnd,
		})
		refSpan.End()

		if err != nil {
			observability.RecordError(ctx, err, "Failed to create PDF reference")
			c.Error(err)
			return
		}

		pdfReferenceUUID = &pdfReference.ID
		referenceType = "pdf"
	}

	// Create note
	noteCtx, noteSpan := observability.TraceDatabase(ctx, "create_note", "notes")
	observability.AddEvent(noteCtx, "db_query_executed",
		attribute.String("sql.operation", "CREATE_NOTE"),
		attribute.String("db.table", "notes"),
	)

	note, err := cfg.Queries.CreateNote(c, repository.CreateNoteParams{
		ID:                 uuid.New(),
		BookID:             bookRow.Book.ID,
		UserID:             dbUser.ID,
		ReferenceDataPdfID: pdfReferenceUUID,
		Content:            &req.Content,
		Color:              req.Color,
		ReferenceType:      &referenceType,
	})
	noteSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to create note")
		c.Error(err)
		return
	}

	noteWithReference := NoteWithReference{Note: note}
	if pdfReferenceUUID != nil {
		noteWithReference.PDFReference = &repository.PdfReference{
			ID:         *pdfReferenceUUID,
			PageNumber: int16(req.PDFReference.PageNumber),
			XStart:     req.PDFReference.XStart,
			XEnd:       req.PDFReference.XEnd,
			YStart:     req.PDFReference.YStart,
			YEnd:       req.PDFReference.YEnd,
		}
	}

	processingDuration := time.Since(start)

	observability.RecordEvent(ctx, "note_created", map[string]interface{}{
		"user_id":            dbUser.ID,
		"book_id":            bookRow.Book.ID.String(),
		"note_id":            note.ID.String(),
		"book_title":         bookRow.Book.Title,
		"has_pdf_reference":  pdfReferenceUUID != nil,
		"content_length":     len(req.Content),
		"processing_time_ms": processingDuration.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Note created successfully")

	c.JSON(http.StatusCreated, noteWithReference)
}

// @Summary Get notes for a book
// @Description Retrieve all notes for a specific book by the authenticated user
// @Tags Notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param book_id path string true "Book ID" Format(uuid)
// @Success 200 {array} NoteWithReference
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /books/{book_id}/notes [get]
func getNotes(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.get_notes")
	defer span.End()

	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid book ID format")
		observability.RecordEvent(ctx, "notes_access_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"book_id": bookID,
			"error":   "invalid_uuid",
		})
		c.Error(NewHttpError(err.Error(), "invalid book ID format", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx, attribute.String("book.id", uuidBookID.String()))

	// Database query span
	dbCtx, dbSpan := observability.TraceDatabase(ctx, "get_notes_for_book_user", "notes")
	observability.AddEvent(dbCtx, "db_query_executed",
		attribute.String("sql.operation", "GET_NOTES_FOR_BOOK_USER"),
		attribute.String("db.table", "notes"),
	)

	notes, err := cfg.Queries.GetNotesForBookUser(dbCtx, repository.GetNotesForBookUserParams{BookID: uuidBookID, UserID: dbUser.ID})
	dbSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to retrieve notes")
		c.Error(err)
		return
	}

	notesWithReferences := convertNotesToNotesWithReferences(notes)

	observability.RecordEvent(ctx, "notes_accessed", map[string]interface{}{
		"user_id":    dbUser.ID,
		"book_id":    uuidBookID.String(),
		"note_count": len(notesWithReferences),
	})
	observability.SetSpanSuccess(ctx, "Notes retrieved")

	c.JSON(http.StatusOK, notesWithReferences)
}

// @Summary Delete a note
// @Description Delete a specific note by ID
// @Tags Notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param note_id path string true "Note ID" Format(uuid)
// @Success 200 {object} SuccessResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /notes/{note_id} [delete]
func deleteNote(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.delete_note")
	defer span.End()

	start := time.Now()
	noteID := c.Param("note_id")

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	uuidNoteID, err := uuid.Parse(noteID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid note ID format")
		observability.RecordEvent(ctx, "note_deletion_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"note_id": noteID,
			"error":   "invalid_uuid",
		})
		c.Error(NewHttpError(err.Error(), "invalid note ID format", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx, attribute.String("note.id", uuidNoteID.String()))

	// First verify the note belongs to the user
	getCtx, getSpan := observability.TraceDatabase(ctx, "get_note_by_id", "notes")
	observability.AddEvent(getCtx, "db_query_executed",
		attribute.String("sql.operation", "GET_NOTE_BY_ID"),
		attribute.String("db.table", "notes"),
	)

	noteRow, err := cfg.Queries.GetNoteByID(getCtx, uuidNoteID)
	getSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Note not found")
		observability.RecordEvent(ctx, "note_deletion_failed", map[string]interface{}{
			"user_id": dbUser.ID,
			"note_id": uuidNoteID.String(),
			"error":   "not_found",
		})
		c.Error(err)
		return
	}

	if noteRow.Note.UserID != dbUser.ID {
		observability.RecordEvent(ctx, "note_deletion_denied", map[string]interface{}{
			"user_id":       dbUser.ID,
			"note_id":       uuidNoteID.String(),
			"note_owner_id": noteRow.Note.UserID,
			"error":         "access_denied",
		})
		c.Error(NewHttpError("not authorized to delete this note", "access denied", http.StatusForbidden))
		return
	}

	observability.AddAttributes(ctx,
		attribute.String("note.book_id", noteRow.Note.BookID.String()),
	)

	// Delete the note
	deleteCtx, deleteSpan := observability.TraceDatabase(ctx, "delete_note", "notes")
	observability.AddEvent(deleteCtx, "db_query_executed",
		attribute.String("sql.operation", "DELETE_NOTE"),
		attribute.String("db.table", "notes"),
	)

	if err := cfg.Queries.DeleteNote(deleteCtx, uuidNoteID); err != nil {
		observability.RecordError(deleteCtx, err, "Failed to delete note")
		deleteSpan.End()
		c.Error(err)
		return
	}
	observability.SetSpanSuccess(deleteCtx, "Note deleted")
	deleteSpan.End()

	processingDuration := time.Since(start)

	observability.RecordEvent(ctx, "note_deleted", map[string]interface{}{
		"user_id":            dbUser.ID,
		"note_id":            uuidNoteID.String(),
		"book_id":            noteRow.Note.BookID.String(),
		"processing_time_ms": processingDuration.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Note deleted successfully")

	c.JSON(http.StatusOK, SuccessResponse{
		Message: "Note deleted successfully",
	})
}

// UpdateNoteRequest represents the request body for updating a note
type UpdateNoteRequest struct {
	Content *string `json:"content,omitempty" example:"Updated note content"`
	Color   *string `json:"color,omitempty" example:"#ff0000"`
}

// @Summary Update a note
// @Description Update the content and/or color of a specific note
// @Tags Notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param note_id path string true "Note ID" Format(uuid)
// @Param request body UpdateNoteRequest true "Note update request"
// @Success 200 {object} repository.Note
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /notes/{note_id} [patch]
func updateNote(c *gin.Context) {
	ctx, span := observability.TraceOperation(c, "handler.update_note")
	defer span.End()

	start := time.Now()
	noteID := c.Param("note_id")

	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		observability.RecordError(ctx, err, "Authentication failed")
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	observability.AddUserContext(ctx, dbUser.ID, "authenticated")

	uuidNoteID, err := uuid.Parse(noteID)
	if err != nil {
		observability.RecordError(ctx, err, "Invalid note ID format")
		c.Error(NewHttpError(err.Error(), "invalid note ID format", http.StatusBadRequest))
		return
	}

	observability.AddAttributes(ctx, attribute.String("note.id", uuidNoteID.String()))

	// Get current note
	getCtx, getSpan := observability.TraceDatabase(ctx, "get_note_by_id", "notes")
	noteRow, err := cfg.Queries.GetNoteByID(getCtx, uuidNoteID)
	getSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Note not found")
		c.Error(err)
		return
	}

	if noteRow.Note.UserID != dbUser.ID {
		observability.RecordEvent(ctx, "note_update_denied", map[string]interface{}{
			"user_id":       dbUser.ID,
			"note_id":       uuidNoteID.String(),
			"note_owner_id": noteRow.Note.UserID,
			"error":         "access_denied",
		})
		c.Error(NewHttpError("not an owner", "access denied", http.StatusForbidden))
		return
	}

	observability.AddAttributes(ctx,
		attribute.String("note.book_id", noteRow.Note.BookID.String()),
	)

	var req UpdateNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		observability.RecordError(ctx, err, "Request validation failed")
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	var updateNoteParams repository.UpdateNoteParams
	updateNoteParams.ID = uuidNoteID
	updateNoteParams.Color = noteRow.Note.Color
	updateNoteParams.Content = noteRow.Note.Content

	fieldsChanged := make([]string, 0)
	if req.Content != nil {
		updateNoteParams.Content = req.Content
		fieldsChanged = append(fieldsChanged, "content")
	}

	if req.Color != nil {
		updateNoteParams.Color = req.Color
		fieldsChanged = append(fieldsChanged, "color")
	}

	// Update note
	updateCtx, updateSpan := observability.TraceDatabase(ctx, "update_note", "notes")
	observability.AddEvent(updateCtx, "db_query_executed",
		attribute.String("sql.operation", "UPDATE_NOTE"),
		attribute.String("db.table", "notes"),
	)

	noteRow.Note, err = cfg.Queries.UpdateNote(updateCtx, updateNoteParams)
	updateSpan.End()

	if err != nil {
		observability.RecordError(ctx, err, "Failed to update note")
		c.Error(err)
		return
	}

	processingDuration := time.Since(start)

	observability.RecordEvent(ctx, "note_updated", map[string]interface{}{
		"user_id":            dbUser.ID,
		"note_id":            uuidNoteID.String(),
		"book_id":            noteRow.Note.BookID.String(),
		"fields_changed":     strings.Join(fieldsChanged, ","),
		"processing_time_ms": processingDuration.Milliseconds(),
	})
	observability.SetSpanSuccess(ctx, "Note updated successfully")

	c.JSON(http.StatusOK, noteRow.Note)
}
