package customerpg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// VerifiedContact opens the customer's most recently verified, still active
// contact of one kind. It returns an empty string when there is none.
//
// It exists for the one caller that must hand a contact to a third party: the
// payment service, for an acquirer that refuses a checkout without the payer's
// e-mail (Antilopay) or must send a fiscal receipt to it (YooKassa). An
// unverified address is never returned, because a receipt sent to an address
// the customer never confirmed is a receipt sent to a stranger.
func (service *Service) VerifiedContact(ctx context.Context, customerID, kind string) (string, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return "", err
	}
	var sealed []byte
	err = service.pool.QueryRow(ctx, `SELECT value_ciphertext FROM contact_channels
		WHERE user_id = $1 AND kind = $2 AND revoked_at IS NULL AND verified_at IS NOT NULL
		  AND value_ciphertext IS NOT NULL
		ORDER BY verified_at DESC, created_at DESC LIMIT 1`, id, kind).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	size := service.contactAEAD.NonceSize()
	if len(sealed) < size {
		return "", errors.New("stored contact is truncated")
	}
	plaintext, err := service.contactAEAD.Open(nil, sealed[:size], sealed[size:], []byte(kind))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
