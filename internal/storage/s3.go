package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client wraps an S3-compatible client for file storage.
type S3Client struct {
	client *s3.Client
	bucket string
}

// S3Config holds S3 connection parameters.
type S3Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	Bucket          string
	ForcePathStyle  bool
}

// NewS3Client creates a new S3-compatible storage client.
func NewS3Client(cfg S3Config) (*S3Client, error) {
	opts := func(o *s3.Options) {
		o.Region = cfg.Region
		o.Credentials = credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	}

	client := s3.New(s3.Options{}, opts)

	return &S3Client{client: client, bucket: cfg.Bucket}, nil
}

// Upload stores data at the given key with the specified content type.
func (s *S3Client) Upload(ctx context.Context, key string, data io.Reader, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        data,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("uploading to S3: %w", err)
	}
	return nil
}

// Get retrieves the object at the given key. Caller must close the returned ReadCloser.
func (s *S3Client) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("getting from S3: %w", err)
	}
	ct := "application/octet-stream"
	if out.ContentType != nil {
		ct = *out.ContentType
	}
	return out.Body, ct, nil
}

// Delete removes the object at the given key. Used when an attachment row
// is removed so the backing object does not remain reachable by URL.
func (s *S3Client) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("deleting from S3: %w", err)
	}
	return nil
}
