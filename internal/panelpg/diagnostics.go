package panelpg

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Diagnostics, the telemetry payload preview, and backup status.
//
// These three exist for the same reason: an operator should be able to answer
// "what is this installation doing, and what is it telling anybody else?"
// without shell access. The interesting constraint is what they must not
// contain.
//
// A diagnostics bundle is the thing an operator emails to somebody for help. It
// is assembled from an allowlist of fields rather than by dumping state and
// filtering, because a filter has to be updated every time a field is added and
// an allowlist fails closed when somebody forgets.
//
// The telemetry preview is the exact payload, rendered from the same values
// that would be sent. A preview built separately from the sender is a promise
// about a different program.

// Diagnostics is the support bundle.
type Diagnostics struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Version     string    `json:"version"`
	GoVersion   string    `json:"goVersion"`
	Platform    string    `json:"platform"`

	// Migration is the schema state, which is the first thing anybody asks
	// about when an installation behaves unlike the documentation.
	Migration MigrationStatus `json:"migration"`

	// Settings lists which sections have been configured and which hold a
	// credential — never the credential, and never the document either: a
	// branding document is harmless, an operator-group document names a chat,
	// and drawing that line per section is exactly the mistake an allowlist
	// avoids.
	Settings []SettingStatus `json:"settings"`

	Backups     BackupStatus     `json:"backups"`
	Providers   []ProviderHealth `json:"paymentProviders"`
	Maintenance MaintenanceState `json:"maintenance"`

	// Counts are row counts for the tables an operator's question usually
	// concerns. They are counts rather than samples for the obvious reason.
	Counts map[string]int64 `json:"counts"`
}

// MigrationStatus is what schema version the database is on.
type MigrationStatus struct {
	Applied int64  `json:"applied"`
	Latest  string `json:"latest"`
	// Dirty reports a migration that failed part-way. It is surfaced because an
	// installation in that state produces errors that look like anything else.
	Dirty bool `json:"dirty"`
}

// SettingStatus is one section's configuration state without its contents.
type SettingStatus struct {
	Section    string    `json:"section"`
	Configured bool      `json:"configured"`
	HasSecret  bool      `json:"hasSecret"`
	Version    int32     `json:"version"`
	UpdatedAt  time.Time `json:"updatedAt,omitzero"`
}

// BackupStatus is the backup schedule's record.
type BackupStatus struct {
	Total          int64     `json:"total"`
	LastAt         time.Time `json:"lastAt,omitzero"`
	LastSizeBytes  int64     `json:"lastSizeBytes"`
	LastVerifiedAt time.Time `json:"lastVerifiedAt,omitzero"`
	// LastRestoreAt is when a restore was last performed or tested. A backup
	// nobody has ever restored is a backup nobody knows works.
	LastRestoreAt time.Time `json:"lastRestoreAt,omitzero"`
	RestoreCount  int64     `json:"restoreCount"`
}

// Diagnostics assembles the support bundle.
func (service *Service) Diagnostics(ctx context.Context, version string) (Diagnostics, error) {
	queries := service.queries()

	bundle := Diagnostics{
		GeneratedAt: service.now(),
		Version:     version,
		GoVersion:   runtime.Version(),
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Counts:      map[string]int64{},
	}

	sections, err := service.SettingSections(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	for _, section := range sections {
		bundle.Settings = append(bundle.Settings, SettingStatus{
			Section: section.Section,
			// A section is configured when somebody has saved it, which the
			// version records: the seeded row is version 1 and every save
			// increments.
			Configured: section.Version > 1,
			HasSecret:  section.SecretConfigured,
			Version:    section.Version,
			UpdatedAt:  section.UpdatedAt,
		})
	}
	sort.SliceStable(bundle.Settings, func(left, right int) bool {
		return bundle.Settings[left].Section < bundle.Settings[right].Section
	})

	bundle.Migration = service.migrationStatus(ctx)
	if backups, err := queries.BackupStatus(ctx); err == nil {
		bundle.Backups = BackupStatus{
			Total: backups.Total, LastAt: backups.LastAt.Time,
			LastSizeBytes: backups.LastSizeBytes, LastVerifiedAt: backups.LastVerifiedAt.Time,
			LastRestoreAt: backups.LastRestoreAt.Time, RestoreCount: backups.RestoreCount,
		}
	}
	if counts, err := queries.DiagnosticCounts(ctx); err == nil {
		bundle.Counts = map[string]int64{
			"customers": counts.Customers, "subscriptions": counts.Subscriptions,
			"orders": counts.Orders, "openTickets": counts.OpenTickets,
			"operators": counts.Operators, "outboxPending": counts.OutboxPending,
		}
	}

	if providers, err := service.ProviderHealth(ctx); err == nil {
		bundle.Providers = providers
	}
	if maintenance, err := service.MaintenanceState(ctx); err == nil {
		bundle.Maintenance = maintenance
	}
	return bundle, nil
}

// TelemetryPreview is exactly what would be sent, with the opt-out state.
type TelemetryPreview struct {
	Enabled bool `json:"enabled"`
	// InstallationID is the anonymous identifier. It is shown because "what
	// identifies us?" is the first question anybody asks about telemetry, and a
	// preview that hid it would be answering a different question.
	InstallationID string `json:"installationId"`
	Endpoint       string `json:"endpoint,omitempty"`
	// Payload is the rendered document. It is built from the same values the
	// sender uses, because a preview assembled separately is a promise about a
	// different program.
	Payload json.RawMessage `json:"payload"`
	// Fields lists every key in the payload, flattened, so an operator can scan
	// what leaves without reading JSON.
	Fields []string `json:"fields"`
	// LastSentAt is when anything last left, or zero if nothing ever has.
	LastSentAt time.Time `json:"lastSentAt,omitzero"`
	EventCount int64     `json:"eventCount"`
}

// TelemetryPreview renders the payload without sending it.
func (service *Service) TelemetryPreview(
	ctx context.Context, enabled bool, endpoint, version string,
) (TelemetryPreview, error) {
	queries := service.queries()

	preview := TelemetryPreview{Enabled: enabled, Endpoint: endpoint}
	if installation, err := queries.TelemetryInstallation(ctx); err == nil {
		preview.InstallationID = uuidString(installation)
	}
	if summary, err := queries.TelemetrySummary(ctx); err == nil {
		preview.LastSentAt = summary.LastAt.Time
		preview.EventCount = summary.Total
	}

	// The payload is a heartbeat: what version is running, on what platform,
	// and nothing about who is running it. There is deliberately no customer
	// count, no revenue figure, and no domain — a number small enough to
	// identify an installation is an identifier.
	document := map[string]any{
		"installationId": preview.InstallationID,
		"version":        version,
		"goVersion":      runtime.Version(),
		"platform":       runtime.GOOS + "/" + runtime.GOARCH,
		"sentAt":         service.now().Format(time.RFC3339),
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return TelemetryPreview{}, err
	}
	preview.Payload = encoded

	preview.Fields = make([]string, 0, len(document))
	for field := range document {
		preview.Fields = append(preview.Fields, field)
	}
	sort.Strings(preview.Fields)
	return preview, nil
}

// BackupHistory is one recorded backup or restore.
type BackupHistory struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	SizeBytes  int64     `json:"sizeBytes"`
	CreatedAt  time.Time `json:"createdAt"`
	VerifiedAt time.Time `json:"verifiedAt,omitzero"`
	// RequestedBy is who asked, empty for the schedule.
	RequestedBy string `json:"requestedBy,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// BackupHistory lists backups and restores, newest first.
func (service *Service) BackupHistory(
	ctx context.Context, limit int32,
) ([]BackupHistory, []BackupHistory, error) {
	queries := service.queries()

	backupRows, err := queries.ListBackups(ctx, pageSize(limit))
	if err != nil {
		return nil, nil, err
	}
	backups := make([]BackupHistory, 0, len(backupRows))
	for _, row := range backupRows {
		backups = append(backups, BackupHistory{
			ID: uuidString(row.ID), Kind: row.Kind, Status: row.Status,
			SizeBytes: row.SizeBytes, CreatedAt: row.StartedAt.Time,
			VerifiedAt: row.VerifiedAt.Time, RequestedBy: row.RequestedBy.String,
		})
	}

	restoreRows, err := queries.ListBackupRestores(ctx, pageSize(limit))
	if err != nil {
		return backups, nil, err
	}
	restores := make([]BackupHistory, 0, len(restoreRows))
	for _, row := range restoreRows {
		restores = append(restores, BackupHistory{
			ID: uuidString(row.ID), Kind: "restore", Status: row.Status,
			CreatedAt: row.RequestedAt.Time, RequestedBy: row.OperatorID,
			Detail: strings.TrimSpace(row.Reason),
		})
	}
	return backups, restores, nil
}

// migrationStatus reads Atlas's own revision table.
//
// It is a raw query rather than a generated one because that table belongs to
// the migrator and is not part of this repository's schema — sqlc cannot see it,
// and inventing a second version number in application code would produce an
// answer that can disagree with the migrator's.
//
// An installation whose migrator has not run yet has no table at all, which is
// not an error: it reports zero applied revisions, and that is the true answer.
func (service *Service) migrationStatus(ctx context.Context) MigrationStatus {
	status := MigrationStatus{}
	row := service.pool.QueryRow(ctx, `
		SELECT count(*)::bigint,
		       coalesce(max(version), ''),
		       coalesce(bool_or(error IS NOT NULL AND error <> ''), false)
		FROM atlas_schema_revisions.atlas_schema_revisions`)
	if err := row.Scan(&status.Applied, &status.Latest, &status.Dirty); err != nil {
		return MigrationStatus{}
	}
	return status
}

var _ = dbgen.New
