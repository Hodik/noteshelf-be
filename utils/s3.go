package utils

import (
	"context"
	"crypto/rsa"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go/service/cloudfront/sign"
)

func InitS3Client(ctx context.Context) (*s3.Client, error) {
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

func GeneratePresignedReadURL(cloudfrontUrl, s3Key, keyPairID string, durationSeconds int, privateSignKey *rsa.PrivateKey) (string, error) {
	duration := time.Duration(durationSeconds * int(time.Second))
	expiresAt := time.Now().Add(duration)
	cloudfrontObjectURL := fmt.Sprintf("https://%s/%s", cloudfrontUrl, s3Key)

	urlSigner := sign.NewURLSigner(keyPairID, privateSignKey)

	signedURL, err := urlSigner.Sign(cloudfrontObjectURL, expiresAt)
	if err != nil {
		return "", fmt.Errorf("error generating presigned read url: %s", err.Error())
	}

	return signedURL, nil
}

func KeyExists(ctx context.Context, client *s3.Client, bucket, key string) bool {
	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err == nil {
		return true
	}

	return false
}

func GeneratePresignedUploadURL(ctx context.Context, s3Client *s3.Client, bucketName, key string, expirySeconds int64) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	request, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expirySeconds) * time.Second
	})

	if err != nil {
		return "", err
	}

	return request.URL, nil
}
