package customerauthpg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// telegramRejection names why Telegram launch or widget data was refused, for
// the audit trail: the signature did not verify, the data was too old, or it
// was not shaped as Telegram's.
func telegramRejection(err error) string {
	switch {
	case errors.Is(err, customerauth.ErrTelegramSignature):
		return "bad_signature"
	case errors.Is(err, customerauth.ErrTelegramStale):
		return "stale"
	case errors.Is(err, customerauth.ErrTelegramMalformed):
		return "malformed"
	default:
		return "rejected"
	}
}

// auditSignInRejected records a refused customer sign-in as `customer.login`
// with outcome failure, the method and the reason, so a sign-in route that
// stops working shows up in the audit trail rather than only as 401s at the
// proxy. Nothing the customer sent is kept: no launch data, no hash, no
// Telegram ID. The answer to the browser does not change, and a failed write
// does not change it either.
func (service *Service) auditSignInRejected(ctx context.Context, request RequestContext, method, reason string) {
	metadata, _ := json.Marshal(map[string]string{"method": method, "reason": reason})
	_, _ = dbgen.New(service.pool).InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "anonymous", Action: "customer.login", Category: "authentication",
		Outcome: "failure", TargetType: "customer",
		Reason:    pgtype.Text{String: reason, Valid: true},
		RequestID: pgtype.Text{String: request.RequestID, Valid: request.RequestID != ""},
		Metadata:  metadata,
	})
}
