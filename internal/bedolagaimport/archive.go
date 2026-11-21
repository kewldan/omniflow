package bedolagaimport

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// ArchiveRecord is one row of a Bedolaga table, kept as it was so nothing the
// previous bot knew is lost once its database is gone. The columns that are
// secrets — passwords, tokens, hashes, VPN credentials, subscription links,
// IP addresses — are removed before the row is read.
type ArchiveRecord struct {
	Table      string
	SourceID   string
	UserID     *int64
	OccurredAt *time.Time
	// Data is the row as JSON, without the secret columns.
	Data []byte
}

// ArchiveSkippedTables are never read: they hold credentials, are Bedolaga's
// own bookkeeping, or are backup copies of tables that are read.
var ArchiveSkippedTables = regexp.MustCompile(`^(system_settings|web_api_tokens|cabinet_refresh_tokens|alembic_version)$|_backup_|_revived_`)

// ArchiveSecretColumns are removed from every archived row.
var ArchiveSecretColumns = regexp.MustCompile(`password|token|secret|_hash$|^hash$|api_?key|credential|ip_address|vless_uuid|subscription_url|crypto_link|email_change_code`)

// ArchiveUserColumns name the Bedolaga user a row belongs to, in order of
// preference.
var ArchiveUserColumns = []string{"user_id", "buyer_user_id", "redeemed_by"}

// ArchiveActorTables name their user column for who did something, not for
// whom: the operators' own audit log. Their rows are kept, attached to no
// customer.
var ArchiveActorTables = map[string]bool{"admin_audit_log": true}

// ArchiveTimeColumns name when a row happened, in order of preference.
var ArchiveTimeColumns = []string{"occurred_at", "created_at", "used_at", "clicked_at", "accepted_at", "assigned_at", "paid_at"}

// Codes of the archive decisions.
const (
	// CodeArchiveUserGone: the row belongs to a Bedolaga user who is not
	// carried (deleted, or never importable), so their history is not either.
	CodeArchiveUserGone = "archive_user_not_imported"
)

// ArchiveDecision is what happens to the archive of one table.
type ArchiveDecision struct {
	Table   string
	Create  []ArchiveRecord
	Already int
	Skipped int
}

func decideArchive(plan *Plan, source Source, target Target, userIndex map[int64]int) {
	byTable := map[string]*ArchiveDecision{}
	for _, record := range source.ArchiveRecords {
		decision := byTable[record.Table]
		if decision == nil {
			decision = &ArchiveDecision{Table: record.Table}
			byTable[record.Table] = decision
		}
		if record.UserID != nil {
			index, ok := userIndex[*record.UserID]
			if !ok || !plan.Users[index].Importable() {
				decision.Skipped++
				continue
			}
		}
		if target.ArchivedRecords[ArchiveKey(record.Table, record.SourceID)] {
			decision.Already++
			continue
		}
		decision.Create = append(decision.Create, record)
	}
	tables := make([]string, 0, len(byTable))
	for table := range byTable {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		plan.Archive = append(plan.Archive, *byTable[table])
	}
}

// ArchiveKey is the map key of Target.ArchivedRecords.
func ArchiveKey(table, sourceID string) string {
	return strings.ToLower(table) + ":" + sourceID
}
