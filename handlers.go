package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Hodik/noteshelf-be.git/auth"
	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/Hodik/noteshelf-be.git/setup"
	"github.com/Hodik/noteshelf-be.git/thumbnailgenerator"
	"github.com/Hodik/noteshelf-be.git/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

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
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	var req UploadBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}
	if !strings.HasSuffix(req.Name, ".pdf") {
		c.Error(NewHttpError("invalid book format", "file must be a PDF", http.StatusBadRequest))
		return
	}

	key := dbUser.ID + "/" + req.Name
	url, err := utils.GeneratePresignedUploadURL(c, cfg.S3Client, cfg.BucketName, key, cfg.PresignedUrlExpirySeconds)

	if err != nil {
		c.Error(err)
		return
	}

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
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	var req ConfirmBookUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	if !utils.KeyExists(c, cfg.S3Client, cfg.BucketName, req.S3Key) {
		c.Error(NewHttpError("s3 key doesn't exist", "uploaded file not found", http.StatusBadRequest))
		return
	}

	tx, err := cfg.DBPool.Begin(c)
	defer tx.Rollback(c)

	localQueries := repository.New(tx)

	if err != nil {
		c.Error(err)
		return
	}

	book, err := localQueries.CreateBook(c, repository.CreateBookParams{ID: uuid.New(), OwnerID: dbUser.ID, S3Key: req.S3Key, TotalPages: int32(req.TotalPages), Title: req.Title})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == setup.UniqueViolationCode {
			c.Error(NewHttpError("Book already exists", "duplicate book entry", http.StatusConflict))
			return
		}

		c.Error(err)
		return
	}

	if _, err := localQueries.CreateReadingProgress(c, repository.CreateReadingProgressParams{BookID: book.ID, UserID: dbUser.ID}); err != nil {
		c.Error(NewHttpError("error while creating reading progress for a book: "+err.Error(), "reading progress creation failed", http.StatusInternalServerError))
		return
	}
	if err := tx.Commit(c); err != nil {
		c.Error(err)
		return
	}

	go func() {
		thumbnailWebpKey := strings.Replace(book.S3Key, ".pdf", ".webp", -1)

		slog.Info("Starting thumbnail generation",
			"book_id", book.ID,
			"user_id", dbUser.ID,
			"s3_key", book.S3Key,
			"thumbnail_key", thumbnailWebpKey,
		)

		localPath, err := utils.DownloadFileToTmp(c, cfg.S3Client, cfg.BucketName, book.S3Key, "/tmp")
		if err != nil {
			slog.Error("Failed to download PDF from S3",
				"error", err.Error(),
				"book_id", book.ID,
				"user_id", dbUser.ID,
				"s3_key", book.S3Key,
			)
			return
		}

		thumbnailPath, err := thumbnailgenerator.GeneratePdfThumbnail(localPath, "/tmp", dbUser.ID)
		if err != nil {
			slog.Error("Failed to generate PDF thumbnail",
				"error", err.Error(),
				"book_id", book.ID,
				"user_id", dbUser.ID,
				"local_path", localPath,
			)
			return
		}

		webpPath := strings.Replace(thumbnailPath, ".jpg", ".webp", -1)
		if err := thumbnailgenerator.ConvertToWebp(thumbnailPath, webpPath); err != nil {
			slog.Error("Failed to convert thumbnail to WebP",
				"error", err.Error(),
				"book_id", book.ID,
				"user_id", dbUser.ID,
				"jpeg_path", thumbnailPath,
				"webp_path", webpPath,
			)
			return
		}

		if err := utils.UploadFileToS3(c, cfg.S3Client, webpPath, cfg.BucketName, thumbnailWebpKey); err != nil {
			slog.Error("Failed to upload thumbnail to S3",
				"error", err.Error(),
				"book_id", book.ID,
				"user_id", dbUser.ID,
				"thumbnail_key", thumbnailWebpKey,
				"local_path", webpPath,
			)
			return
		}

		if _, err := cfg.Queries.UpdateBook(c, repository.UpdateBookParams{ThumbnailS3Key: &thumbnailWebpKey, BookID: book.ID}); err != nil {
			slog.Error("Failed to update book with thumbnail",
				"error", err.Error(),
				"book_id", book.ID,
				"user_id", dbUser.ID,
				"thumbnail_key", thumbnailWebpKey,
			)
			return
		}

		slog.Info("Thumbnail generation completed successfully",
			"book_id", book.ID,
			"user_id", dbUser.ID,
			"thumbnail_key", thumbnailWebpKey,
		)
	}()

	c.JSON(http.StatusOK, book)
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
	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.Error(NewHttpError(bookID+" is not a valid uuid", "invalid book ID format", http.StatusBadRequest))
		return
	}

	bookRow, err := cfg.Queries.GetBookByID(c, uuidBookID)
	if err != nil {
		c.Error(err)
		return
	}

	if bookRow.Book.OwnerID != dbUser.ID {
		c.Error(NewHttpError("not an owner", "access denied", http.StatusForbidden))
		return
	}
	readURL, err := utils.GeneratePresignedReadURL(cfg.CloudfrontUrl, bookRow.Book.S3Key, cfg.KeyPairID, int(cfg.PresignedUrlExpirySeconds), cfg.PrivateSignKey)
	if err != nil {
		c.Error(err)
		return
	}

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
	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	var req UpdateReadingProgressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "invalid book ID format", http.StatusBadRequest))
		return
	}

	bookRow, err := cfg.Queries.GetBookByID(c, uuidBookID)
	if err != nil {
		c.Error(err)
		return
	}

	if req.TotalPages != nil {
		totalPages := int32(*req.TotalPages)
		if bookRow.Book.TotalPages != totalPages {
			if _, err := cfg.Queries.UpdateBook(c, repository.UpdateBookParams{TotalPages: &totalPages, BookID: bookRow.Book.ID}); err != nil {
				c.Error(err)
				return
			}
			bookRow.Book.TotalPages = totalPages
		}
	}

	var percentageComplete float64
	if bookRow.Book.TotalPages < 1 {
		percentageComplete = 0.0
	} else {
		percentageComplete = float64(req.CurrentPage) / float64(bookRow.Book.TotalPages) * 100
	}

	readingProgress, err := cfg.Queries.UpdateReadingProgress(c, repository.UpdateReadingProgressParams{CurrentPage: int32(req.CurrentPage), PercentageComplete: percentageComplete, BookID: uuidBookID, UserID: dbUser.ID})

	if err != nil {
		c.Error(err)
		return
	}

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
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	books, err := cfg.Queries.GetBooksByOwnerID(c, dbUser.ID)

	if err != nil {
		c.Error(err)
		return
	}

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
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	bookRows, err := cfg.Queries.GetPublicSharedBooks(c, dbUser.ID)

	if err != nil {
		c.Error(err)
		return
	}

	var books []repository.Book
	for _, bookRow := range bookRows {
		books = append(books, bookRow.Book)
	}

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
	var req WaitListEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}
	if _, err := cfg.Queries.RegisterWaitListEmail(c, req.Email); err != nil {
		if strings.Contains(err.Error(), "unique constraint") {
			c.Error(NewHttpError("Email already registered", "duplicate email entry", http.StatusConflict))
			return
		}
		c.Error(err)
		return
	}
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
	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	var req CreateNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.Error(err)
		return
	}

	bookRow, err := cfg.Queries.GetBookByID(c, uuidBookID)
	if err != nil {
		c.Error(err)
		return
	}

	var pdfReferenceUUID *uuid.UUID
	var referenceType string
	if req.PDFReference != nil {
		if err := req.PDFReference.Validate(&bookRow.Book); err != nil {
			c.Error(err)
			return
		}

		pdfReference, err := cfg.Queries.CreatePDFReference(c, repository.CreatePDFReferenceParams{ID: uuid.New(), PageNumber: int16(req.PDFReference.PageNumber), XStart: req.PDFReference.XStart, XEnd: req.PDFReference.XEnd, YStart: req.PDFReference.YStart, YEnd: req.PDFReference.YEnd})
		if err != nil {
			c.Error(err)
			return
		}

		pdfReferenceUUID = &pdfReference.ID
		referenceType = "pdf"
	}

	note, err := cfg.Queries.CreateNote(c, repository.CreateNoteParams{ID: uuid.New(), BookID: bookRow.Book.ID, UserID: dbUser.ID, ReferenceDataPdfID: pdfReferenceUUID, Content: &req.Content, Color: req.Color, ReferenceType: &referenceType})
	if err != nil {
		c.Error(err)
		return
	}

	noteWithReference := NoteWithReference{Note: note}
	if pdfReferenceUUID != nil {
		noteWithReference.PDFReference = &repository.PdfReference{ID: *pdfReferenceUUID, PageNumber: int16(req.PDFReference.PageNumber), XStart: req.PDFReference.XStart, XEnd: req.PDFReference.XEnd, YStart: req.PDFReference.YStart, YEnd: req.PDFReference.YEnd}
	}

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
	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "invalid book ID format", http.StatusBadRequest))
		return
	}
	notes, err := cfg.Queries.GetNotesForBookUser(c, repository.GetNotesForBookUserParams{BookID: uuidBookID, UserID: dbUser.ID})
	if err != nil {
		c.Error(err)
		return
	}

	notesWithReferences := convertNotesToNotesWithReferences(notes)
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
	noteID := c.Param("note_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	uuidNoteID, err := uuid.Parse(noteID)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "invalid note ID format", http.StatusBadRequest))
		return
	}

	// First verify the note belongs to the user
	noteRow, err := cfg.Queries.GetNoteByID(c, uuidNoteID)
	if err != nil {
		c.Error(err)
		return
	}

	if noteRow.Note.UserID != dbUser.ID {
		c.Error(NewHttpError("not authorized to delete this note", "access denied", http.StatusForbidden))
		return
	}

	if err := cfg.Queries.DeleteNote(c, uuidNoteID); err != nil {
		c.Error(err)
		return
	}

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
	noteID := c.Param("note_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	uuidNoteID, err := uuid.Parse(noteID)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "invalid note ID format", http.StatusBadRequest))
		return
	}

	noteRow, err := cfg.Queries.GetNoteByID(c, uuidNoteID)
	if err != nil {
		c.Error(err)
		return
	}

	if noteRow.Note.UserID != dbUser.ID {
		c.Error(NewHttpError("not an owner", "access denied", http.StatusForbidden))
		return
	}

	var req UpdateNoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(NewHttpError(err.Error(), "invalid request body", http.StatusBadRequest))
		return
	}

	var updateNoteParams repository.UpdateNoteParams
	updateNoteParams.ID = uuidNoteID
	updateNoteParams.Color = noteRow.Note.Color
	updateNoteParams.Content = noteRow.Note.Content

	if req.Content != nil {
		updateNoteParams.Content = req.Content
	}

	if req.Color != nil {
		updateNoteParams.Color = req.Color
	}

	noteRow.Note, err = cfg.Queries.UpdateNote(c, updateNoteParams)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, noteRow.Note)
}
