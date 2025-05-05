package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"log"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var dbPool *pgxpool.Pool
var queries *repository.Queries
var s3Client *s3.Client
var bucketName string
var presignedUrlExpirySeconds int64

func initS3Client(ctx context.Context) (*s3.Client, error) {
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	region := os.Getenv("AWS_REGION")

	credProvider := credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credProvider),
	)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(cfg), nil
}

func generatePresignedUploadURL(ctx context.Context, s3Client *s3.Client, bucketName, objectKey string, expirySeconds int64) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	request, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expirySeconds) * time.Second
	})

	if err != nil {
		return "", err
	}

	return request.URL, nil
}

func getPrimaryPhone(usr *clerk.User) *string {
	if usr.PrimaryPhoneNumberID == nil {
		return nil
	} else {
		for _, phone := range usr.PhoneNumbers {
			if phone != nil {
				if phone.ID == *usr.PrimaryPhoneNumberID {
					return &phone.PhoneNumber
				}
			}
		}
	}

	return nil
}

func getPrimaryEmail(usr *clerk.User) *string {
	if usr.PrimaryEmailAddressID == nil {
		return nil
	} else {
		for _, email := range usr.EmailAddresses {
			if email != nil {
				if email.ID == *usr.PrimaryEmailAddressID {
					return &email.EmailAddress
				}
			}
		}
	}

	return nil
}

func setupDB() error {
	connString := os.Getenv("POSTGRESQL_URL")
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		log.Fatalf("Unable to parse connection string: %v", err)
	}

	config.MaxConns = 1
	dbPool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	queries = repository.New(dbPool)
	return nil
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authheader := c.GetHeader("Authorization")
		if authheader == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authheader, " ")

		if parts[0] != "Bearer" || len(parts) != 2 {
			c.AbortWithStatus(http.StatusNotAcceptable)
			return
		}

		claims, err := jwt.Verify(c, &jwt.VerifyParams{
			Token: parts[1],
		})
		if err != nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		usr, err := user.Get(c, claims.Subject)
		c.Set("user", usr)

		dbUser, err := syncUser(c, usr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, &gin.H{"detail": err.Error()})
			return
		}
		c.Set("dbUser", dbUser)
		c.Next()
	}
}

func syncUser(ctx context.Context, usr *clerk.User) (*repository.User, error) {
	user, err := queries.GetUserById(ctx, usr.ID)

	if err == nil {
		return &user, nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		phoneNumber := getPrimaryPhone(usr)
		email := getPrimaryEmail(usr)
		if email == nil {
			return nil, errors.New("email not found in clerk user")
		}

		user, err = queries.CreateUser(ctx, repository.CreateUserParams{ID: usr.ID, FirstName: usr.FirstName, LastName: usr.LastName, Username: usr.Username, Email: *email, Phone: phoneNumber})
		if err != nil {
			return nil, err
		} else {
			return &user, nil

		}
	}
	return nil, err
}

func getClerkUserFromRequest(c *gin.Context) (*clerk.User, error) {
	user, exists := c.Get("user")

	if !exists {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not found in context")
	}

	clerkUser, ok := user.(*clerk.User)
	if !ok {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not clerk user")
	}

	return clerkUser, nil
}

func getDBUserFromRequest(c *gin.Context) (*repository.User, error) {
	user, exists := c.Get("dbUser")

	if !exists {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not found in context")
	}

	dbUser, ok := user.(*repository.User)
	if !ok {
		c.AbortWithStatus(http.StatusInternalServerError)
		return nil, errors.New("user is not clerk user")
	}

	return dbUser, nil
}

func meHandler(c *gin.Context) {
	dbUser, err := getDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	c.JSON(http.StatusOK, dbUser)
}

type UploadBookRequest struct {
	Name string `json:"name" binding:"required"`
}

func generateUploadUrlHandler(c *gin.Context) {
	dbUser, err := getDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	var req UploadBookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key := dbUser.ID + "/" + req.Name
	url, err := generatePresignedUploadURL(c, s3Client, bucketName, key, presignedUrlExpirySeconds)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"presigned_url": url})
}

type ConfirmBookUpload struct {
	Title      string `json:"title" binding:"required"`
	Author     string `json:"author"`
	S3Key      string `json:"s3_key"`
	TotalPages int    `json:"total_pages"`
}

func confirmBookUpload(c *gin.Context) {
	dbUser, err := getDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	var req ConfirmBookUpload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	book, err := queries.CreateBook(c, repository.CreateBookParams{ID: uuid.New(), OwnerID: dbUser.ID, S3Key: req.S3Key, TotalPages: int32(req.TotalPages)})

	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, book)
}


type UpdateReadingProgress struct {
	Title      string `json:"title" binding:"required"`
	Author     string `json:"author"`
	S3Key      string `json:"s3_key"`
	TotalPages int    `json:"total_pages"`
}

func getLibraryHandler(c *gin.Context) {
	dbUser, err := getDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	books, err := queries.GetBooksByOwnerID(c, dbUser.ID)

	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
  
  c.JSON(http.StatusOK, books)
}

func getLibraryHandler(c *gin.Context) {
	dbUser, err := getDBUserFromRequest(c)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
	}

	books, err := queries.GetBooksByOwnerID(c, dbUser.ID)

	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
  
  c.JSON(http.StatusOK, books)
}

func main() {
	clerk.SetKey(os.Getenv("CLERK_SECRET_KEY"))
	if err := setupDB(); err != nil {
		log.Fatalf("failed to setup db connections", err)
	}
	var err error
	s3Client, err = initS3Client(context.Background())
	bucketName = os.Getenv("S3_BUCKET_NAME")
	if bucketName == "" {
		log.Fatalf("failed to setup s3 bucket name")
	}

	if err != nil {
		log.Fatalf("failed to setup s3 client", err)
	}
	presignedUrlExpirySeconds = 15

	defer func() {
		if dbPool != nil {
			log.Println("Closing database connection pool...")
			dbPool.Close()
		}
	}()

	router := gin.Default()

	router.Use(AuthMiddleware())
	router.GET("/ping", func(c *gin.Context) {
		clerkUser, err := getClerkUserFromRequest(c)
		if err != nil {
			return
		}

		dbUser, err := getDBUserFromRequest(c)
		if err != nil {
			return
		}
		log.Println(clerkUser.ID, dbUser.ID)
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	router.GET("/me", meHandler)
	router.POST("/upload-book", generateUploadUrlHandler)

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
