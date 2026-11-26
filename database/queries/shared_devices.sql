-- Accounts that share a device.
--
-- Remnawave reports which HWIDs connected through each subscription; the
-- worker's scan keeps a sighting per device and customer, and every device
-- seen on two or more customers becomes a row an operator reviews. Like the
-- rest of risk, nothing here suspends anybody: a verdict is a record, and the
-- adverse action, when there is one, is an ordinary customer mutation.

-- name: ResolveRemnawaveCustomers :many
-- Panel user IDs to customers. A subscription carries its own panel user; a
-- customer imported before subscriptions had slots is linked through
-- remnawave_users. Deleted and merged customers are left out.
SELECT s.remnawave_user_id::bigint AS remnawave_id, s.user_id
FROM subscriptions s
JOIN users u ON u.id = s.user_id AND u.status IN ('active', 'suspended')
WHERE s.remnawave_user_id = ANY(sqlc.arg(remnawave_ids)::bigint[])
UNION
SELECT r.remnawave_id, r.user_id
FROM remnawave_users r
JOIN users u ON u.id = r.user_id AND u.status IN ('active', 'suspended')
WHERE r.remnawave_id = ANY(sqlc.arg(remnawave_ids)::bigint[]);

-- name: ListCustomersWithTelegramIDs :many
-- Customers signed in with one of these Telegram accounts: the operators the
-- scan leaves out.
SELECT DISTINCT user_id FROM identities
WHERE provider = 'telegram' AND status = 'active'
  AND provider_subject = ANY(sqlc.arg(telegram_ids)::text[]);

-- name: UpsertDeviceSightings :exec
-- One row per (device, customer); the caller passes each pair once.
INSERT INTO device_sightings (device_handle, user_id, platform, first_seen_at, last_seen_at)
SELECT seen.handle, seen.user_id, NULLIF(seen.platform, ''), seen.seen_at, seen.seen_at
FROM (
  SELECT unnest(sqlc.arg(handles)::text[]) AS handle,
    unnest(sqlc.arg(user_ids)::uuid[]) AS user_id,
    unnest(sqlc.arg(platforms)::text[]) AS platform,
    unnest(sqlc.arg(seen_at)::timestamptz[]) AS seen_at
) AS seen
ON CONFLICT (device_handle, user_id) DO UPDATE
SET last_seen_at = GREATEST(device_sightings.last_seen_at, EXCLUDED.last_seen_at),
    platform = COALESCE(EXCLUDED.platform, device_sightings.platform);

-- name: PruneDeviceSightings :execrows
-- A device nobody has connected with for the retention period is forgotten.
DELETE FROM device_sightings WHERE last_seen_at < sqlc.arg(before);

-- name: RefreshSharedDevices :execrows
-- Recomputes the queue from the sightings. A device that gained a customer
-- since its verdict is reopened; the earlier verdict stays on the row as
-- history.
WITH clusters AS (
  SELECT s.device_handle,
    count(*)::integer AS accounts,
    count(*) FILTER (WHERE EXISTS (
      SELECT 1 FROM trial_claims c WHERE c.user_id = s.user_id))::integer AS trial_accounts,
    max(s.last_seen_at) AS last_seen_at,
    (array_agg(s.platform ORDER BY s.last_seen_at DESC) FILTER (WHERE s.platform IS NOT NULL))[1] AS platform
  FROM device_sightings s
  GROUP BY s.device_handle
  HAVING count(*) >= 2
), pairs AS (
  SELECT referred.device_handle, count(*)::integer AS referral_pairs
  FROM device_sightings referred
  JOIN referral_attributions a ON a.referred_user_id = referred.user_id
  JOIN device_sightings referrer ON referrer.device_handle = referred.device_handle
    AND referrer.user_id = a.referrer_user_id
  GROUP BY referred.device_handle
)
INSERT INTO shared_devices (device_handle, accounts, trial_accounts, referral_pairs, platform, last_seen_at)
SELECT c.device_handle, c.accounts, c.trial_accounts, COALESCE(p.referral_pairs, 0), c.platform, c.last_seen_at
FROM clusters c
LEFT JOIN pairs p ON p.device_handle = c.device_handle
ON CONFLICT (device_handle) DO UPDATE
SET accounts = EXCLUDED.accounts,
    trial_accounts = EXCLUDED.trial_accounts,
    referral_pairs = EXCLUDED.referral_pairs,
    platform = COALESCE(EXCLUDED.platform, shared_devices.platform),
    last_seen_at = EXCLUDED.last_seen_at,
    status = CASE
      WHEN shared_devices.status <> 'open' AND EXCLUDED.accounts > shared_devices.reviewed_accounts THEN 'open'
      ELSE shared_devices.status
    END;

-- name: DropUnsharedDevices :execrows
-- A device left with fewer than two customers, because sightings were pruned
-- or an account was deleted, is no longer shared.
DELETE FROM shared_devices d
WHERE (SELECT count(*) FROM device_sightings s WHERE s.device_handle = d.device_handle) < 2;

-- name: FlagSharedDeviceReferrals :many
-- An invited customer on the same device as whoever invited them. The signal
-- is recorded once per pair and holds the attribution for review; a device an
-- operator marked legitimate does not raise it.
WITH pairs AS (
  SELECT a.referred_user_id, count(DISTINCT referred.device_handle)::integer AS devices
  FROM referral_attributions a
  JOIN device_sightings referred ON referred.user_id = a.referred_user_id
  JOIN device_sightings referrer ON referrer.device_handle = referred.device_handle
    AND referrer.user_id = a.referrer_user_id
  WHERE a.review_state <> 'rejected'
    AND NOT EXISTS (
      SELECT 1 FROM shared_devices d
      WHERE d.device_handle = referred.device_handle AND d.status = 'legitimate')
  GROUP BY a.referred_user_id
)
INSERT INTO referral_signals (referred_user_id, code, evidence)
SELECT referred_user_id, 'shared_device', jsonb_build_object('sharedDevices', devices)
FROM pairs
ON CONFLICT (referred_user_id, code) DO NOTHING
RETURNING referred_user_id;

-- name: SearchSharedDevices :many
SELECT * FROM shared_devices d
WHERE (
    sqlc.narg(cursor_last_seen_at)::timestamptz IS NULL
    OR (d.last_seen_at, d.device_handle) < (sqlc.narg(cursor_last_seen_at)::timestamptz, sqlc.narg(cursor_handle)::text)
  )
  AND (sqlc.narg(status)::text IS NULL OR d.status = sqlc.narg(status))
  AND (sqlc.narg(min_accounts)::integer IS NULL OR d.accounts >= sqlc.narg(min_accounts))
  AND (sqlc.narg(user_id)::uuid IS NULL OR EXISTS (
    SELECT 1 FROM device_sightings s
    WHERE s.device_handle = d.device_handle AND s.user_id = sqlc.narg(user_id)))
ORDER BY d.last_seen_at DESC, d.device_handle DESC
LIMIT sqlc.arg(page_size);

-- name: ListSharedDeviceAccounts :many
-- The customers on each listed device, with the facts an operator weighs: who
-- took a trial, and who invited whom on the same device.
SELECT s.device_handle, s.user_id, s.first_seen_at, s.last_seen_at,
  EXISTS (SELECT 1 FROM trial_claims c WHERE c.user_id = s.user_id) AS trial_claimed,
  (SELECT a.referrer_user_id FROM referral_attributions a
    JOIN device_sightings inviter ON inviter.user_id = a.referrer_user_id
      AND inviter.device_handle = s.device_handle
    WHERE a.referred_user_id = s.user_id LIMIT 1) AS invited_by
FROM device_sightings s
WHERE s.device_handle = ANY(sqlc.arg(handles)::text[])
ORDER BY s.device_handle, s.first_seen_at, s.user_id;

-- name: GetSharedDevice :one
SELECT * FROM shared_devices WHERE device_handle = $1;

-- name: ReviewSharedDevice :one
-- Records a verdict on an open device. It covers the customers on the device
-- now; another one joining reopens it.
UPDATE shared_devices
SET status = sqlc.arg(status),
    reviewed_accounts = accounts,
    review_reason = sqlc.arg(review_reason),
    reviewed_by = sqlc.narg(reviewed_by),
    reviewed_at = now()
WHERE device_handle = sqlc.arg(device_handle) AND status = 'open'
RETURNING *;

-- name: ListOpenSharedDeviceHandles :many
SELECT device_handle FROM shared_devices WHERE status = 'open';

-- name: SetSuspensionEnd :exec
-- When a suspension ends; null is a ban with no end.
UPDATE users SET suspension_ends_at = sqlc.narg(ends_at), updated_at = now()
WHERE id = sqlc.arg(user_id);

-- name: ListExpiredSuspensions :many
SELECT id FROM users
WHERE status = 'suspended' AND suspension_ends_at IS NOT NULL AND suspension_ends_at <= sqlc.arg(now)
ORDER BY suspension_ends_at
LIMIT sqlc.arg(page_size);

-- name: ListSharedDeviceCustomers :many
SELECT user_id FROM device_sightings WHERE device_handle = $1;
