package main

import (
	"errors"
	"log"
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
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	c.JSON(http.StatusOK, dbUser)
}

type UploadBookRequest struct {
	Name string `json:"name" binding:"required"`
}

func generateUploadUrlHandler(c *gin.Context) {
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	var req UploadBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !strings.HasSuffix(req.Name, ".pdf") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid book format"})
		return
	}

	key := dbUser.ID + "/" + req.Name
	url, err := utils.GeneratePresignedUploadURL(c, cfg.S3Client, cfg.BucketName, key, cfg.PresignedUrlExpirySeconds)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	var req ConfirmBookUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !utils.KeyExists(c, cfg.S3Client, cfg.BucketName, req.S3Key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "s3 key doesn't exists"})
		return
	}

	tx, err := cfg.DBPool.Begin(c)
	defer tx.Rollback(c)

	localQueries := repository.New(tx)

	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	book, err := localQueries.CreateBook(c, repository.CreateBookParams{ID: uuid.New(), OwnerID: dbUser.ID, S3Key: req.S3Key, TotalPages: int32(req.TotalPages), Title: req.Title})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == setup.UniqueViolationCode {
			c.JSON(http.StatusConflict, gin.H{"error": "Book already exists"})
			return
		}

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if _, err := localQueries.CreateReadingProgress(c, repository.CreateReadingProgressParams{BookID: book.ID, UserID: dbUser.ID}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "error while creating reading progress for a book" + err.Error()})
		return
	}
	if err := tx.Commit(c); err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	go func() {
		thumbnailWebpKey := strings.Replace(book.S3Key, ".pdf", ".webp", -1)
		log.Println("generating thumbnail for book: ", book.S3Key, ", thumbnailKey: ", thumbnailWebpKey)
		localPath, err := utils.DownloadFileToTmp(c, cfg.S3Client, cfg.BucketName, book.S3Key, "/tmp")
		if err != nil {
			log.Println("error while downloading s3key to tmp: ", err.Error())
			return
		}

		thumbnailPath, err := thumbnailgenerator.GeneratePdfThumbnail(localPath, "/tmp", dbUser.ID)
		if err != nil {
			log.Println("error while getting pdf thumbnail: ", err.Error())
			return
		}

		webpPath := strings.Replace(thumbnailPath, ".jpg", ".webp", -1)
		if err := thumbnailgenerator.ConvertToWebp(thumbnailPath, webpPath); err != nil {
			log.Println("error while getting pdf thumbnail: ", err.Error())
			return
		}

		if err := utils.UploadFileToS3(c, cfg.S3Client, webpPath, cfg.BucketName, thumbnailWebpKey); err != nil {
			log.Println("error while uploading thumbnail to s3: ", err.Error())
			return
		}

		if _, err := cfg.Queries.UpdateBook(c, repository.UpdateBookParams{ThumbnailS3Key: &thumbnailWebpKey, BookID: book.ID}); err != nil {
			log.Println("error while updating DB book", err.Error())
			return
		}
	}()

	c.JSON(http.StatusOK, book)
}

func getBookHandler(c *gin.Context) {
	bookID := c.Param("book_id")
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": bookID + " is not a valid uuid"})
		return
	}

	bookRow, err := cfg.Queries.GetBookByID(c, uuidBookID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		} else {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if bookRow.Book.OwnerID != dbUser.ID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "not an owner"})
		return
	}
	readURL, err := utils.GeneratePresignedReadURL(cfg.CloudfrontUrl, bookRow.Book.S3Key, cfg.KeyPairID, int(cfg.PresignedUrlExpirySeconds), cfg.PrivateSignKey)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		log.Println(err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	var req UpdateReadingProgressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	uuidBookID, err := uuid.Parse(bookID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	bookRow, err := cfg.Queries.GetBookByID(c, uuidBookID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			log.Println(err)
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		} else {
			log.Println(err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if req.TotalPages != nil {
		totalPages := int32(*req.TotalPages)
		if bookRow.Book.TotalPages != totalPages {
			if _, err := cfg.Queries.UpdateBook(c, repository.UpdateBookParams{TotalPages: &totalPages, BookID: bookRow.Book.ID}); err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		log.Println(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, readingProgress)
}

func getLibraryHandler(c *gin.Context) {
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	books, err := cfg.Queries.GetBooksByOwnerID(c, dbUser.ID)

	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, books)
}

func getSharedLibraryHandler(c *gin.Context) {
	dbUser, err := auth.GetDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	bookRows, err := cfg.Queries.GetPublicSharedBooks(c, dbUser.ID)

	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := cfg.Queries.RegisterWaitListEmail(c, req.Email); err != nil {
    if strings.Contains(err.Error(), "unique constraint") {
      c.AbortWithStatus(http.StatusConflict)
      return
    }
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, req)
}
