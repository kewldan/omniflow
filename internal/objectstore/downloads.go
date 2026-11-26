package objectstore

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/omniflow/omniflow/internal/clientmirror"
	"github.com/omniflow/omniflow/internal/config"
)

// downloadsPrefix keeps mirrored client installers apart from support
// attachments when both share a bucket.
const downloadsPrefix = "client-downloads/"

// ClientDownloads is where mirrored client installers live. It needs an object
// store: an installer is up to 300 MB, served by the API and written by the
// worker, and the two share no directory. Nil without one.
func ClientDownloads(cfg config.ObjectStoreConfig) (*Store, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	store, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.DownloadsBucket != "" {
		store.bucket = cfg.DownloadsBucket
	}
	store.prefix = downloadsPrefix
	return store, nil
}

// PutFile streams size bytes into the object under key.
func (store *Store) PutFile(
	ctx context.Context, key string, content io.Reader, size int64, contentType string,
) error {
	name, err := store.object(key)
	if err != nil {
		return err
	}
	if err := store.ensureBucket(ctx); err != nil {
		return err
	}
	_, err = store.client.PutObject(ctx, store.bucket, name, content, size,
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// OpenFile opens the object under key for reading and seeking, so a download
// can be served in ranges and resumed. Missing is os.ErrNotExist.
func (store *Store) OpenFile(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	name, err := store.object(key)
	if err != nil {
		return nil, err
	}
	object, err := store.client.GetObject(ctx, store.bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return nil, translate(err)
	}
	// GetObject is lazy; Stat is what learns the object is missing.
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, translate(err)
	}
	return object, nil
}

// ListFiles names every stored file and when it was written.
func (store *Store) ListFiles(ctx context.Context) ([]clientmirror.StoredFile, error) {
	if err := store.ensureBucket(ctx); err != nil {
		return nil, err
	}
	var files []clientmirror.StoredFile
	for object := range store.client.ListObjects(ctx, store.bucket, minio.ListObjectsOptions{
		Prefix: store.prefix, Recursive: true,
	}) {
		if object.Err != nil {
			return nil, fmt.Errorf("object store: list: %w", object.Err)
		}
		key := object.Key[strings.LastIndex(object.Key, "/")+1:]
		if !contentKey.MatchString(key) {
			continue
		}
		files = append(files, clientmirror.StoredFile{Key: key, Written: object.LastModified.UTC()})
	}
	return files, nil
}

// RemoveFile deletes the file under key; one already gone is not an error.
func (store *Store) RemoveFile(ctx context.Context, key string) error {
	return store.Remove(ctx, key)
}
