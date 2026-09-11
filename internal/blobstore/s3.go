// Package blobstore stores CI artifacts and sealed job logs in S3-compatible
// object storage. The deployment target is self-hosted MinIO, which keeps the
// platform working air-gapped while leaving real S3 usable unchanged.
package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound is returned when a key does not exist.
var ErrNotFound = errors.New("object not found")

// Options configures a Client.
type Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Client is a bucket-scoped object store.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New connects to the endpoint and ensures the bucket exists. Creating the
// bucket here means a fresh air-gapped install needs no manual setup step.
func New(ctx context.Context, o Options) (*Client, error) {
	mc, err := minio.New(o.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: o.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("connect object storage: %w", err)
	}
	exists, err := mc.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %s: %w", o.Bucket, err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, o.Bucket, minio.MakeBucketOptions{}); err != nil {
			// A concurrent creator is not an error for us.
			if resp := minio.ToErrorResponse(err); resp.Code != "BucketAlreadyOwnedByYou" && resp.Code != "BucketAlreadyExists" {
				return nil, fmt.Errorf("create bucket %s: %w", o.Bucket, err)
			}
		}
	}
	return &Client{mc: mc, bucket: o.Bucket}, nil
}

// Put writes size bytes from r at key. A negative size streams with unknown
// length, which the multipart uploader handles.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// Get opens key for reading. A missing key yields ErrNotFound so callers can
// distinguish absence from a transport failure — the difference decides whether
// a job is retried or failed.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, c.mapErr(key, err)
	}
	// GetObject is lazy: the request is not made until the first read or Stat,
	// so a missing key only surfaces here.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, c.mapErr(key, err)
	}
	return obj, nil
}

// Delete removes key. Deleting an absent key is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// Bucket reports the bucket this client is scoped to.
func (c *Client) Bucket() string { return c.bucket }

func (c *Client) mapErr(key string, err error) error {
	resp := minio.ToErrorResponse(err)
	if resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" {
		return fmt.Errorf("%s: %w", key, ErrNotFound)
	}
	return fmt.Errorf("get %s: %w", key, err)
}
