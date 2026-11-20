package accountsupport

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// supportBlockedCode is the SQLSTATE the database raises when a customer an
// operator blocked from support opens a ticket or writes in one. The check
// is a trigger, so every surface that writes a ticket gets it.
const supportBlockedCode = "OFSB1"

// SupportBlocked reports whether an error is that refusal.
func SupportBlocked(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == supportBlockedCode
}
