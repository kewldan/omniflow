// Package objectstore keeps the installation's files in an S3-compatible
// object store: MinIO beside the stack, Cloudflare R2, or AWS S3.
package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/config"
)

// supportPrefix keeps support attachments apart from anything else the
// bucket may later hold.
const supportPrefix = "support-attachments/"

// contentKey is the attachment key: the SHA-256 of the content, in hex.
var contentKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is a bucket in an object store.
type Store struct {
	client *minio.Client
	bucket string
	region string
	prefix string

	mu    sync.Mutex
	ready bool
}

// New builds the client. It does not reach the store: a store that is down
// while a process starts must not stop it, so the bucket is checked, and
// created if missing, before the first write.
func New(cfg config.ObjectStoreConfig) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("object store: %w", err)
	}
	return &Store{client: client, bucket: cfg.Bucket, region: cfg.Region, prefix: supportPrefix}, nil
}

// SupportAttachments is where support attachments live: the object store when
// one is configured, the local directory otherwise.
func SupportAttachments(cfg config.ObjectStoreConfig, directory string) (accountsupport.AttachmentStore, error) {
	if !cfg.Enabled() {
		return accountsupport.NewDirectoryStore(directory), nil
	}
	return New(cfg)
}

// ensureBucket makes sure the bucket exists, once per process.
func (store *Store) ensureBucket(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ready {
		return nil
	}
	exists, err := store.client.BucketExists(ctx, store.bucket)
	if err != nil {
		return fmt.Errorf("object store: bucket %q: %w", store.bucket, err)
	}
	if !exists {
		err = store.client.MakeBucket(ctx, store.bucket, minio.MakeBucketOptions{Region: store.region})
		if err != nil && minio.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
			return fmt.Errorf("object store: create bucket %q: %w", store.bucket, err)
		}
	}
	store.ready = true
	return nil
}

func (store *Store) object(key string) (string, error) {
	if !contentKey.MatchString(key) {
		return "", errors.New("object store: malformed key")
	}
	return store.prefix + key[:2] + "/" + key, nil
}

// Put stores content under its key. The key is the content's digest, so a
// second Put of the same key writes the same bytes and is harmless.
func (store *Store) Put(ctx context.Context, key string, content []byte) error {
	name, err := store.object(key)
	if err != nil {
		return err
	}
	if err := store.ensureBucket(ctx); err != nil {
		return err
	}
	_, err = store.client.PutObject(ctx, store.bucket, name, bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

// Open reads the content stored under key, or os.ErrNotExist.
func (store *Store) Open(ctx context.Context, key string) ([]byte, error) {
	name, err := store.object(key)
	if err != nil {
		return nil, err
	}
	object, err := store.client.GetObject(ctx, store.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return nil, translate(err)
	}
	defer object.Close()
	content, err := io.ReadAll(object)
	if err != nil {
		return nil, translate(err)
	}
	return content, nil
}

// Remove deletes the content under key; a key already gone is not an error.
func (store *Store) Remove(ctx context.Context, key string) error {
	name, err := store.object(key)
	if err != nil {
		return err
	}
	return translateRemove(store.client.RemoveObject(ctx, store.bucket, name, minio.RemoveObjectOptions{}))
}

func translate(err error) error {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchBucket":
		return os.ErrNotExist
	}
	return err
}

func translateRemove(err error) error {
	if err == nil {
		return nil
	}
	if code := minio.ToErrorResponse(err).Code; code == "NoSuchKey" || code == "NoSuchBucket" {
		return nil
	}
	return err
}
