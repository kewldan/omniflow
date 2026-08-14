package panelpg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accesscode"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Wholesale code batches: selling a block of access to a distributor.
//
// Promo codes belong to a promotion and discount a purchase the customer still
// makes. Gifts are issued one at a time through an order. Neither is a reseller
// arrangement, which is: generate two hundred codes at an agreed price, hand
// over the list, and be able to kill whatever is left when the list leaks.
//
// The one thing to understand before reading the rest: the plaintext codes exist
// in exactly one place, the return value of CreateCodeBatch, and after that
// nowhere at all. Only the SHA-256 is stored. An operator who loses the list
// cannot recover it — which the screen says before they generate one, rather
// than after.

// CodeBatch is one batch as an operator sees it.
type CodeBatch struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	PlanCode  string `json:"planCode"`
	// PlanVersionID is what the batch grants, named explicitly rather than
	// resolved from a plan: a batch grants what the version said when it was
	// created, and publishing a new version must not alter what two hundred
	// people already hold a code for.
	PlanVersionID  string `json:"planVersionId"`
	PlanVersion    int32  `json:"planVersion"`
	BillingPeriod  string `json:"billingPeriod"`
	Quantity       int32  `json:"quantity"`
	UnitPriceMinor int64  `json:"unitPriceMinor"`
	Currency       string `json:"currency"`
	Note           string `json:"note,omitempty"`

	Issued   int64 `json:"issued"`
	Redeemed int64 `json:"redeemed"`
	Revoked  int64 `json:"revoked"`

	ExpiresAt    time.Time `json:"expiresAt,omitzero"`
	RevokedAt    time.Time `json:"revokedAt,omitzero"`
	RevokeReason string    `json:"revokeReason,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	CreatedBy    string    `json:"createdBy,omitempty"`
}

// GeneratedBatch is the one response that carries plaintext codes.
type GeneratedBatch struct {
	Batch CodeBatch `json:"batch"`
	// Codes is returned once and never again. It is a separate type from
	// CodeBatch precisely so that no listing endpoint can accidentally return
	// it: there is no field on the stored shape to put a code in.
	Codes []string `json:"codes"`
}

// BatchCode is one code's row, without anything redeemable in it.
type BatchCode struct {
	// Hint is the last four characters, which is enough to match a support
	// question to a row and far too little to guess the other twelve.
	Hint       string    `json:"hint"`
	Status     string    `json:"status"`
	RedeemedBy string    `json:"redeemedBy,omitempty"`
	RedeemedAt time.Time `json:"redeemedAt,omitzero"`
}

// MaxBatchSize bounds one batch.
//
// Ten thousand codes is a real reseller order and is also where generating
// stops being a request and starts being a job. A larger arrangement is several
// batches, which is what a distributor's own order structure looks like anyway.
const MaxBatchSize = 10000

// CodeBatches lists batches with their counts.
func (service *Service) CodeBatches(ctx context.Context, size int32) ([]CodeBatch, error) {
	rows, err := service.queries().ListCodeBatches(ctx, pageSize(size))
	if err != nil {
		return nil, err
	}
	batches := make([]CodeBatch, 0, len(rows))
	for _, row := range rows {
		batches = append(batches, CodeBatch{
			ID: uuidString(row.ID), Reference: row.Reference,
			PlanCode: row.PlanCode, PlanVersion: row.PlanVersion,
			BillingPeriod: row.BillingPeriod, Quantity: row.Quantity,
			UnitPriceMinor: row.UnitPriceMinor, Currency: row.Currency,
			Note: row.Note.String, Issued: row.Issued, Redeemed: row.Redeemed,
			Revoked: row.Revoked, ExpiresAt: row.ExpiresAt.Time,
			RevokedAt: row.RevokedAt.Time, RevokeReason: row.RevokeReason.String,
			CreatedAt: row.CreatedAt.Time, CreatedBy: uuidString(row.CreatedBy),
		})
	}
	return batches, nil
}

// BatchCodes lists one batch's codes by hint and status.
func (service *Service) BatchCodes(ctx context.Context, batchID string) ([]BatchCode, error) {
	id, err := parseUUID(batchID)
	if err != nil {
		return nil, ErrValidaton
	}
	rows, err := service.queries().ListBatchCodes(ctx, id)
	if err != nil {
		return nil, err
	}
	codes := make([]BatchCode, 0, len(rows))
	for _, row := range rows {
		codes = append(codes, BatchCode{
			Hint: row.CodeHint, Status: row.Status,
			RedeemedBy: uuidString(row.RedeemedBy), RedeemedAt: row.RedeemedAt.Time,
		})
	}
	return codes, nil
}

// CreateCodeBatch generates a batch and returns its codes once.
//
// Every code and the batch row commit together. A partial batch would leave an
// operator holding a list whose second half redeems and whose first half does
// not, with no way to tell which is which — the codes are the only copy and the
// database has nothing to compare them against.
func (service *Service) CreateCodeBatch(
	ctx context.Context, batch CodeBatch, actor Actor,
) (GeneratedBatch, error) {
	batch.Reference = strings.TrimSpace(batch.Reference)
	batch.Currency = strings.ToUpper(strings.TrimSpace(batch.Currency))

	if len(batch.Reference) < 3 {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf("a batch needs a reference to be found by"))
	}
	if batch.Quantity < 1 || batch.Quantity > MaxBatchSize {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf(
			"a batch holds between 1 and %d codes", MaxBatchSize))
	}
	if batch.UnitPriceMinor < 0 {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf("a price cannot be negative"))
	}
	if len(batch.Currency) != 3 {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf("a batch needs an ISO currency"))
	}
	planVersion, err := parseUUID(batch.PlanVersionID)
	if err != nil {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf("a batch needs a plan version to grant"))
	}
	if !batch.ExpiresAt.IsZero() && !batch.ExpiresAt.After(time.Now()) {
		return GeneratedBatch{}, wrapValidation(fmt.Errorf("an expiry in the past issues dead codes"))
	}

	// Generated before the transaction opens, so entropy exhaustion refuses the
	// request rather than rolling back a partially written batch.
	codes := make([]string, 0, batch.Quantity)
	hashes := make([][]byte, 0, batch.Quantity)
	hints := make([]string, 0, batch.Quantity)
	for index := int32(0); index < batch.Quantity; index++ {
		code, hint, codeErr := accesscode.New()
		if codeErr != nil {
			return GeneratedBatch{}, codeErr
		}
		codes = append(codes, code)
		hashes = append(hashes, accesscode.Hash(code))
		hints = append(hints, hint)
	}

	var created CodeBatch
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, insertErr := queries.CreateCodeBatch(ctx, dbgen.CreateCodeBatchParams{
			Reference: batch.Reference, PlanVersionID: planVersion,
			Quantity: batch.Quantity, UnitPriceMinor: batch.UnitPriceMinor,
			Currency: batch.Currency, Note: optionalText(batch.Note),
			ExpiresAt: expiryOrNever(batch.ExpiresAt),
			CreatedBy: optionalUUID(actor.AdminID),
		})
		if insertErr != nil {
			return conflicted(insertErr)
		}
		for index := range codes {
			if err := queries.InsertAccessCode(ctx, dbgen.InsertAccessCodeParams{
				BatchID: row.ID, CodeHash: hashes[index], CodeHint: hints[index],
			}); err != nil {
				return err
			}
		}
		created = CodeBatch{
			ID: uuidString(row.ID), Reference: row.Reference, Quantity: row.Quantity,
			UnitPriceMinor: row.UnitPriceMinor, Currency: row.Currency,
			Note: row.Note.String, ExpiresAt: row.ExpiresAt.Time,
			CreatedAt: row.CreatedAt.Time, CreatedBy: uuidString(row.CreatedBy),
			Issued: int64(row.Quantity),
		}
		// The audit records the shape of the batch and never a code. A trail
		// carrying two hundred redeemable codes would be a second copy of the
		// thing this whole design exists to keep in one place.
		return appendAudit(ctx, queries, actor.audit(
			"codes.batch.created", "financial", "code_batch", created.ID,
			map[string]any{
				"reference": batch.Reference, "quantity": batch.Quantity,
				"unitPriceMinor": batch.UnitPriceMinor, "currency": batch.Currency,
			},
		))
	})
	if err != nil {
		return GeneratedBatch{}, err
	}
	return GeneratedBatch{Batch: created, Codes: codes}, nil
}

// RevokeCodeBatch kills every code in a batch that nobody has redeemed.
func (service *Service) RevokeCodeBatch(
	ctx context.Context, batchID, reason string, actor Actor,
) (int64, error) {
	id, err := parseUUID(batchID)
	if err != nil {
		return 0, ErrValidaton
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 {
		// This is the action taken when a list leaks, and "why were these three
		// hundred codes killed" is the question somebody asks six months later.
		return 0, wrapValidation(fmt.Errorf("revoking a batch needs a reason"))
	}

	var revoked int64
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		count, killErr := queries.RevokeBatchCodes(ctx, id)
		if killErr != nil {
			return killErr
		}
		revoked = count
		if _, markErr := queries.MarkCodeBatchRevoked(ctx, dbgen.MarkCodeBatchRevokedParams{
			BatchID: id, Reason: pgtype.Text{String: reason, Valid: true},
			RevokedBy: optionalUUID(actor.AdminID),
		}); markErr != nil {
			return notFound(markErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"codes.batch.revoked", "financial", "code_batch", batchID,
			map[string]any{"revoked": revoked, "reason": reason},
		))
	})
	return revoked, err
}

// expiryOrNever turns the zero instant into SQL NULL.
//
// NULL means the codes never expire, which is a decision an operator is allowed
// to make and a liability they then carry. It is distinct from an expiry of
// "the zero time", which would issue a batch that was dead on arrival.
func expiryOrNever(at time.Time) pgtype.Timestamptz {
	if at.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timestamp(at)
}
