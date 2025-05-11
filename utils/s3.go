package utils

import (
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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

func DownloadFileToTmp(ctx context.Context, s3Client *s3.Client, bucketName, key, baseTmpDir string) (string, error) {
	localPath := filepath.Join(baseTmpDir, key)

	localDir := filepath.Dir(localPath)

	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create local directory '%s': %w", localDir, err)
	}

	localFile, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file '%s': %w", localPath, err)
	}
	defer localFile.Close()

	getObjectInput := &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	}

	resp, err := s3Client.GetObject(ctx, getObjectInput)
	if err != nil {
		os.Remove(localPath)
		return "", fmt.Errorf("failed to get object '%s' from bucket '%s': %w", key, bucketName, err)
	}

	defer resp.Body.Close()

	_, err = io.Copy(localFile, resp.Body)
	if err != nil {

		os.Remove(localPath)
		return "", fmt.Errorf("failed to copy S3 object body to local file '%s': %w", localPath, err)
	}

	log.Printf("Successfully downloaded s3://%s/%s to %s", bucketName, key, localPath)
	return localPath, nil
}

func UploadFileToS3(ctx context.Context, s3Client *s3.Client, localFilePath, bucketName, key string) error {
	localFile, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open local file '%s': %w", localFilePath, err)
	}
	defer localFile.Close()

	putObjectInput := &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
		Body:   localFile,
	}

	_, err = s3Client.PutObject(ctx, putObjectInput)
	if err != nil {
		return fmt.Errorf("failed to upload file '%s' to s3://%s/%s: %w", localFilePath, bucketName, key, err)
	}

	log.Printf("Successfully uploaded '%s' to s3://%s/%s", localFilePath, bucketName, key)
	return nil
}
