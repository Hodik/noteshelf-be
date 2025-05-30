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

func meHandler(c *gin.Context) {
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.Error(NewHttpError(err.Error(), "user authentication failed", http.StatusUnauthorized))
		return
	}

	c.JSON(http.StatusOK, dbUser)
}

type UploadBookRequest struct {
	Name string `json:"name" binding:"required"`
}

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

type ConfirmBookUploadRequest struct {
	Title      string `json:"title" binding:"required"`
	Author     string `json:"author"`
	S3Key      string `json:"s3_key"`
	TotalPages int    `json:"total_pages"`
}

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

type UpdateReadingProgressRequest struct {
	CurrentPage int  `json:"current_page" binding:"required"`
	TotalPages  *int `json:"total_pages"`
}

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

type WaitListEmailRequest struct {
	Email string `json:"email" binding:"required"`
}

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

type PDFReferenceRequest struct {
	PageNumber int      `json:"page_number" binding:"required"`
	XStart     float32  `json:"x_start" binding:"required"`
	XEnd       *float32 `json:"x_end"`
	YStart     float32  `json:"y_start" binding:"required"`
	YEnd       *float32 `json:"y_end"`
}

func (req *PDFReferenceRequest) Validate(book *repository.Book) error {
	if book.TotalPages != 0 && req.PageNumber > int(book.TotalPages) {
		return NewHttpError(fmt.Sprintf("bigger than %d", book.TotalPages), "", http.StatusBadRequest)
	}

	return nil
}

type CreateNoteRequest struct {
	Content string  `json:"content" binding:"required"`
	Color   *string `json:"color"`

	PDFReference *PDFReferenceRequest `json:"pdf_reference"`
}

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
	}

	note, err := cfg.Queries.CreateNote(c, repository.CreateNoteParams{BookID: bookRow.Book.ID, UserID: dbUser.ID, ReferenceDataPdfID: pdfReferenceUUID, Content: &req.Content, Color: req.Color})
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusCreated, note)
}

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

	c.JSON(http.StatusOK, notes)
}

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

	note, err := cfg.Queries.GetNoteByID(c, uuidNoteID)
	if err != nil {
		c.Error(err)
		return
	}

	if note.UserID != dbUser.ID {
		c.Error(NewHttpError("not an owner", "access denied", http.StatusForbidden))
		return
	}

	if err := cfg.Queries.DeleteNote(c, uuidNoteID); err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Note deleted successfully"})
}

type UpdateNoteRequest struct {
	Content *string `json:"content"`
	Color   *string `json:"color"`
}

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

	note, err := cfg.Queries.GetNoteByID(c, uuidNoteID)
	if err != nil {
		c.Error(err)
		return
	}

	if note.UserID != dbUser.ID {
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
	updateNoteParams.Color = note.Color
	updateNoteParams.Content = note.Content

	if req.Content != nil {
		updateNoteParams.Content = req.Content
	}

	if req.Color != nil {
		updateNoteParams.Color = req.Color
	}

	note, err = cfg.Queries.UpdateNote(c, updateNoteParams)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, note)
}
