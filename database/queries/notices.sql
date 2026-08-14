-- Transactional notice overrides.
--
-- A row is the exception and its absence is the shipped wording, so there is no
-- "list all notices" query here: the set of notices is compiled into
-- `internal/notice`, and this only ever says which of them an operator has
-- reworded.

-- name: ListNoticeOverrides :many
SELECT code, locale, body, updated_at, updated_by
FROM notice_overrides
ORDER BY code, locale;

-- name: SaveNoticeOverride :one
-- Upsert, because an operator editing wording twice is editing the same thing.
INSERT INTO notice_overrides (code, locale, body, updated_by)
VALUES (sqlc.arg(code), sqlc.arg(locale), sqlc.arg(body), sqlc.narg(updated_by))
ON CONFLICT (code, locale) DO UPDATE
SET body = EXCLUDED.body, updated_at = now(), updated_by = EXCLUDED.updated_by
RETURNING code, locale, body, updated_at, updated_by;

-- name: DeleteNoticeOverride :execrows
-- Reverting to the shipped wording is a delete rather than a write of the
-- current default. An installation that reverts today and upgrades tomorrow
-- gets tomorrow's improved wording, which a copied-in default would have
-- silently frozen.
DELETE FROM notice_overrides
WHERE code = sqlc.arg(code) AND locale = sqlc.arg(locale);

-- name: EnqueueNoticeTestSend :one
-- The body is stored, not the code, because what the operator asked to see is
-- the text that was in the editor — which may not be saved and may be edited
-- again before the group receives it.
INSERT INTO notice_test_sends (code, locale, body, requested_by)
VALUES (sqlc.arg(code), sqlc.arg(locale), sqlc.arg(body), sqlc.narg(requested_by))
RETURNING id, code, locale, status, requested_at, resolved_at, error_code;

-- name: ListNoticeTestSends :many
-- What the screen shows under the button, so a test in flight looks like a test
-- in flight rather than a button that did nothing.
SELECT id, code, locale, status, requested_at, resolved_at, error_code
FROM notice_test_sends
WHERE code = sqlc.arg(code)
ORDER BY requested_at DESC
LIMIT sqlc.arg(page_size);

-- name: ListPendingNoticeTestSends :many
SELECT id, code, locale, body
FROM notice_test_sends
WHERE status = 'pending'
ORDER BY requested_at
LIMIT sqlc.arg(page_size);

-- name: CompleteNoticeTestSend :exec
UPDATE notice_test_sends
SET status = sqlc.arg(status), error_code = sqlc.narg(error_code), resolved_at = now()
WHERE id = sqlc.arg(test_send_id);
