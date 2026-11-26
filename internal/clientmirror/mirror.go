package clientmirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Due is a mirror whose check time has passed, with the file it holds now.
type Due struct {
	ID       string
	Source   Source
	Checksum string
	Version  string
	FileName string
	Size     int64
}

// Fetched is a file a mirror now holds.
type Fetched struct {
	ID          string
	Source      Source
	Version     string
	FileName    string
	Size        int64
	Checksum    string
	ContentType string
	NextCheck   time.Time
}

// StoredFile is one file in the store.
type StoredFile struct {
	Key     string
	Written time.Time
}

// Repository is the table's side of a mirror.
type Repository interface {
	DueMirrors(ctx context.Context, limit int) ([]Due, error)
	// RecordFile stores what a mirror now holds. False means the mirror's
	// source changed meanwhile and nothing was recorded.
	RecordFile(ctx context.Context, fetched Fetched) (bool, error)
	// RecordChecked notes a check; failure empty means it succeeded.
	RecordChecked(ctx context.Context, id, failure string, next time.Time) error
	// Checksums lists every file a row still refers to.
	Checksums(ctx context.Context) ([]string, error)
}

// Files is the store files are kept in, addressed by their SHA-256.
type Files interface {
	PutFile(ctx context.Context, key string, content io.Reader, size int64, contentType string) error
	ListFiles(ctx context.Context) ([]StoredFile, error)
	RemoveFile(ctx context.Context, key string) error
}

// Releases finds the newest release asset for a source.
type Releases interface {
	Latest(ctx context.Context, source Source) (Release, error)
}

// Options configure a Service.
type Options struct {
	Repository Repository
	Files      Files
	Releases   Releases
	// Download fetches release assets. Nil uses a client that follows https
	// redirects only and gives one file half an hour.
	Download *http.Client
	Logger   *slog.Logger
	Now      func() time.Time
}

// Service keeps mirrors at their newest release.
type Service struct {
	repository Repository
	files      Files
	releases   Releases
	download   *http.Client
	logger     *slog.Logger
	now        func() time.Time
}

// passBatch is how many mirrors one pass checks. A pass runs every minute, so
// a first fill of a dozen installers spreads over a few passes rather than
// holding one for the time it takes to copy them all.
const passBatch = 4

// sweepGrace keeps a file the sweep finds unreferenced for this long, because
// a file is written before the row that refers to it.
const sweepGrace = time.Hour

// New builds a service.
func New(options Options) *Service {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Download == nil {
		options.Download = &http.Client{
			Timeout: 30 * time.Minute,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				if request.URL.Scheme != "https" {
					return errors.New("a release asset redirected away from https")
				}
				return nil
			},
		}
	}
	return &Service{
		repository: options.Repository, files: options.Files, releases: options.Releases,
		download: options.Download, logger: options.Logger, now: options.Now,
	}
}

// Pass checks the mirrors that are due. A mirror that fails records why and
// is tried again later; only a failure to read or write the table fails the
// pass.
func (service *Service) Pass(ctx context.Context) error {
	due, err := service.repository.DueMirrors(ctx, passBatch)
	if err != nil {
		return err
	}
	for _, mirror := range due {
		if err := service.refresh(ctx, mirror); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			service.logger.Warn("client mirror check failed",
				"download", mirror.ID, "repo", mirror.Source.Repo, "error", err)
			failure := err.Error()
			if len(failure) > 500 {
				failure = failure[:500]
			}
			if recordErr := service.repository.RecordChecked(
				ctx, mirror.ID, failure, service.now().Add(RetryInterval),
			); recordErr != nil {
				return recordErr
			}
		}
	}
	return nil
}

// refresh brings one mirror to the newest release.
func (service *Service) refresh(ctx context.Context, mirror Due) error {
	release, err := service.releases.Latest(ctx, mirror.Source)
	if err != nil {
		return err
	}
	asset := release.Asset
	if asset.Size > MaxFileBytes {
		return fmt.Errorf("%s is %d MB, over the %d MB limit",
			asset.Name, asset.Size>>20, MaxFileBytes>>20)
	}
	fetched := Fetched{
		ID: mirror.ID, Source: mirror.Source, Version: release.Version,
		FileName: asset.Name, ContentType: ContentType(asset.Name),
		NextCheck: service.now().Add(CheckInterval),
	}

	if unchanged(mirror, release) {
		// The same bytes, perhaps under a new tag: record the release without
		// copying the file again.
		fetched.Checksum, fetched.Size = mirror.Checksum, mirror.Size
		if fetched.Size == 0 {
			fetched.Size = asset.Size
		}
		_, err := service.repository.RecordFile(ctx, fetched)
		return err
	}

	checksum, size, err := service.copy(ctx, asset)
	if err != nil {
		return err
	}
	fetched.Checksum, fetched.Size = checksum, size
	recorded, err := service.repository.RecordFile(ctx, fetched)
	if err != nil {
		return err
	}
	if recorded {
		service.logger.Info("client mirror updated",
			"download", mirror.ID, "repo", mirror.Source.Repo, "version", release.Version, "file", asset.Name)
	}
	return nil
}

// unchanged reports whether the mirror already holds the release's asset: by
// GitHub's digest when it publishes one, and by tag, name, and size when not.
func unchanged(mirror Due, release Release) bool {
	if mirror.Checksum == "" {
		return false
	}
	if release.Asset.Digest != "" {
		return release.Asset.Digest == mirror.Checksum
	}
	return mirror.Version == release.Version && mirror.FileName == release.Asset.Name &&
		mirror.Size == release.Asset.Size
}

// copy downloads an asset to a temporary file, checks it against what GitHub
// published, and stores it under its SHA-256.
func (service *Service) copy(ctx context.Context, asset Asset) (string, int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("User-Agent", "Omniflow-client-mirror")
	response, err := service.download.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("%s could not be downloaded: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%s could not be downloaded: GitHub answered %d", asset.Name, response.StatusCode)
	}

	temporary, err := os.CreateTemp("", "client-mirror-*")
	if err != nil {
		return "", 0, err
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, MaxFileBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("%s could not be downloaded: %w", asset.Name, err)
	}
	if size > MaxFileBytes {
		return "", 0, fmt.Errorf("%s is over the %d MB limit", asset.Name, MaxFileBytes>>20)
	}
	if asset.Size > 0 && size != asset.Size {
		return "", 0, fmt.Errorf("%s arrived as %d bytes, not the %d GitHub lists", asset.Name, size, asset.Size)
	}
	checksum := hex.EncodeToString(digest.Sum(nil))
	if asset.Digest != "" && checksum != asset.Digest {
		return "", 0, fmt.Errorf("%s does not match the SHA-256 GitHub publishes for it", asset.Name)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	if err := service.files.PutFile(ctx, checksum, temporary, size, ContentType(asset.Name)); err != nil {
		return "", 0, fmt.Errorf("%s could not be stored: %w", asset.Name, err)
	}
	return checksum, size, nil
}

// Sweep removes files no row refers to: a file a mirror replaced, and the
// files of mirrors, clients, and platforms an operator deleted.
func (service *Service) Sweep(ctx context.Context) error {
	referenced, err := service.repository.Checksums(ctx)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(referenced))
	for _, checksum := range referenced {
		keep[checksum] = true
	}
	files, err := service.files.ListFiles(ctx)
	if err != nil {
		return err
	}
	cutoff := service.now().Add(-sweepGrace)
	for _, file := range files {
		if keep[file.Key] || file.Written.After(cutoff) {
			continue
		}
		if err := service.files.RemoveFile(ctx, file.Key); err != nil {
			return err
		}
		service.logger.Info("client mirror file removed", "checksum", file.Key)
	}
	return nil
}
