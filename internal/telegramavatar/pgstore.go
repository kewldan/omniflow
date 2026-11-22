package telegramavatar

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// PostgresStore keeps photos in the telegram_avatars table.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore builds a store on the installation's database.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Load returns the stored photo, when it was fetched, and whether a row exists.
func (store *PostgresStore) Load(ctx context.Context, telegramID int64) ([]byte, time.Time, bool, error) {
	row, err := dbgen.New(store.pool).GetTelegramAvatar(ctx, telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return row.Photo, row.FetchedAt.Time, true, nil
}

// Save records a fetched photo, or that there is none.
func (store *PostgresStore) Save(ctx context.Context, telegramID int64, photo []byte) error {
	return dbgen.New(store.pool).UpsertTelegramAvatar(ctx, dbgen.UpsertTelegramAvatarParams{
		TelegramID: telegramID, Photo: photo,
	})
}
