package connectpg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/clientmirror"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Mirrors is the table's side of the client mirrors: which are due, what each
// holds, and what the public download address may serve.
type Mirrors struct {
	queries *dbgen.Queries
}

// NewMirrors builds the repository over a pool.
func NewMirrors(pool *pgxpool.Pool) *Mirrors {
	if pool == nil {
		return nil
	}
	return &Mirrors{queries: dbgen.New(pool)}
}

var _ clientmirror.Repository = (*Mirrors)(nil)

// DueMirrors lists enabled mirrors whose check time has passed, oldest first.
func (mirrors *Mirrors) DueMirrors(ctx context.Context, limit int) ([]clientmirror.Due, error) {
	rows, err := mirrors.queries.DueConnectMirrors(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	due := make([]clientmirror.Due, 0, len(rows))
	for _, row := range rows {
		due = append(due, clientmirror.Due{
			ID: uuidText(row.ID),
			Source: clientmirror.Source{
				Repo: row.SourceRepo.String, TagPrefix: row.TagPrefix, AssetPattern: row.AssetPattern.String,
			},
			Checksum: row.Checksum.String, Version: row.Version.String,
			FileName: row.FileName.String, Size: row.SizeBytes.Int64,
		})
	}
	return due, nil
}

// RecordFile stores what a mirror now holds, unless its source changed.
func (mirrors *Mirrors) RecordFile(ctx context.Context, fetched clientmirror.Fetched) (bool, error) {
	id, err := parseID(fetched.ID)
	if err != nil {
		return false, err
	}
	updated, err := mirrors.queries.RecordConnectMirrorFile(ctx, dbgen.RecordConnectMirrorFileParams{
		Version:      text(fetched.Version),
		FileName:     text(fetched.FileName),
		SizeBytes:    pgtype.Int8{Int64: fetched.Size, Valid: true},
		Checksum:     text(fetched.Checksum),
		ContentType:  text(fetched.ContentType),
		NextCheckAt:  pgtype.Timestamptz{Time: fetched.NextCheck, Valid: true},
		ID:           id,
		SourceRepo:   text(fetched.Source.Repo),
		TagPrefix:    fetched.Source.TagPrefix,
		AssetPattern: text(fetched.Source.AssetPattern),
	})
	return updated == 1, err
}

// RecordChecked notes a check that changed nothing, or why it failed.
func (mirrors *Mirrors) RecordChecked(ctx context.Context, id, failure string, next time.Time) error {
	parsed, err := parseID(id)
	if err != nil {
		return err
	}
	return mirrors.queries.RecordConnectMirrorChecked(ctx, dbgen.RecordConnectMirrorCheckedParams{
		LastError:   pgtype.Text{String: failure, Valid: failure != ""},
		NextCheckAt: pgtype.Timestamptz{Time: next, Valid: true},
		ID:          parsed,
	})
}

// Checksums lists every file a row still refers to.
func (mirrors *Mirrors) Checksums(ctx context.Context) ([]string, error) {
	return mirrors.queries.ListConnectDownloadChecksums(ctx)
}

// ServedFile is what the public download address serves.
type ServedFile struct {
	FileName    string
	Size        int64
	Checksum    string
	ContentType string
	FetchedAt   time.Time
}

// ErrNoFile means there is nothing to serve at that address: no such
// download, one that holds no file yet, or one somebody switched off.
var ErrNoFile = errors.New("connectpg: no file to serve")

// File reads what a download serves.
func (mirrors *Mirrors) File(ctx context.Context, id string) (ServedFile, error) {
	parsed, err := parseID(id)
	if err != nil {
		return ServedFile{}, ErrNoFile
	}
	row, err := mirrors.queries.ConnectDownloadFile(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServedFile{}, ErrNoFile
	}
	if err != nil {
		return ServedFile{}, err
	}
	return ServedFile{
		FileName: row.FileName.String, Size: row.SizeBytes.Int64, Checksum: row.Checksum.String,
		ContentType: row.ContentType.String, FetchedAt: row.FetchedAt.Time,
	}, nil
}

func parseID(id string) (pgtype.UUID, error) {
	var parsed pgtype.UUID
	if err := parsed.Scan(id); err != nil {
		return pgtype.UUID{}, err
	}
	return parsed, nil
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func uuidText(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
