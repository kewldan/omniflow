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
--
-- `freed` says an unlinked row no longer belongs to the customer it names. An
-- operator unlinked it — usually because it was linked to the wrong person — or
-- the customer has since linked another account of the same provider, which
-- they can hold only one of. A freed row is not brought back for its old owner;
-- the next sign-in with it is a new person's, and ClaimFreedIdentity hands the
-- row over. A row the customer unlinked themselves and did not replace is not
-- freed: the next sign-in with it relinks it.
SELECT i.*, u.status AS user_status, u.locale AS user_locale, u.timezone AS user_timezone,
       (i.status = 'revoked' AND (
          COALESCE(i.metadata->>'revoked_by', '') = 'operator'
          OR u.status = 'merged'
          OR EXISTS (
            SELECT 1 FROM identities other
            WHERE other.user_id = i.user_id AND other.provider = i.provider
              AND other.status = 'active' AND other.id <> i.id
          )
       ))::boolean AS freed
FROM identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = sqlc.arg(provider) AND i.provider_subject = sqlc.arg(provider_subject);

-- name: ClaimFreedIdentity :one
-- Hands a freed row to the customer now signing in or linking with it. The
-- predicate is GetIdentityBySubjectAnyStatus's `freed`, repeated so a row that
-- stopped being free between the read and this write is not taken.
UPDATE identities i
SET user_id = sqlc.arg(user_id), status = 'active', revoked_at = NULL,
    verified_at = sqlc.arg(verified_at), oidc_issuer = sqlc.narg(oidc_issuer),
    metadata = i.metadata - 'revoked_by'
WHERE i.id = sqlc.arg(identity_id) AND i.status = 'revoked'
  AND (
    COALESCE(i.metadata->>'revoked_by', '') = 'operator'
    OR EXISTS (SELECT 1 FROM users owner WHERE owner.id = i.user_id AND owner.status = 'merged')
    OR EXISTS (
      SELECT 1 FROM identities other
      WHERE other.user_id = i.user_id AND other.provider = i.provider
        AND other.status = 'active' AND other.id <> i.id
    )
  )
  AND NOT EXISTS (
    SELECT 1 FROM identities mine
    WHERE mine.user_id = sqlc.arg(user_id) AND mine.provider = i.provider AND mine.status = 'active'
  )
RETURNING *;

-- name: RevokeCustomerIdentityByOperator :one
-- An operator's unlink. The row is marked, which is what frees it: the next
-- sign-in with that account does not relink it to this customer.
UPDATE identities
SET status = 'revoked', revoked_at = now(),
    metadata = metadata || '{"revoked_by": "operator"}'::jsonb
WHERE id = sqlc.arg(identity_id) AND user_id = sqlc.arg(user_id) AND status = 'active'
RETURNING *;

-- name: ClearTelegramMapping :exec
-- An unlinked Telegram account stops being the address the customer-level
-- mapping gives notifications and the bot's adoption rule. Left in place, the
-- bot kept serving the account through a Telegram account it no longer had,
-- and a later sign-in adopted the customer by the mapping alone.
UPDATE remnawave_users SET telegram_id = NULL
WHERE user_id = sqlc.arg(user_id) AND telegram_id = sqlc.arg(telegram_id);

-- name: ReplaceTelegramMapping :exec
-- Linking a Telegram account to a customer whose mapping still names one they
-- no longer hold points the mapping at the new one.
UPDATE remnawave_users AS mapping
SET telegram_id = sqlc.arg(telegram_id)
WHERE mapping.user_id = sqlc.arg(user_id)
  AND mapping.telegram_id IS DISTINCT FROM sqlc.arg(telegram_id)
  AND NOT EXISTS (
    SELECT 1 FROM identities held
    WHERE held.user_id = mapping.user_id AND held.provider = 'telegram' AND held.status = 'active'
      AND held.provider_subject = mapping.telegram_id::text
  )
  AND NOT EXISTS (
    SELECT 1 FROM remnawave_users other WHERE other.telegram_id = sqlc.arg(telegram_id)
  );

-- name: ReactivateCustomerIdentity :one
-- Brings an unlinked identity back for the customer it always belonged to.
-- Scoped by user_id so it can never move a row between accounts: a caller that
-- wants another customer's revoked row gets zero rows and must refuse.
--
-- A customer who has since linked another account of the same provider holds
-- that one; this one is freed and does not come back, which is also what keeps
-- the one-per-provider index from failing the sign-in.
UPDATE identities i
SET status = 'active',
    revoked_at = NULL,
    verified_at = COALESCE(i.verified_at, sqlc.arg(verified_at)),
    metadata = i.metadata - 'revoked_by'
WHERE i.id = sqlc.arg(identity_id) AND i.user_id = sqlc.arg(user_id) AND i.status = 'revoked'
  AND NOT EXISTS (
    SELECT 1 FROM identities other
    WHERE other.user_id = i.user_id AND other.provider = i.provider
      AND other.status = 'active' AND other.id <> i.id
  )
RETURNING *;

-- name: GetCustomerByTelegramMapping :one
-- The adoption rule, as the bot applies it: a customer imported from
-- Remnawave carries their Telegram ID on the mapping row before they ever hold
-- an identity row. A widget sign-in must land on that customer rather than
-- provision an empty second one.
--
-- A customer who already signs in with a different Telegram account is not
-- adopted: the mapping is stale, and the customer can hold only one.
SELECT u.*
FROM remnawave_users r
JOIN users u ON u.id = r.user_id
WHERE r.telegram_id = sqlc.arg(telegram_id)
  AND NOT EXISTS (
    SELECT 1 FROM identities held
    WHERE held.user_id = u.id AND held.provider = 'telegram' AND held.status = 'active'
      AND held.provider_subject <> sqlc.arg(telegram_id)::bigint::text
  );

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

-- name: LockCustomerSignInMethods :exec
-- Serialises changes to one customer's sign-in methods. The last-method guard
-- counts what remains and then removes one; under Read Committed two removals
-- of two different methods would each count two and both succeed, leaving
-- none. The row lock makes the second wait and count again.
SELECT 1 FROM users WHERE id = sqlc.arg(user_id) FOR NO KEY UPDATE;

-- name: CustomerIsActive :one
SELECT status = 'active' FROM users WHERE id = sqlc.arg(user_id);
