package panelpg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/clientmirror"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ConnectDownload is one place to get a recommended application: a store link,
// or a GitHub release asset this installation mirrors and serves itself.
type ConnectDownload struct {
	ID       string `json:"id,omitempty"`
	ClientID string `json:"clientId"`
	// Kind is "store" or "mirror".
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// URL is the store link.
	URL string `json:"url,omitempty"`
	// SourceRepo, TagPrefix, and AssetPattern name a mirror's release asset.
	SourceRepo   string `json:"sourceRepo,omitempty"`
	TagPrefix    string `json:"tagPrefix,omitempty"`
	AssetPattern string `json:"assetPattern,omitempty"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int32  `json:"sortOrder"`

	// What a mirror holds, written by the worker and read-only here.
	Version     string     `json:"version,omitempty"`
	FileName    string     `json:"fileName,omitempty"`
	SizeBytes   int64      `json:"sizeBytes,omitempty"`
	Checksum    string     `json:"checksum,omitempty"`
	FetchedAt   *time.Time `json:"fetchedAt,omitempty"`
	CheckedAt   *time.Time `json:"checkedAt,omitempty"`
	NextCheckAt *time.Time `json:"nextCheckAt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	// Path is where customers download the file, once there is one.
	Path string `json:"path,omitempty"`

	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy,omitempty"`
}

// SaveConnectDownload creates or updates a store link or a mirror.
//
// Saving a mirror with a changed source makes it due at once, so the worker
// fetches the new source within a minute rather than at the next check.
func (service *Service) SaveConnectDownload(
	ctx context.Context, download ConnectDownload, actor Actor,
) (ConnectDownload, error) {
	download.Kind = strings.TrimSpace(download.Kind)
	download.Label = strings.TrimSpace(download.Label)
	clientID, err := parseUUID(download.ClientID)
	if err != nil {
		return ConnectDownload{}, wrapValidation(fmt.Errorf("a client is required"))
	}
	if download.Label == "" || len([]rune(download.Label)) > 40 {
		return ConnectDownload{}, wrapValidation(fmt.Errorf("a label of up to 40 characters is required"))
	}

	params := dbgen.UpsertConnectDownloadParams{
		ID: optionalUUID(download.ID), ClientID: clientID, Kind: download.Kind,
		Label: download.Label, Enabled: download.Enabled, SortOrder: download.SortOrder,
		UpdatedBy: optionalUUID(actor.AdminID),
	}
	auditDetails := map[string]any{"kind": download.Kind, "label": download.Label, "enabled": download.Enabled}
	switch download.Kind {
	case clientmirror.KindStore:
		download.URL = strings.TrimSpace(download.URL)
		if download.URL == "" {
			return ConnectDownload{}, wrapValidation(fmt.Errorf("a store link needs an address"))
		}
		// The same rule as a client's own download address: it is handed to
		// somebody about to install software, so it is https only.
		if err := commerce.ValidateDownloadURL(download.URL); err != nil {
			return ConnectDownload{}, wrapValidation(err)
		}
		params.Url = optionalText(download.URL)
		auditDetails["url"] = download.URL
	case clientmirror.KindMirror:
		source := clientmirror.NormaliseSource(clientmirror.Source{
			Repo: download.SourceRepo, TagPrefix: download.TagPrefix, AssetPattern: download.AssetPattern,
		})
		if err := clientmirror.ValidateSource(source); err != nil {
			return ConnectDownload{}, wrapValidation(err)
		}
		params.SourceRepo = optionalText(source.Repo)
		params.TagPrefix = source.TagPrefix
		params.AssetPattern = optionalText(source.AssetPattern)
		auditDetails["repo"] = source.Repo
		auditDetails["tagPrefix"] = source.TagPrefix
		auditDetails["asset"] = source.AssetPattern
	default:
		return ConnectDownload{}, wrapValidation(fmt.Errorf("the kind is store or mirror"))
	}

	var saved ConnectDownload
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, err := queries.UpsertConnectDownload(ctx, params)
		if err != nil {
			// A client that no longer exists arrives as a foreign-key violation.
			return conflicted(err)
		}
		saved = connectDownloadFromRow(row)
		return appendAudit(ctx, queries, actor.audit(
			"connect.download.saved", "configuration", "connect_download", saved.ID, auditDetails,
		))
	})
	if err != nil {
		return ConnectDownload{}, err
	}
	return saved, nil
}

// DeleteConnectDownload removes a store link or a mirror. A mirror's file is
// removed from the store by the worker's sweep once nothing refers to it.
func (service *Service) DeleteConnectDownload(ctx context.Context, id string, actor Actor) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		removed, err := queries.DeleteConnectDownload(ctx, parsed)
		if err != nil {
			return err
		}
		if removed == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.download.deleted", "configuration", "connect_download", id, nil,
		))
	})
}

// CheckConnectMirror makes a mirror due now. The worker checks due mirrors
// every minute, so this is "look for a new release now" without the API
// holding a request open while a 150 MB file downloads.
func (service *Service) CheckConnectMirror(ctx context.Context, id string, actor Actor) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		updated, err := queries.RequestConnectMirrorCheck(ctx, parsed)
		if err != nil {
			return err
		}
		if updated == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"connect.download.check_requested", "configuration", "connect_download", id, nil,
		))
	})
}

func connectDownloadFromRow(row dbgen.UpsertConnectDownloadRow) ConnectDownload {
	download := ConnectDownload{
		ID: uuidString(row.ID), ClientID: uuidString(row.ClientID), Kind: row.Kind,
		Label: row.Label, URL: row.Url.String, SourceRepo: row.SourceRepo.String,
		TagPrefix: row.TagPrefix, AssetPattern: row.AssetPattern.String,
		Enabled: row.Enabled, SortOrder: row.SortOrder,
		Version: row.Version.String, FileName: row.FileName.String,
		SizeBytes: row.SizeBytes.Int64, Checksum: row.Checksum.String,
		LastError: row.LastError.String,
		UpdatedAt: row.UpdatedAt.Time, UpdatedBy: uuidString(row.UpdatedBy),
	}
	if row.FetchedAt.Valid {
		download.FetchedAt = &row.FetchedAt.Time
	}
	if row.CheckedAt.Valid {
		download.CheckedAt = &row.CheckedAt.Time
	}
	if row.Kind == clientmirror.KindMirror && row.NextCheckAt.Valid {
		download.NextCheckAt = &row.NextCheckAt.Time
	}
	if download.FileName != "" {
		download.Path = commerce.ClientDownloadPath(download.ID, download.FileName)
	}
	return download
}
