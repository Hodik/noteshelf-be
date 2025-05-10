package setup

import (
	"crypto/rsa"

	"github.com/Hodik/noteshelf-be.git/repository"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	DBPool                    *pgxpool.Pool
	Queries                   *repository.Queries
	S3Client                  *s3.Client
	BucketName                string
	PresignedUrlExpirySeconds int64
	CloudfrontUrl             string
	PrivateSignKey            *rsa.PrivateKey
	KeyPairID                 string
}
