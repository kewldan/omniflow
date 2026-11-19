package panelpg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// inTxRaw runs body in a transaction that also exposes the raw handle, for
// the few statements that have no generated query.
func (service *Service) inTxRaw(ctx context.Context, body func(pgx.Tx, *dbgen.Queries) error) error {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = body(tx, dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AppendAudit records an operator action from a package outside panelpg, in
// the caller's transaction, with exactly the row shape the panel's own
// actions produce. Features that keep their own service (e-mail, withdrawals,
// receipts, engagement) audit through this rather than writing a second
// audit format.
func AppendAudit(
	ctx context.Context, db dbgen.DBTX, actor Actor,
	action, category, targetType, targetID string, metadata map[string]any,
) error {
	return appendAudit(ctx, dbgen.New(db), actor.audit(action, category, targetType, targetID, metadata))
}
