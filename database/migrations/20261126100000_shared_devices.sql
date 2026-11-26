-- Which customers' subscriptions a device has connected through, so accounts
-- that share one device can be put in front of an operator. The device is
-- kept only as its handle (the digest commerce.DeviceHandle derives from the
-- HWID Remnawave reports); the hardware ID itself never reaches the database.
CREATE TABLE device_sightings (
  device_handle text NOT NULL,
  user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  platform text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL,
  CONSTRAINT device_sightings_pkey PRIMARY KEY (device_handle, user_id),
  CONSTRAINT device_sightings_handle_check CHECK (char_length(device_handle) BETWEEN 8 AND 64),
  CONSTRAINT device_sightings_platform_check CHECK (platform IS NULL OR char_length(platform) <= 64)
);

CREATE INDEX device_sightings_user ON device_sightings (user_id);
CREATE INDEX device_sightings_last_seen ON device_sightings (last_seen_at);

-- One row per device seen on two or more customers: the review queue. The scan
-- keeps `accounts` current; an operator's verdict holds until a customer the
-- verdict did not cover joins the device, which reopens it.
CREATE TABLE shared_devices (
  device_handle text PRIMARY KEY,
  accounts integer NOT NULL,
  trial_accounts integer NOT NULL DEFAULT 0,
  referral_pairs integer NOT NULL DEFAULT 0,
  platform text,
  detected_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'open',
  reviewed_accounts integer,
  review_reason text,
  reviewed_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  reviewed_at timestamptz,
  CONSTRAINT shared_devices_accounts_check CHECK (accounts >= 2),
  CONSTRAINT shared_devices_counts_check CHECK (trial_accounts >= 0 AND referral_pairs >= 0),
  CONSTRAINT shared_devices_status_check CHECK (status IN ('open', 'legitimate', 'confirmed')),
  CONSTRAINT shared_devices_review_check CHECK (
    (status = 'open') OR (review_reason IS NOT NULL AND reviewed_at IS NOT NULL AND reviewed_accounts IS NOT NULL)
  ),
  CONSTRAINT shared_devices_reason_check CHECK (review_reason IS NULL OR char_length(review_reason) BETWEEN 1 AND 400)
);

CREATE INDEX shared_devices_queue ON shared_devices (status, last_seen_at DESC, device_handle);
