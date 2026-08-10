package botapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

var ErrNotLinked = errors.New("telegram account is not linked")

type IdentityStore interface {
	RemnawaveUserID(context.Context, int64) (int64, error)
	Link(context.Context, int64, int64) (int64, error)
}

func (store *PostgresStore) Link(ctx context.Context, telegramID, remnawaveID int64) (int64, error) {
	linkedID, err := store.queries.LinkTelegramRemnawaveUser(ctx, dbgen.LinkTelegramRemnawaveUserParams{
		RemnawaveID: remnawaveID,
		TelegramID:  telegramID,
	})
	if err != nil {
		return 0, fmt.Errorf("persist Telegram identity link: %w", err)
	}
	return linkedID, nil
}

type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	if databaseURL == "" {
		return nil, errors.New("database URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open bot database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping bot database: %w", err)
	}
	return &PostgresStore{pool: pool, queries: dbgen.New(pool)}, nil
}

func (store *PostgresStore) Close() {
	store.pool.Close()
}

func (store *PostgresStore) RemnawaveUserID(ctx context.Context, telegramID int64) (int64, error) {
	userID, err := store.queries.GetRemnawaveUserIDByTelegramID(ctx, pgtype.Int8{Int64: telegramID, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotLinked
	}
	if err != nil {
		return 0, fmt.Errorf("lookup Telegram identity: %w", err)
	}
	return userID, nil
}
