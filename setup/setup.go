package setup

import (
	"context"
	"log"
	"os"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/Hodik/noteshelf-be.git/utils"
	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	UniqueViolationCode = "23505" // PostgreSQL error code for unique constraint violation
)

func SetupDB() (*pgxpool.Pool, error) {
	connString := os.Getenv("POSTGRESQL_URL")
	if connString == "" {
		log.Fatalf("failed to setup psql conn string")
	}

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		log.Fatalf("Unable to parse connection string: %v", err)
	}

	config.MaxConns = 1
	dbPool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v", err)
	}
	return dbPool, nil
}

func SetupQueries(dbPool *pgxpool.Pool) *repository.Queries {
	return repository.New(dbPool)
}

func Setup(presignedUrlExpirySeconds int) Config {

	clerk.SetKey(os.Getenv("CLERK_SECRET_KEY"))
	dbPool, err := SetupDB()
	if err != nil {
		log.Fatalln("failed to setup db connections", err.Error())
	}
	s3Client, err := utils.InitS3Client(context.Background())
	if err != nil {
		log.Fatalf("failed to setup s3 client %s", err)
	}

	bucketName := os.Getenv("S3_BUCKET_NAME")
	if bucketName == "" {
		log.Fatalf("failed to setup s3 bucket name")
	}

	cloudfrontUrl := os.Getenv("CLOUDFRONT_URL")
	if cloudfrontUrl == "" {
		log.Fatalf("failed to setup cloudfront url")
	}

	keyPairID := os.Getenv("KEYPAIR_ID")
	if keyPairID == "" {
		log.Fatalf("failed to setup key pair ID")
	}

	privateSignKey, err := utils.LoadPrivateKey("./private_key.pem")

	if err != nil {
		log.Fatalf("failed to setup private key %s", err)
	}
	queries := SetupQueries(dbPool)

	return Config{DBPool: dbPool, Queries: queries, S3Client: s3Client, BucketName: bucketName, PresignedUrlExpirySeconds: int64(presignedUrlExpirySeconds), CloudfrontUrl: cloudfrontUrl, PrivateSignKey: privateSignKey, KeyPairID: keyPairID}
}
