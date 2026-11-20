package panelpg

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/blocklist"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// screenPageSize is how many identifiers one screening round trip checks.
const screenPageSize = 1000

// ScreenResult is what one blocklist screen did.
type ScreenResult struct {
	// Checked is the number of customer identifiers looked up.
	Checked int `json:"checked"`
	// NewMatches are the matches this screen recorded; matches already on
	// record, decided or not, are not counted again.
	NewMatches int `json:"newMatches"`
	// SkippedOperators counts identifiers left out because they belong to an
	// operator.
	SkippedOperators int `json:"skippedOperators"`
}

// SetOperatorTelegramIDs names the Telegram accounts that are operators
// (APP_OPERATOR_TELEGRAM_IDS). A customer signed in with one of them is never
// screened against a blocklist and never produces a match: an operator is
// staff, and a list a publisher assembled cannot flag the people running the
// installation, nor put their account in the review queue of a colleague.
func (service *Service) SetOperatorTelegramIDs(ids []int64) {
	exempt := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		exempt[id] = struct{}{}
	}
	service.operatorTelegramIDs = exempt
}

// operatorIdentifier reports whether an identifier is an operator's.
func (service *Service) operatorIdentifier(kind, value string) bool {
	if kind != blocklist.SubjectTelegramID || len(service.operatorTelegramIDs) == 0 {
		return false
	}
	normalized, err := blocklist.Normalize(kind, value)
	if err != nil {
		return false
	}
	id, err := strconv.ParseInt(normalized, 10, 64)
	if err != nil {
		return false
	}
	_, operator := service.operatorTelegramIDs[id]
	return operator
}

// operatorCustomer reports whether a customer signed in with an operator's
// Telegram account. A lookup failure answers false: the exemption must never
// be the reason a check silently stops working.
func (service *Service) operatorCustomer(ctx context.Context, customerID string) bool {
	if len(service.operatorTelegramIDs) == 0 {
		return false
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return false
	}
	telegramID, err := service.queries().GetCustomerTelegramID(ctx, id)
	if err != nil {
		return false
	}
	_, operator := service.operatorTelegramIDs[telegramID]
	return operator
}

// ScreenCustomers checks customers' Telegram identifiers against every
// enabled blocklist and records what it finds, exactly as CheckCustomer does
// for one customer. An empty customerID screens every customer, a page at a
// time with one lookup per page; that is the "check now" for the whole list,
// and what the sweeper runs after a source refresh so newly listed
// identifiers are flagged without waiting for the customer to act.
//
// Operators are skipped, allowlisted customers are skipped, and a match that
// is already on record — decided or not — is left exactly as it was.
func (service *Service) ScreenCustomers(ctx context.Context, customerID string) (ScreenResult, error) {
	var result ScreenResult
	only := pgtype.UUID{}
	if customerID != "" {
		id, err := parseUUID(customerID)
		if err != nil {
			return result, err
		}
		only = id
	}
	queries := service.queries()
	after := dbgen.ListBlocklistScreeningSubjectsParams{
		OnlyUserID: only, AfterUserID: pgtype.UUID{Valid: true}, PageSize: screenPageSize,
	}
	for {
		rows, err := queries.ListBlocklistScreeningSubjects(ctx, after)
		if err != nil {
			return result, err
		}
		if len(rows) == 0 {
			return result, nil
		}
		fingerprints := make([][]byte, 0, len(rows))
		owners := make(map[string][]pgtype.UUID, len(rows))
		for _, row := range rows {
			if service.operatorIdentifier(blocklist.SubjectTelegramID, row.ProviderSubject) {
				result.SkippedOperators++
				continue
			}
			fingerprint, fingerprintErr := blocklist.Fingerprint(blocklist.SubjectTelegramID, row.ProviderSubject)
			if fingerprintErr != nil {
				continue
			}
			result.Checked++
			fingerprints = append(fingerprints, fingerprint)
			owners[string(fingerprint)] = append(owners[string(fingerprint)], row.UserID)
		}
		if len(fingerprints) > 0 {
			hits, matchErr := queries.MatchBlocklistFingerprints(ctx, fingerprints)
			if matchErr != nil {
				return result, matchErr
			}
			byCustomer := map[pgtype.UUID][]dbgen.MatchBlocklistFingerprintsRow{}
			for _, hit := range hits {
				for _, owner := range owners[string(hit.BlocklistEntry.ValueFingerprint)] {
					byCustomer[owner] = append(byCustomer[owner], hit)
				}
			}
			for owner, customerHits := range byCustomer {
				recorded, recordErr := service.recordBlocklistHits(ctx, owner, customerHits)
				if recordErr != nil {
					return result, recordErr
				}
				result.NewMatches += len(recorded)
			}
		}
		if len(rows) < screenPageSize {
			return result, nil
		}
		last := rows[len(rows)-1]
		after.AfterUserID, after.AfterSubject = last.UserID, last.ProviderSubject
	}
}

// ScreenCustomersFor is ScreenCustomers started by an operator's "check now":
// the run is recorded in the audit trail with its counts.
func (service *Service) ScreenCustomersFor(
	ctx context.Context, customerID string, actor Actor,
) (ScreenResult, error) {
	result, err := service.ScreenCustomers(ctx, customerID)
	if err != nil {
		return result, err
	}
	targetType, targetID := "blocklist", "all"
	if customerID != "" {
		targetType, targetID = "customer", customerID
	}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		return appendAudit(ctx, queries, actor.audit(
			"panel.blocklist.screened", "risk", targetType, targetID,
			map[string]any{"checked": result.Checked, "newMatches": result.NewMatches},
		))
	})
	return result, err
}
