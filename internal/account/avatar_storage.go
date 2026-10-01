package account

import (
	"bytes"
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// avatarStorage holds uploaded avatars in a public bucket. Objects are written once under a new random key and never overwritten, so the public URL of one can be cached forever; replacing an avatar writes a new key and deletes the old one.
type avatarStorage interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Delete(ctx context.Context, key string) error
}

// r2Storage is a Cloudflare R2 bucket reached through its S3-compatible API. This is the only code that uses the AWS SDK.
type r2Storage struct {
	client *s3.Client
	bucket string
}

// avatarStorageFor is the avatar storage of a validated configuration: nil when no bucket is configured, and also nil, with an error in the log, when the bucket is configured but any of its secrets is missing from the environment, so uploads answer 503 avatar_upload_disabled while the rest of the service runs.
func avatarStorageFor(c AvatarConfig) avatarStorage {
	if c.Bucket == "" {
		return nil
	}
	var missing []string
	for _, name := range []string{c.AccountIDEnv, c.AccessKeyIDEnv, c.SecretAccessKeyEnv} {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slog.Error("avatar uploads disabled: storage secrets are not set", "bucket", c.Bucket, "missing_env", missing)
		return nil
	}
	return newR2Storage(c)
}

func newR2Storage(c AvatarConfig) *r2Storage {
	return newR2StorageAt("https://"+os.Getenv(c.AccountIDEnv)+".r2.cloudflarestorage.com", c)
}

// newR2StorageAt is newR2Storage against an explicit S3 endpoint, which is how tests reach a local server.
func newR2StorageAt(endpoint string, c AvatarConfig) *r2Storage {
	return &r2Storage{
		client: s3.New(s3.Options{
			Region:       "auto",
			BaseEndpoint: aws.String(endpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(os.Getenv(c.AccessKeyIDEnv), os.Getenv(c.SecretAccessKeyEnv), ""),
			UsePathStyle: true,
			// Only send a checksum where the operation requires one; R2 does not implement every checksum the SDK would otherwise add by default.
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		}),
		bucket: c.Bucket,
	}
}

func (r *r2Storage) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, e := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(r.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
		CacheControl:  aws.String("public, max-age=31536000, immutable"),
	})
	return e
}

func (r *r2Storage) Delete(ctx context.Context, key string) error {
	_, e := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	return e
}
