package config

import (
	"errors"
	"os"
	"strings"
)

// ObjectStoreConfig points the installation at an S3-compatible object store
// — MinIO beside the stack, Cloudflare R2, or AWS S3 — for the files it keeps:
// support attachments, including the ones customers send the bot. Without an
// endpoint the processes keep using the local attachment directory.
type ObjectStoreConfig struct {
	// Endpoint is APP_S3_ENDPOINT, a host and optional port without a scheme,
	// such as `minio:9000` or `<account>.r2.cloudflarestorage.com`.
	Endpoint string
	// AccessKey and SecretKey are APP_S3_ACCESS_KEY and APP_S3_SECRET_KEY.
	AccessKey string
	SecretKey string
	// Bucket is APP_S3_BUCKET, `omniflow` by default.
	Bucket string
	// DownloadsBucket is APP_S3_DOWNLOADS_BUCKET, the bucket mirrored client
	// installers are kept in; the main bucket by default. Installers are
	// re-fetched from their source and need no backup, so an installation that
	// archives its store can keep them out of the archive by giving them a
	// bucket of their own.
	DownloadsBucket string
	// Region is APP_S3_REGION, `us-east-1` by default (R2 takes `auto`).
	Region string
	// Secure is APP_S3_SECURE: TLS to the endpoint, on by default. A store
	// reached over the stack's private network, as MinIO beside it is, sets it
	// to false.
	Secure bool
}

// Enabled reports whether an object store is configured.
func (cfg ObjectStoreConfig) Enabled() bool { return cfg.Endpoint != "" }

// LoadObjectStore reads the object store's environment. An endpoint without
// credentials is refused at startup rather than at the first upload.
func LoadObjectStore() (ObjectStoreConfig, error) {
	cfg := ObjectStoreConfig{
		Endpoint:  strings.TrimSpace(os.Getenv("APP_S3_ENDPOINT")),
		AccessKey: strings.TrimSpace(os.Getenv("APP_S3_ACCESS_KEY")),
		SecretKey: strings.TrimSpace(os.Getenv("APP_S3_SECRET_KEY")),
		Bucket:    envOr("APP_S3_BUCKET", "omniflow"),
		Region:    envOr("APP_S3_REGION", "us-east-1"),
		Secure:    boolEnvOr("APP_S3_SECURE", true),
	}
	cfg.DownloadsBucket = envOr("APP_S3_DOWNLOADS_BUCKET", cfg.Bucket)
	if !cfg.Enabled() {
		return cfg, nil
	}
	if strings.Contains(cfg.Endpoint, "://") || strings.Contains(cfg.Endpoint, "/") {
		return ObjectStoreConfig{}, errors.New("APP_S3_ENDPOINT is a host and port, without a scheme or path")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return ObjectStoreConfig{}, errors.New("APP_S3_ENDPOINT needs APP_S3_ACCESS_KEY and APP_S3_SECRET_KEY")
	}
	return cfg, nil
}
