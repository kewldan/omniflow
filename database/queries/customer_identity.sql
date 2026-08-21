-- Telegram identity resolution for the customer web panel.
--
-- These exist beside LinkCustomerIdentity in commerce.sql because that upsert
-- only ever touches an *active* row: a Telegram account whose identity was
-- unlinked from the security screen left a `revoked` row behind, and every
-- later sign-in with that account hit the unique index and failed. The web
-- sign-in needs to see the revoked row, decide whose it is, and bring it back.

-- name: LockTelegramSubject :exec
-- The same transaction-scoped advisory lock the bot takes in EnsureCustomer,
-- keyed identically, so a first /start and a first widget sign-in from one
-- Telegram account cannot race each other into two customers.
SELECT pg_advisory_xact_lock(hashtextextended('omniflow:telegram:' || sqlc.arg(subject)::text, 0));

-- name: GetIdentityBySubjectAnyStatus :one
-- The (provider, subject) row whatever its status, with the account behind it.
-- The unique constraint guarantees at most one.
SELECT i.*, u.status AS user_status, u.locale AS user_locale, u.timezone AS user_timezone
FROM identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = sqlc.arg(provider) AND i.provider_subject = sqlc.arg(provider_subject);

-- name: ReactivateCustomerIdentity :one
-- Brings an unlinked identity back for the customer it always belonged to.
-- Scoped by user_id so it can never move a row between accounts: a caller that
-- wants another customer's revoked row gets zero rows and must refuse.
UPDATE identities
SET status = 'active',
    revoked_at = NULL,
    verified_at = COALESCE(verified_at, sqlc.arg(verified_at))
WHERE id = sqlc.arg(identity_id) AND user_id = sqlc.arg(user_id) AND status = 'revoked'
RETURNING *;

-- name: GetCustomerByTelegramMapping :one
-- The v0.2 adoption rule, as the bot applies it: a customer imported from
-- Remnawave carries their Telegram ID on the mapping row before they ever hold
-- an identity row. A widget sign-in must land on that customer rather than
-- provision an empty second one.
SELECT u.*
FROM remnawave_users r
JOIN users u ON u.id = r.user_id
WHERE r.telegram_id = sqlc.arg(telegram_id);

-- name: BackfillTelegramMapping :exec
-- Keeps the Remnawave mapping addressable by Telegram ID once an identity is
-- linked, as the bot does, so notification and self-service queries keyed on
-- telegram_id keep resolving. Only an empty slot is filled, and only when no
-- other mapping already claims the ID.
UPDATE remnawave_users AS mapping
SET telegram_id = sqlc.arg(telegram_id)
WHERE mapping.user_id = sqlc.arg(user_id) AND mapping.telegram_id IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM remnawave_users other WHERE other.telegram_id = sqlc.arg(telegram_id)
  );

-- name: EnsureBotPreferences :exec
-- A customer created by the web panel gets the same preferences row a customer
-- created by the bot gets, with the language both surfaces will read. Nothing
-- is overwritten: a row the bot already wrote wins.
INSERT INTO bot_preferences (user_id, locale)
VALUES (sqlc.arg(user_id), sqlc.arg(locale))
ON CONFLICT (user_id) DO NOTHING;

-- name: SetCustomerLocaleEverywhere :exec
-- The language the customer chose on the web, written to both places the two
-- surfaces read it from. The bot consults bot_preferences.locale first and
-- users.locale last; writing only the latter left the web's setting ignored.
WITH account AS (
  UPDATE users
  SET locale = sqlc.arg(locale), updated_at = now()
  WHERE id = sqlc.arg(user_id) AND status <> 'deleted'
)
INSERT INTO bot_preferences (user_id, locale)
VALUES (sqlc.arg(user_id), sqlc.arg(locale))
ON CONFLICT (user_id) DO UPDATE
SET locale = EXCLUDED.locale, updated_at = now();
