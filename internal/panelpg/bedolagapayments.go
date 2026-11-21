package panelpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// BedolagaPaymentClaim is a payment the previous bot opened that settled after
// the migration: credited to the customer it named, or waiting for an
// operator because it could not be attributed. See paymentservice/bedolaga.go.
type BedolagaPaymentClaim struct {
	ID                string     `json:"id"`
	Provider          string     `json:"provider"`
	ProviderReference string     `json:"providerReference"`
	BedolagaUserID    *int64     `json:"bedolagaUserId,omitempty"`
	CustomerID        string     `json:"customerId,omitempty"`
	AmountMinor       int64      `json:"amountMinor"`
	Currency          string     `json:"currency"`
	PaymentType       string     `json:"paymentType,omitempty"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	ResolutionNote    string     `json:"resolutionNote,omitempty"`
}

// BedolagaPaymentClaims lists the claims, newest first, optionally of one
// status: credited, needs_review, or resolved.
func (service *Service) BedolagaPaymentClaims(ctx context.Context, status string, limit int32) ([]BedolagaPaymentClaim, error) {
	switch status {
	case "", "credited", "needs_review", "resolved":
	default:
		return nil, ErrValidaton
	}
	rows, err := service.queries().ListBedolagaPaymentClaims(ctx, dbgen.ListBedolagaPaymentClaimsParams{
		Status: status, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, err
	}
	claims := make([]BedolagaPaymentClaim, 0, len(rows))
	for _, row := range rows {
		claims = append(claims, bedolagaClaim(row))
	}
	return claims, nil
}

// ResolveBedolagaPaymentClaim closes a queued payment once the operator has
// dealt with it — credited the right customer by hand, refunded it in the
// provider's dashboard — and says how in the note, which the audit trail
// keeps. Only a queued claim can be resolved; a credited one is already done.
func (service *Service) ResolveBedolagaPaymentClaim(
	ctx context.Context, claimID, note string, actor Actor,
) (BedolagaPaymentClaim, error) {
	note = strings.TrimSpace(note)
	if note == "" || len([]rune(note)) > 500 {
		return BedolagaPaymentClaim{}, ErrValidaton
	}
	id, err := parseUUID(claimID)
	if err != nil {
		return BedolagaPaymentClaim{}, err
	}
	operator, err := parseUUID(actor.AdminID)
	if err != nil {
		return BedolagaPaymentClaim{}, ErrValidaton
	}
	var resolved BedolagaPaymentClaim
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.ResolveBedolagaPaymentClaim(ctx, dbgen.ResolveBedolagaPaymentClaimParams{
			ClaimID: id, ResolvedBy: operator, Note: optionalText(note),
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if txErr != nil {
			return txErr
		}
		resolved = bedolagaClaim(row)
		// The note stays out of the audit metadata, like a support reply: the
		// claim row is the record.
		return appendAudit(ctx, queries, actor.audit(
			"panel.bedolaga_payment.resolved", "financial", "bedolaga_payment_claim", claimID,
			map[string]any{"provider": row.Provider, "amountMinor": row.AmountMinor, "currency": row.Currency},
		))
	})
	return resolved, err
}

func bedolagaClaim(row dbgen.BedolagaPaymentClaim) BedolagaPaymentClaim {
	claim := BedolagaPaymentClaim{
		ID: uuidString(row.ID), Provider: row.Provider, ProviderReference: row.ProviderReference,
		CustomerID: uuidString(row.UserID), AmountMinor: row.AmountMinor, Currency: row.Currency,
		PaymentType: row.PaymentType, Status: row.Status, Reason: row.Reason,
		CreatedAt: timeValue(row.CreatedAt), ResolutionNote: row.ResolutionNote.String,
	}
	if row.BedolagaUserID.Valid {
		value := row.BedolagaUserID.Int64
		claim.BedolagaUserID = &value
	}
	if row.ResolvedAt.Valid {
		value := timeValue(row.ResolvedAt)
		claim.ResolvedAt = &value
	}
	return claim
}
