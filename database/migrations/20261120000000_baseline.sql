-- Omniflow baseline schema.
--
-- The schema every installation starts from. It replaces the pre-release
-- migration history, which was squashed into this one file before the first
-- deployment; later changes are new migrations on top of it.
--
-- River's queue tables are River v0.43.0's `main` migration line, versions 1
-- through 7, and the river_migration rows at the end record that, so a later
-- River upgrade applies with rivermigrate (or a later migration) from exactly
-- this point. internal/integrationtest checks that rivermigrate reports no
-- unapplied River version after the migrations run. The `…1` suffixes on
-- river_migration's constraint names are River's own: its version 5 rebuilds
-- that table while the renamed original still holds the unsuffixed names.
--
-- The statements are written as pg_dump prints them, so this file and
-- database/schema.sql read the same way.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ----------------------------------------------------------------------------
-- Users, identities, telemetry and outbox
-- ----------------------------------------------------------------------------

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    locale text DEFAULT 'ru'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    suspended_at timestamp with time zone,
    deleted_at timestamp with time zone,
    anonymized_at timestamp with time zone,
    retention_until timestamp with time zone,
    merged_into uuid,
    merged_at timestamp with time zone,
    CONSTRAINT users_pkey PRIMARY KEY (id),
    CONSTRAINT users_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT users_merge_complete CHECK ((((status = 'merged'::text) = (merged_into IS NOT NULL)) AND ((merged_into IS NULL) = (merged_at IS NULL)))),
    CONSTRAINT users_merge_not_self CHECK (((merged_into IS NULL) OR (merged_into <> id))),
    CONSTRAINT users_status_known CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'deleted'::text, 'merged'::text]))),
    CONSTRAINT users_merged_into_fkey FOREIGN KEY (merged_into) REFERENCES users(id)
);

COMMENT ON COLUMN users.merged_into IS 'Where this account went. A merged account keeps its rows and its history; everything transferable was moved to the target.';

CREATE INDEX users_merged_into_idx ON users USING btree (merged_into) WHERE (merged_into IS NOT NULL);

CREATE INDEX users_status_created_idx ON users USING btree (status, created_at DESC);

CREATE TABLE identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    provider_subject text NOT NULL,
    verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    revoked_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    oidc_issuer text,
    CONSTRAINT identities_pkey PRIMARY KEY (id),
    CONSTRAINT identities_provider_provider_subject_key UNIQUE (provider, provider_subject),
    CONSTRAINT identities_oidc_issuer_only_for_oidc CHECK (((oidc_issuer IS NULL) OR ((provider ~~ 'oidc:%'::text) AND (oidc_issuer ~ '^https://'::text)))),
    CONSTRAINT identities_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'revoked'::text]))),
    CONSTRAINT identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE UNIQUE INDEX identities_one_active_provider_per_user_idx ON identities USING btree (user_id, provider) WHERE (status = 'active'::text);

CREATE TABLE remnawave_users (
    user_id uuid NOT NULL,
    remnawave_id bigint NOT NULL,
    telegram_id bigint,
    observed_state jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT remnawave_users_pkey PRIMARY KEY (user_id),
    CONSTRAINT remnawave_users_remnawave_id_key UNIQUE (remnawave_id),
    CONSTRAINT remnawave_users_telegram_id_key UNIQUE (telegram_id),
    CONSTRAINT remnawave_users_remnawave_id_check CHECK ((remnawave_id > 0)),
    CONSTRAINT remnawave_users_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE telemetry_installation (
    singleton boolean DEFAULT true NOT NULL,
    installation_id uuid DEFAULT gen_random_uuid() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT telemetry_installation_pkey PRIMARY KEY (singleton),
    CONSTRAINT telemetry_installation_singleton_check CHECK (singleton)
);

CREATE TABLE telemetry_events (
    id bigint GENERATED ALWAYS AS IDENTITY,
    installation_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    service text NOT NULL,
    os text NOT NULL,
    architecture text NOT NULL,
    features jsonb DEFAULT '{}'::jsonb NOT NULL,
    counters jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT telemetry_events_pkey PRIMARY KEY (id),
    CONSTRAINT telemetry_events_name_check CHECK ((name = ANY (ARRAY['installation.heartbeat'::text, 'feature.usage'::text])))
);

CREATE INDEX telemetry_events_installation_idx ON telemetry_events USING btree (installation_id, received_at DESC);

CREATE INDEX telemetry_events_received_at_idx ON telemetry_events USING btree (received_at);

CREATE TABLE outbox_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    topic text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    CONSTRAINT outbox_events_pkey PRIMARY KEY (id)
);

CREATE INDEX outbox_events_unpublished_idx ON outbox_events USING btree (occurred_at) WHERE (published_at IS NULL);

-- ----------------------------------------------------------------------------
-- Bot sessions, support, referrals and notifications
-- ----------------------------------------------------------------------------

CREATE TABLE bot_preferences (
    user_id uuid NOT NULL,
    locale text DEFAULT 'auto'::text NOT NULL,
    expiry_notifications boolean DEFAULT true NOT NULL,
    traffic_notifications boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    renewal_notifications boolean DEFAULT true NOT NULL,
    news_notifications boolean DEFAULT true NOT NULL,
    marketing_enabled boolean DEFAULT false NOT NULL,
    quiet_hours_start smallint,
    quiet_hours_end smallint,
    CONSTRAINT bot_preferences_pkey PRIMARY KEY (user_id),
    CONSTRAINT bot_preferences_locale_check CHECK ((locale = ANY (ARRAY['auto'::text, 'ru'::text, 'en'::text]))),
    CONSTRAINT bot_preferences_quiet_hours_check CHECK (((quiet_hours_start IS NULL) = (quiet_hours_end IS NULL))),
    CONSTRAINT bot_preferences_quiet_hours_end_check CHECK (((quiet_hours_end >= 0) AND (quiet_hours_end <= 23))),
    CONSTRAINT bot_preferences_quiet_hours_start_check CHECK (((quiet_hours_start >= 0) AND (quiet_hours_start <= 23))),
    CONSTRAINT bot_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE bot_sessions (
    telegram_id bigint NOT NULL,
    state text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone DEFAULT (now() + '00:30:00'::interval) NOT NULL,
    context jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT bot_sessions_pkey PRIMARY KEY (telegram_id),
    CONSTRAINT bot_sessions_state_check CHECK ((state = ANY (ARRAY['support_message'::text, 'support_reply'::text, 'promo_code'::text, 'topup_amount'::text, 'subscription_label'::text, 'gift_recipient'::text, 'gift_message'::text, 'gift_claim'::text, 'goods_recipient'::text, 'goods_promo'::text, 'goods_stars_amount'::text])))
);

CREATE TABLE support_tickets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    priority text DEFAULT 'normal'::text NOT NULL,
    last_message_at timestamp with time zone DEFAULT now() NOT NULL,
    customer_unread_count integer DEFAULT 0 NOT NULL,
    closed_at timestamp with time zone,
    queue_id uuid NOT NULL,
    assignee_id uuid,
    assigned_at timestamp with time zone,
    first_response_at timestamp with time zone,
    resolved_at timestamp with time zone,
    reopened_count integer DEFAULT 0 NOT NULL,
    operator_unread_count integer DEFAULT 0 NOT NULL,
    merged_into_ticket_id uuid,
    CONSTRAINT support_tickets_pkey PRIMARY KEY (id),
    CONSTRAINT support_tickets_customer_unread_count_check CHECK ((customer_unread_count >= 0)),
    CONSTRAINT support_tickets_merge_is_not_self CHECK (((merged_into_ticket_id IS NULL) OR (merged_into_ticket_id <> id))),
    CONSTRAINT support_tickets_operator_unread_count_check CHECK ((operator_unread_count >= 0)),
    CONSTRAINT support_tickets_priority_check CHECK ((priority = ANY (ARRAY['low'::text, 'normal'::text, 'high'::text, 'urgent'::text]))),
    CONSTRAINT support_tickets_reopened_count_check CHECK ((reopened_count >= 0)),
    CONSTRAINT support_tickets_status_check CHECK ((status = ANY (ARRAY['open'::text, 'pending'::text, 'resolved'::text, 'closed'::text, 'merged'::text]))),
    CONSTRAINT support_tickets_subject_check CHECK ((char_length(subject) <= 120)),
    CONSTRAINT support_tickets_merged_into_ticket_id_fkey FOREIGN KEY (merged_into_ticket_id) REFERENCES support_tickets(id),
    CONSTRAINT support_tickets_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX support_tickets_assignee_idx ON support_tickets USING btree (assignee_id, status, last_message_at) WHERE (assignee_id IS NOT NULL);

CREATE INDEX support_tickets_queue_idx ON support_tickets USING btree (queue_id, status, last_message_at) WHERE (status = ANY (ARRAY['open'::text, 'pending'::text]));

CREATE INDEX support_tickets_subject_trgm_idx ON support_tickets USING gin (subject public.gin_trgm_ops);

CREATE INDEX support_tickets_user_updated_idx ON support_tickets USING btree (user_id, updated_at DESC);

CREATE TABLE support_messages (
    id bigint GENERATED ALWAYS AS IDENTITY,
    ticket_id uuid NOT NULL,
    sender text NOT NULL,
    body text NOT NULL,
    telegram_message_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    dedupe_key text,
    delivered_at timestamp with time zone,
    read_at timestamp with time zone,
    author_id uuid,
    canned_response_id uuid,
    CONSTRAINT support_messages_pkey PRIMARY KEY (id),
    CONSTRAINT support_messages_body_check CHECK (((char_length(body) >= 1) AND (char_length(body) <= 4000))),
    CONSTRAINT support_messages_sender_check CHECK ((sender = ANY (ARRAY['customer'::text, 'operator'::text, 'system'::text]))),
    CONSTRAINT support_messages_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES support_tickets(id) ON DELETE CASCADE
);

CREATE INDEX support_messages_author_idx ON support_messages USING btree (author_id, created_at DESC) WHERE (author_id IS NOT NULL);

CREATE UNIQUE INDEX support_messages_dedupe_idx ON support_messages USING btree (ticket_id, dedupe_key) WHERE (dedupe_key IS NOT NULL);

CREATE INDEX support_messages_ticket_idx ON support_messages USING btree (ticket_id, created_at);

CREATE TABLE referral_codes (
    user_id uuid NOT NULL,
    code text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT referral_codes_pkey PRIMARY KEY (user_id),
    CONSTRAINT referral_codes_code_key UNIQUE (code),
    CONSTRAINT referral_codes_code_check CHECK ((code ~ '^[A-Z0-9]{10}$'::text)),
    CONSTRAINT referral_codes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE referral_attributions (
    referred_user_id uuid NOT NULL,
    referrer_user_id uuid NOT NULL,
    code text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    qualified_at timestamp with time zone,
    qualifying_order_id uuid,
    rejected_reason text,
    review_state text DEFAULT 'clear'::text NOT NULL,
    reviewed_by uuid,
    reviewed_at timestamp with time zone,
    review_note text,
    signal_codes text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT referral_attributions_pkey PRIMARY KEY (referred_user_id),
    CONSTRAINT referral_attributions_check CHECK ((referred_user_id <> referrer_user_id)),
    CONSTRAINT referral_attributions_review_note_check CHECK (((review_note IS NULL) OR (char_length(review_note) <= 400))),
    CONSTRAINT referral_attributions_review_state_check CHECK ((review_state = ANY (ARRAY['clear'::text, 'held'::text, 'rejected'::text]))),
    CONSTRAINT referral_attributions_referred_user_id_fkey FOREIGN KEY (referred_user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT referral_attributions_referrer_user_id_fkey FOREIGN KEY (referrer_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX referral_attributions_referrer_idx ON referral_attributions USING btree (referrer_user_id, created_at);

CREATE INDEX referral_attributions_review_idx ON referral_attributions USING btree (review_state, created_at DESC) WHERE (review_state <> 'clear'::text);

CREATE TABLE notification_deliveries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    dedupe_key text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    scheduled_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_at timestamp with time zone,
    failure_count integer DEFAULT 0 NOT NULL,
    class text DEFAULT 'transactional'::text NOT NULL,
    error_code text,
    deferred_until timestamp with time zone,
    subscription_id uuid,
    CONSTRAINT notification_deliveries_pkey PRIMARY KEY (id),
    CONSTRAINT notification_deliveries_class_check CHECK ((class = ANY (ARRAY['transactional'::text, 'marketing'::text]))),
    CONSTRAINT notification_deliveries_failure_count_check CHECK ((failure_count >= 0)),
    CONSTRAINT notification_deliveries_kind_known CHECK ((kind = ANY (ARRAY['expiry'::text, 'traffic'::text, 'renewal'::text, 'grace'::text, 'recovery'::text, 'payment'::text, 'fulfillment'::text, 'support'::text, 'news'::text, 'announcement'::text, 'incident'::text, 'maintenance'::text, 'marketing'::text, 'referral'::text, 'trial'::text, 'test'::text]))),
    CONSTRAINT notification_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'deferred'::text, 'sent'::text, 'failed'::text, 'suppressed'::text]))),
    CONSTRAINT notification_deliveries_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

COMMENT ON COLUMN notification_deliveries.error_code IS 'Why a message was not sent: a transport failure, or a policy outcome such as quiet_hours, frequency_cap, or no_consent. It is what makes "I never got it" answerable.';

CREATE UNIQUE INDEX notification_deliveries_dedupe_idx ON notification_deliveries USING btree (user_id, kind, subscription_id, dedupe_key) NULLS NOT DISTINCT;

CREATE INDEX notification_deliveries_frequency_idx ON notification_deliveries USING btree (user_id, class, sent_at DESC) WHERE (status = 'sent'::text);

CREATE INDEX notification_deliveries_pending_idx ON notification_deliveries USING btree (scheduled_at) WHERE (status = 'pending'::text);

-- ----------------------------------------------------------------------------
-- Customers, plans, orders, payments, ledger and audit
-- ----------------------------------------------------------------------------

CREATE TABLE contact_channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    value_ciphertext bytea,
    value_fingerprint bytea NOT NULL,
    verified_at timestamp with time zone,
    transactional_enabled boolean DEFAULT true NOT NULL,
    marketing_enabled boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT contact_channels_pkey PRIMARY KEY (id),
    CONSTRAINT contact_channels_kind_value_fingerprint_key UNIQUE (kind, value_fingerprint),
    CONSTRAINT contact_channels_kind_check CHECK ((kind = ANY (ARRAY['email'::text, 'phone'::text, 'telegram'::text]))),
    CONSTRAINT contact_channels_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX contact_channels_user_idx ON contact_channels USING btree (user_id, created_at DESC);

CREATE TABLE consent_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    purpose text NOT NULL,
    granted boolean NOT NULL,
    policy_version text NOT NULL,
    source text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    request_id text,
    CONSTRAINT consent_records_pkey PRIMARY KEY (id),
    CONSTRAINT consent_records_purpose_check CHECK ((purpose = ANY (ARRAY['terms'::text, 'privacy'::text, 'marketing'::text, 'profiling'::text]))),
    CONSTRAINT consent_records_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX consent_records_user_purpose_idx ON consent_records USING btree (user_id, purpose, occurred_at DESC);

CREATE TABLE customer_lifecycle_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    action text NOT NULL,
    reason text NOT NULL,
    actor_type text NOT NULL,
    actor_id text,
    request_id text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT customer_lifecycle_events_pkey PRIMARY KEY (id),
    CONSTRAINT customer_lifecycle_events_action_known CHECK ((action = ANY (ARRAY['suspended'::text, 'restored'::text, 'deletion_requested'::text, 'deleted'::text, 'anonymized'::text, 'merged_away'::text, 'merged_in'::text]))),
    CONSTRAINT customer_lifecycle_events_actor_type_check CHECK ((actor_type = ANY (ARRAY['customer'::text, 'operator'::text, 'system'::text]))),
    CONSTRAINT customer_lifecycle_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE customer_imports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source text NOT NULL,
    status text DEFAULT 'previewing'::text NOT NULL,
    cursor text,
    total_count integer DEFAULT 0 NOT NULL,
    valid_count integer DEFAULT 0 NOT NULL,
    conflict_count integer DEFAULT 0 NOT NULL,
    invalid_count integer DEFAULT 0 NOT NULL,
    error_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT customer_imports_pkey PRIMARY KEY (id),
    CONSTRAINT customer_imports_conflict_count_check CHECK ((conflict_count >= 0)),
    CONSTRAINT customer_imports_invalid_count_check CHECK ((invalid_count >= 0)),
    CONSTRAINT customer_imports_source_check CHECK ((source = 'remnawave'::text)),
    CONSTRAINT customer_imports_status_check CHECK ((status = ANY (ARRAY['previewing'::text, 'ready'::text, 'applying'::text, 'completed'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT customer_imports_total_count_check CHECK ((total_count >= 0)),
    CONSTRAINT customer_imports_valid_count_check CHECK ((valid_count >= 0))
);

CREATE TABLE customer_import_items (
    import_id uuid NOT NULL,
    source_id text NOT NULL,
    status text NOT NULL,
    fingerprint bytea NOT NULL,
    staged_data jsonb NOT NULL,
    validation_errors jsonb DEFAULT '[]'::jsonb NOT NULL,
    user_id uuid,
    applied_at timestamp with time zone,
    CONSTRAINT customer_import_items_pkey PRIMARY KEY (import_id, source_id),
    CONSTRAINT customer_import_items_status_check CHECK ((status = ANY (ARRAY['valid'::text, 'conflict'::text, 'invalid'::text, 'applied'::text, 'skipped'::text]))),
    CONSTRAINT customer_import_items_import_id_fkey FOREIGN KEY (import_id) REFERENCES customer_imports(id) ON DELETE CASCADE,
    CONSTRAINT customer_import_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE plans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    kind text NOT NULL,
    visible boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    max_concurrent_per_customer integer,
    CONSTRAINT plans_pkey PRIMARY KEY (id),
    CONSTRAINT plans_code_key UNIQUE (code),
    CONSTRAINT plans_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT plans_kind_check CHECK ((kind = ANY (ARRAY['trial'::text, 'one_time'::text, 'recurring'::text, 'free'::text, 'manual'::text]))),
    CONSTRAINT plans_max_concurrent_per_customer_check CHECK (((max_concurrent_per_customer IS NULL) OR (max_concurrent_per_customer > 0)))
);

CREATE TABLE plan_localizations (
    plan_id uuid NOT NULL,
    locale text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    CONSTRAINT plan_localizations_pkey PRIMARY KEY (plan_id, locale),
    CONSTRAINT plan_localizations_description_check CHECK ((char_length(description) <= 2000)),
    CONSTRAINT plan_localizations_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT plan_localizations_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 120))),
    CONSTRAINT plan_localizations_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id)
);

CREATE TABLE plan_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    plan_id uuid NOT NULL,
    version integer NOT NULL,
    billing_period text NOT NULL,
    duration_seconds bigint NOT NULL,
    traffic_allowance_bytes bigint,
    device_limit integer,
    remnawave_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    upgrade_policy text DEFAULT 'extend'::text NOT NULL,
    downgrade_policy text DEFAULT 'at_expiry'::text NOT NULL,
    cancellation_policy text DEFAULT 'at_expiry'::text NOT NULL,
    recurring_capable boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    retired_at timestamp with time zone,
    grace_period_seconds bigint DEFAULT 0 NOT NULL,
    trial_eligibility text DEFAULT 'new_customer'::text NOT NULL,
    squad_selection text DEFAULT 'automatic'::text NOT NULL,
    min_selectable_squads integer DEFAULT 0 NOT NULL,
    max_selectable_squads integer,
    CONSTRAINT plan_versions_pkey PRIMARY KEY (id),
    CONSTRAINT plan_versions_plan_id_version_key UNIQUE (plan_id, version),
    CONSTRAINT plan_versions_billing_period_check CHECK ((billing_period = ANY (ARRAY['none'::text, 'day'::text, 'week'::text, 'month'::text, 'quarter'::text, 'half_year'::text, 'year'::text, 'custom'::text]))),
    CONSTRAINT plan_versions_cancellation_policy_check CHECK ((cancellation_policy = ANY (ARRAY['forbid'::text, 'immediate'::text, 'at_expiry'::text]))),
    CONSTRAINT plan_versions_device_limit_check CHECK (((device_limit IS NULL) OR (device_limit >= 0))),
    CONSTRAINT plan_versions_downgrade_policy_check CHECK ((downgrade_policy = ANY (ARRAY['forbid'::text, 'immediate'::text, 'at_expiry'::text]))),
    CONSTRAINT plan_versions_duration_seconds_check CHECK ((duration_seconds > 0)),
    CONSTRAINT plan_versions_grace_period_seconds_check CHECK ((grace_period_seconds >= 0)),
    CONSTRAINT plan_versions_max_selectable_squads_check CHECK (((max_selectable_squads IS NULL) OR (max_selectable_squads > 0))),
    CONSTRAINT plan_versions_min_selectable_squads_check CHECK ((min_selectable_squads >= 0)),
    CONSTRAINT plan_versions_selectable_squad_range_check CHECK (((max_selectable_squads IS NULL) OR (max_selectable_squads >= min_selectable_squads))),
    CONSTRAINT plan_versions_squad_selection_check CHECK ((squad_selection = ANY (ARRAY['automatic'::text, 'optional'::text, 'required'::text]))),
    CONSTRAINT plan_versions_traffic_allowance_bytes_check CHECK (((traffic_allowance_bytes IS NULL) OR (traffic_allowance_bytes >= 0))),
    CONSTRAINT plan_versions_trial_eligibility_check CHECK ((trial_eligibility = ANY (ARRAY['new_customer'::text, 'never_trialled'::text, 'any'::text]))),
    CONSTRAINT plan_versions_upgrade_policy_check CHECK ((upgrade_policy = ANY (ARRAY['forbid'::text, 'replace'::text, 'extend'::text]))),
    CONSTRAINT plan_versions_version_check CHECK ((version > 0)),
    CONSTRAINT plan_versions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id)
);

CREATE TABLE plan_prices (
    plan_version_id uuid NOT NULL,
    currency text NOT NULL,
    amount_minor bigint NOT NULL,
    CONSTRAINT plan_prices_pkey PRIMARY KEY (plan_version_id, currency),
    CONSTRAINT plan_prices_amount_minor_check CHECK ((amount_minor >= 0)),
    CONSTRAINT plan_prices_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT plan_prices_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id)
);

CREATE TABLE promotions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    kind text NOT NULL,
    value bigint NOT NULL,
    currency text,
    starts_at timestamp with time zone,
    ends_at timestamp with time zone,
    redemption_limit integer,
    per_customer_limit integer DEFAULT 1 NOT NULL,
    eligibility jsonb DEFAULT '{}'::jsonb NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    stackable boolean DEFAULT false NOT NULL,
    precedence integer DEFAULT 0 NOT NULL,
    applies_to text DEFAULT 'plans'::text NOT NULL,
    CONSTRAINT promotions_pkey PRIMARY KEY (id),
    CONSTRAINT promotions_code_key UNIQUE (code),
    CONSTRAINT promotions_applies_to_check CHECK ((applies_to = ANY (ARRAY['plans'::text, 'goods'::text]))),
    CONSTRAINT promotions_check CHECK (((ends_at IS NULL) OR (starts_at IS NULL) OR (ends_at > starts_at))),
    CONSTRAINT promotions_check1 CHECK ((((kind = 'fixed'::text) AND (currency IS NOT NULL)) OR (kind <> 'fixed'::text))),
    CONSTRAINT promotions_check2 CHECK ((((kind = 'percent'::text) AND (value <= 10000)) OR (kind <> 'percent'::text))),
    CONSTRAINT promotions_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT promotions_credit_currency_present CHECK (((kind <> 'wallet_credit'::text) OR (currency IS NOT NULL))),
    CONSTRAINT promotions_currency_check CHECK (((currency IS NULL) OR (currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT promotions_day_reward_bounded CHECK (((kind <> ALL (ARRAY['days'::text, 'trial'::text])) OR (value <= 3650))),
    CONSTRAINT promotions_kind_known CHECK ((kind = ANY (ARRAY['percent'::text, 'fixed'::text, 'wallet_credit'::text, 'days'::text, 'trial'::text]))),
    CONSTRAINT promotions_per_customer_limit_check CHECK ((per_customer_limit > 0)),
    CONSTRAINT promotions_redemption_limit_check CHECK (((redemption_limit IS NULL) OR (redemption_limit > 0))),
    CONSTRAINT promotions_value_check CHECK ((value > 0))
);

COMMENT ON COLUMN promotions.applies_to IS 'Which catalogue this promotion discounts. Existing rows default to plans, so no promotion widens on upgrade.';

CREATE TABLE promotion_plans (
    promotion_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    CONSTRAINT promotion_plans_pkey PRIMARY KEY (promotion_id, plan_id),
    CONSTRAINT promotion_plans_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id),
    CONSTRAINT promotion_plans_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES promotions(id) ON DELETE CASCADE
);

CREATE TABLE promo_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    promotion_id uuid NOT NULL,
    normalized_code text NOT NULL,
    redemption_limit integer,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT promo_codes_pkey PRIMARY KEY (id),
    CONSTRAINT promo_codes_normalized_code_key UNIQUE (normalized_code),
    CONSTRAINT promo_codes_normalized_code_check CHECK ((normalized_code ~ '^[A-Z0-9_-]{3,64}$'::text)),
    CONSTRAINT promo_codes_redemption_limit_check CHECK (((redemption_limit IS NULL) OR (redemption_limit > 0))),
    CONSTRAINT promo_codes_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES promotions(id)
);

CREATE TABLE orders (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    state text DEFAULT 'draft'::text NOT NULL,
    operation text NOT NULL,
    currency text NOT NULL,
    subtotal_minor bigint NOT NULL,
    discount_minor bigint DEFAULT 0 NOT NULL,
    wallet_minor bigint DEFAULT 0 NOT NULL,
    external_minor bigint NOT NULL,
    paid_minor bigint DEFAULT 0 NOT NULL,
    refunded_minor bigint DEFAULT 0 NOT NULL,
    idempotency_key text NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    subscription_id uuid,
    selected_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    paid_at timestamp with time zone,
    CONSTRAINT orders_pkey PRIMARY KEY (id),
    CONSTRAINT orders_user_id_idempotency_key_key UNIQUE (user_id, idempotency_key),
    CONSTRAINT orders_check CHECK ((discount_minor <= subtotal_minor)),
    CONSTRAINT orders_check1 CHECK (((wallet_minor + external_minor) = (subtotal_minor - discount_minor))),
    CONSTRAINT orders_check2 CHECK ((refunded_minor <= paid_minor)),
    CONSTRAINT orders_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT orders_discount_minor_check CHECK ((discount_minor >= 0)),
    CONSTRAINT orders_external_minor_check CHECK ((external_minor >= 0)),
    CONSTRAINT orders_operation_known CHECK ((operation = ANY (ARRAY['purchase'::text, 'upgrade'::text, 'downgrade'::text, 'extension'::text, 'topup'::text, 'addon'::text, 'gift'::text, 'goods'::text, 'renewal'::text, 'code'::text]))),
    CONSTRAINT orders_paid_minor_check CHECK ((paid_minor >= 0)),
    CONSTRAINT orders_refunded_minor_check CHECK ((refunded_minor >= 0)),
    CONSTRAINT orders_state_check CHECK ((state = ANY (ARRAY['draft'::text, 'pending'::text, 'paid'::text, 'fulfilled'::text, 'cancelled'::text, 'expired'::text, 'partially_refunded'::text, 'refunded'::text]))),
    CONSTRAINT orders_subtotal_minor_check CHECK ((subtotal_minor >= 0)),
    CONSTRAINT orders_wallet_minor_check CHECK ((wallet_minor >= 0)),
    CONSTRAINT orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

COMMENT ON COLUMN orders.paid_at IS 'Set once, when the order first reaches a settled state. Never updated afterwards, so a later refund cannot move a sale between reporting periods.';

CREATE INDEX orders_customer_recent_idx ON orders USING btree (user_id, created_at DESC);

CREATE INDEX orders_paid_at_idx ON orders USING btree (paid_at) WHERE (paid_at IS NOT NULL);

CREATE INDEX orders_subscription_idx ON orders USING btree (subscription_id, created_at DESC);

CREATE TABLE order_lines (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_version_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    unit_amount_minor bigint NOT NULL,
    snapshot jsonb NOT NULL,
    CONSTRAINT order_lines_pkey PRIMARY KEY (id),
    CONSTRAINT order_lines_order_id_plan_version_id_key UNIQUE (order_id, plan_version_id),
    CONSTRAINT order_lines_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT order_lines_unit_amount_minor_check CHECK ((unit_amount_minor >= 0)),
    CONSTRAINT order_lines_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT order_lines_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id),
    CONSTRAINT order_lines_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id)
);

CREATE TABLE order_mutations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    action text NOT NULL,
    idempotency_key text NOT NULL,
    reason text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT order_mutations_pkey PRIMARY KEY (id),
    CONSTRAINT order_mutations_order_id_action_idempotency_key_key UNIQUE (order_id, action, idempotency_key),
    CONSTRAINT order_mutations_action_check CHECK ((action = ANY (ARRAY['cancel'::text, 'expire'::text]))),
    CONSTRAINT order_mutations_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE TABLE promo_redemptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    promo_code_id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    user_id uuid NOT NULL,
    order_id uuid NOT NULL,
    discount_minor bigint NOT NULL,
    redeemed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT promo_redemptions_pkey PRIMARY KEY (id),
    CONSTRAINT promo_redemptions_order_id_key UNIQUE (order_id),
    CONSTRAINT promo_redemptions_discount_minor_check CHECK ((discount_minor >= 0)),
    CONSTRAINT promo_redemptions_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT promo_redemptions_promo_code_id_fkey FOREIGN KEY (promo_code_id) REFERENCES promo_codes(id),
    CONSTRAINT promo_redemptions_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES promotions(id),
    CONSTRAINT promo_redemptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX promo_redemptions_limits_idx ON promo_redemptions USING btree (promo_code_id, user_id);

CREATE TABLE payment_intents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    provider text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    provider_reference text,
    checkout_url text,
    idempotency_key text NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    receipt_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_intents_pkey PRIMARY KEY (id),
    CONSTRAINT payment_intents_provider_idempotency_key_key UNIQUE (provider, idempotency_key),
    CONSTRAINT payment_intents_amount_minor_check CHECK ((amount_minor >= 0)),
    CONSTRAINT payment_intents_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_intents_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'telegram_stars'::text, 'cryptobot'::text, 'yookassa'::text, 'heleket'::text, 'mulenpay'::text, 'pal24'::text, 'platega'::text, 'cashera'::text, 'tribute'::text, 'freekassa'::text, 'kassa_ai'::text, 'riopay'::text, 'severpay'::text, 'rollypay'::text, 'aurapay'::text, 'wata'::text, 'cloudpayments'::text, 'lava'::text, 'overpay'::text, 'paritypay'::text, 'payhot'::text, 'cispay'::text, 'antilopay'::text, 'anore'::text, 'donut'::text, 'etoplatezhi'::text, 'jupiter'::text, 'tabpay'::text]))),
    CONSTRAINT payment_intents_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'requires_action'::text, 'processing'::text, 'succeeded'::text, 'failed'::text, 'cancelled'::text, 'expired'::text]))),
    CONSTRAINT payment_intents_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE INDEX payment_intents_order_idx ON payment_intents USING btree (order_id, created_at DESC);

CREATE UNIQUE INDEX payment_intents_provider_reference_idx ON payment_intents USING btree (provider, provider_reference) WHERE (provider_reference IS NOT NULL);

CREATE INDEX payment_intents_provider_reference_lookup_idx ON payment_intents USING btree (provider_reference) WHERE (provider_reference IS NOT NULL);

CREATE TABLE payment_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    payment_intent_id uuid NOT NULL,
    type text NOT NULL,
    previous_status text,
    status text,
    amount_minor bigint,
    currency text,
    provider_event_id text,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_events_pkey PRIMARY KEY (id),
    CONSTRAINT payment_events_payment_intent_id_provider_event_id_type_key UNIQUE NULLS NOT DISTINCT (payment_intent_id, provider_event_id, type),
    CONSTRAINT payment_events_type_check CHECK ((type = ANY (ARRAY['created'::text, 'status_changed'::text, 'amount_mismatch'::text, 'wallet_unavailable'::text, 'duplicate'::text, 'late'::text, 'overpayment'::text, 'underpayment'::text, 'reconciled'::text]))),
    CONSTRAINT payment_events_payment_intent_id_fkey FOREIGN KEY (payment_intent_id) REFERENCES payment_intents(id)
);

CREATE TABLE provider_webhook_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider text NOT NULL,
    provider_event_id text NOT NULL,
    signature_valid boolean NOT NULL,
    body_sha256 bytea NOT NULL,
    raw_body bytea NOT NULL,
    headers jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'received'::text NOT NULL,
    error_code text,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    retain_until timestamp with time zone DEFAULT (now() + '30 days'::interval) NOT NULL,
    CONSTRAINT provider_webhook_events_pkey PRIMARY KEY (id),
    CONSTRAINT provider_webhook_events_provider_provider_event_id_key UNIQUE (provider, provider_event_id),
    CONSTRAINT provider_webhook_events_status_check CHECK ((status = ANY (ARRAY['received'::text, 'processed'::text, 'ignored'::text, 'failed'::text])))
);

CREATE TABLE refunds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    payment_intent_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    provider_reference text,
    reason text NOT NULL,
    idempotency_key text NOT NULL,
    receipt_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT refunds_pkey PRIMARY KEY (id),
    CONSTRAINT refunds_payment_intent_id_idempotency_key_key UNIQUE (payment_intent_id, idempotency_key),
    CONSTRAINT refunds_amount_minor_check CHECK ((amount_minor > 0)),
    CONSTRAINT refunds_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT refunds_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'succeeded'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT refunds_payment_intent_id_fkey FOREIGN KEY (payment_intent_id) REFERENCES payment_intents(id)
);

CREATE INDEX refunds_intent_idx ON refunds USING btree (payment_intent_id, created_at DESC);

CREATE TABLE manual_payment_approvals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    payment_intent_id uuid NOT NULL,
    decision text NOT NULL,
    operator_id text NOT NULL,
    reason text NOT NULL,
    idempotency_key text NOT NULL,
    request_id text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT manual_payment_approvals_pkey PRIMARY KEY (id),
    CONSTRAINT manual_payment_approvals_payment_intent_id_key UNIQUE (payment_intent_id),
    CONSTRAINT manual_payment_approvals_decision_check CHECK ((decision = ANY (ARRAY['approved'::text, 'rejected'::text]))),
    CONSTRAINT manual_payment_approvals_payment_intent_id_fkey FOREIGN KEY (payment_intent_id) REFERENCES payment_intents(id)
);

CREATE TABLE ledger_transactions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    type text NOT NULL,
    reference_type text NOT NULL,
    reference_id text NOT NULL,
    idempotency_key text NOT NULL,
    reason text,
    actor_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ledger_transactions_pkey PRIMARY KEY (id),
    CONSTRAINT ledger_transactions_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT ledger_transactions_reference_type_reference_id_type_key UNIQUE (reference_type, reference_id, type),
    CONSTRAINT ledger_transactions_type_check CHECK ((type = ANY (ARRAY['credit'::text, 'debit'::text, 'payment'::text, 'refund'::text, 'referral_reward'::text, 'correction'::text, 'expiration'::text])))
);

CREATE TABLE ledger_entries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    transaction_id uuid NOT NULL,
    account_type text NOT NULL,
    user_id uuid,
    currency text NOT NULL,
    amount_minor bigint NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ledger_entries_pkey PRIMARY KEY (id),
    CONSTRAINT ledger_entries_transaction_id_account_type_user_id_currency_key UNIQUE (transaction_id, account_type, user_id, currency),
    CONSTRAINT ledger_entries_account_type_check CHECK ((account_type = ANY (ARRAY['customer_wallet'::text, 'platform_clearing'::text, 'provider_clearing'::text]))),
    CONSTRAINT ledger_entries_amount_minor_check CHECK ((amount_minor <> 0)),
    CONSTRAINT ledger_entries_check CHECK ((((account_type = 'customer_wallet'::text) AND (user_id IS NOT NULL)) OR ((account_type = ANY (ARRAY['platform_clearing'::text, 'provider_clearing'::text])) AND (user_id IS NULL)))),
    CONSTRAINT ledger_entries_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT ledger_entries_transaction_id_fkey FOREIGN KEY (transaction_id) REFERENCES ledger_transactions(id),
    CONSTRAINT ledger_entries_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX ledger_entries_balance_idx ON ledger_entries USING btree (user_id, currency, created_at) WHERE (account_type = 'customer_wallet'::text);

CREATE TABLE entitlements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    order_id uuid NOT NULL,
    plan_version_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone NOT NULL,
    traffic_allowance_bytes bigint,
    device_limit integer,
    remnawave_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    remnawave_user_id bigint,
    observed_state jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    subscription_id uuid,
    paused_at timestamp with time zone,
    paused_seconds bigint DEFAULT 0 NOT NULL,
    CONSTRAINT entitlements_pkey PRIMARY KEY (id),
    CONSTRAINT entitlements_order_id_key UNIQUE (order_id),
    CONSTRAINT entitlements_check CHECK ((ends_at > starts_at)),
    CONSTRAINT entitlements_device_limit_check CHECK (((device_limit IS NULL) OR (device_limit >= 0))),
    CONSTRAINT entitlements_pause_consistent CHECK (((status = 'paused'::text) = (paused_at IS NOT NULL))),
    CONSTRAINT entitlements_paused_seconds_check CHECK ((paused_seconds >= 0)),
    CONSTRAINT entitlements_status_known CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'limited'::text, 'disabled'::text, 'paused'::text, 'expired'::text, 'superseded'::text, 'failed'::text]))),
    CONSTRAINT entitlements_traffic_allowance_bytes_check CHECK (((traffic_allowance_bytes IS NULL) OR (traffic_allowance_bytes >= 0))),
    CONSTRAINT entitlements_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT entitlements_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT entitlements_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

COMMENT ON COLUMN entitlements.paused_at IS 'When the current pause began. NULL exactly when the status is not paused; the table enforces the pairing.';

CREATE INDEX entitlements_subscription_idx ON entitlements USING btree (subscription_id, ends_at DESC);

CREATE TABLE fulfillment_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    entitlement_id uuid NOT NULL,
    operation text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    idempotency_key text NOT NULL,
    correlation_id text NOT NULL,
    desired_state jsonb DEFAULT '{}'::jsonb NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT fulfillment_operations_pkey PRIMARY KEY (id),
    CONSTRAINT fulfillment_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT fulfillment_operations_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT fulfillment_operations_operation_known CHECK ((operation = ANY (ARRAY['create'::text, 'extend'::text, 'enable'::text, 'disable'::text, 'reset_traffic'::text, 'set_limits'::text, 'set_squads'::text, 'reconcile'::text, 'pause'::text, 'resume'::text]))),
    CONSTRAINT fulfillment_operations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'retrying'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT fulfillment_operations_entitlement_id_fkey FOREIGN KEY (entitlement_id) REFERENCES entitlements(id)
);

CREATE INDEX fulfillment_operations_pending_idx ON fulfillment_operations USING btree (next_attempt_at) WHERE (status = ANY (ARRAY['pending'::text, 'retrying'::text]));

CREATE TABLE fulfillment_history (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    operation_id uuid NOT NULL,
    status text NOT NULL,
    correlation_id text NOT NULL,
    request_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    response_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_code text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fulfillment_history_pkey PRIMARY KEY (id),
    CONSTRAINT fulfillment_history_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES fulfillment_operations(id)
);

CREATE TABLE entitlement_drifts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    entitlement_id uuid NOT NULL,
    kind text NOT NULL,
    expected jsonb NOT NULL,
    observed jsonb NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT entitlement_drifts_pkey PRIMARY KEY (id),
    CONSTRAINT entitlement_drifts_kind_check CHECK ((kind = ANY (ARRAY['missing_remote'::text, 'external_edit'::text, 'status'::text, 'expiry'::text, 'traffic'::text, 'device_limit'::text, 'squads'::text]))),
    CONSTRAINT entitlement_drifts_status_check CHECK ((status = ANY (ARRAY['open'::text, 'resolved'::text, 'ignored'::text]))),
    CONSTRAINT entitlement_drifts_entitlement_id_fkey FOREIGN KEY (entitlement_id) REFERENCES entitlements(id)
);

CREATE UNIQUE INDEX entitlement_drifts_one_open_kind_idx ON entitlement_drifts USING btree (entitlement_id, kind) WHERE (status = 'open'::text);

CREATE INDEX entitlement_drifts_open_idx ON entitlement_drifts USING btree (detected_at) WHERE (status = 'open'::text);

CREATE TABLE audit_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor_type text NOT NULL,
    actor_id text,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    reason text,
    request_id text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    category text DEFAULT 'system'::text NOT NULL,
    outcome text DEFAULT 'success'::text NOT NULL,
    CONSTRAINT audit_events_pkey PRIMARY KEY (id),
    CONSTRAINT audit_events_actor_type_known CHECK ((actor_type = ANY (ARRAY['customer'::text, 'operator'::text, 'admin'::text, 'system'::text, 'provider'::text]))),
    CONSTRAINT audit_events_category_known CHECK ((category = ANY (ARRAY['authentication'::text, 'authorization'::text, 'configuration'::text, 'customer'::text, 'financial'::text, 'support'::text, 'marketing'::text, 'system'::text, 'risk'::text]))),
    CONSTRAINT audit_events_outcome_known CHECK ((outcome = ANY (ARRAY['success'::text, 'failure'::text, 'denied'::text])))
);

CREATE INDEX audit_events_action_idx ON audit_events USING btree (action, occurred_at DESC);

CREATE INDEX audit_events_actor_idx ON audit_events USING btree (actor_type, actor_id, occurred_at DESC);

CREATE INDEX audit_events_category_idx ON audit_events USING btree (category, occurred_at DESC);

CREATE INDEX audit_events_recent_idx ON audit_events USING btree (occurred_at DESC);

CREATE INDEX audit_events_target_idx ON audit_events USING btree (target_type, target_id, occurred_at DESC);

-- ----------------------------------------------------------------------------
-- Bot checkout, trials, auto-renewal, news and referral rewards
-- ----------------------------------------------------------------------------

CREATE TABLE bot_delivery_state (
    user_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    last_error_code text,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    retry_after timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT bot_delivery_state_pkey PRIMARY KEY (user_id),
    CONSTRAINT bot_delivery_state_consecutive_failures_check CHECK ((consecutive_failures >= 0)),
    CONSTRAINT bot_delivery_state_status_known CHECK ((status = ANY (ARRAY['active'::text, 'failing'::text, 'blocked'::text, 'deactivated'::text, 'not_started'::text]))),
    CONSTRAINT bot_delivery_state_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE bot_checkout_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    plan_version_id uuid NOT NULL,
    operation text NOT NULL,
    currency text NOT NULL,
    provider text,
    promo_code text,
    promo_rejection text,
    apply_wallet boolean DEFAULT true NOT NULL,
    order_id uuid,
    idempotency_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone DEFAULT (now() + '01:00:00'::interval) NOT NULL,
    subscription_id uuid,
    selected_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    CONSTRAINT bot_checkout_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT bot_checkout_sessions_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT bot_checkout_sessions_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT bot_checkout_sessions_operation_check CHECK ((operation = ANY (ARRAY['purchase'::text, 'upgrade'::text, 'downgrade'::text, 'extension'::text]))),
    CONSTRAINT bot_checkout_sessions_promo_code_check CHECK (((promo_code IS NULL) OR (promo_code ~ '^[A-Z0-9_-]{3,64}$'::text))),
    CONSTRAINT bot_checkout_sessions_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'telegram_stars'::text, 'cryptobot'::text, 'yookassa'::text, 'heleket'::text, 'mulenpay'::text, 'pal24'::text, 'platega'::text, 'cashera'::text, 'tribute'::text, 'freekassa'::text, 'kassa_ai'::text, 'riopay'::text, 'severpay'::text, 'rollypay'::text, 'aurapay'::text, 'wata'::text, 'cloudpayments'::text, 'lava'::text, 'overpay'::text, 'paritypay'::text, 'payhot'::text, 'cispay'::text, 'antilopay'::text, 'anore'::text, 'donut'::text, 'etoplatezhi'::text, 'jupiter'::text, 'tabpay'::text]))),
    CONSTRAINT bot_checkout_sessions_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT bot_checkout_sessions_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT bot_checkout_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX bot_checkout_sessions_expiry_idx ON bot_checkout_sessions USING btree (expires_at);

CREATE UNIQUE INDEX bot_checkout_sessions_open_idx ON bot_checkout_sessions USING btree (user_id) WHERE (order_id IS NULL);

CREATE TABLE trial_claims (
    user_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    order_id uuid NOT NULL,
    claimed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT trial_claims_pkey PRIMARY KEY (user_id),
    CONSTRAINT trial_claims_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT trial_claims_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id),
    CONSTRAINT trial_claims_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE auto_renew_settings (
    user_id uuid NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    plan_version_id uuid,
    provider text,
    currency text,
    cancelled_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    subscription_id uuid,
    payment_method_id uuid,
    funding text DEFAULT 'wallet'::text NOT NULL,
    lead_time_seconds bigint DEFAULT 259200 NOT NULL,
    consent_at timestamp with time zone,
    state text DEFAULT 'idle'::text NOT NULL,
    last_attempt_at timestamp with time zone,
    last_failure_code text,
    CONSTRAINT auto_renew_settings_check CHECK (((NOT enabled) OR ((plan_version_id IS NOT NULL) AND (provider IS NOT NULL) AND (currency IS NOT NULL)))),
    CONSTRAINT auto_renew_settings_consent_recorded CHECK (((enabled = false) OR (consent_at IS NOT NULL))),
    CONSTRAINT auto_renew_settings_currency_check CHECK (((currency IS NULL) OR (currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT auto_renew_settings_funding_check CHECK ((funding = ANY (ARRAY['wallet'::text, 'saved_method'::text]))),
    CONSTRAINT auto_renew_settings_lead_time_seconds_check CHECK ((lead_time_seconds >= 0)),
    CONSTRAINT auto_renew_settings_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'telegram_stars'::text, 'cryptobot'::text, 'yookassa'::text, 'heleket'::text, 'mulenpay'::text, 'pal24'::text, 'platega'::text, 'cashera'::text, 'tribute'::text, 'freekassa'::text, 'kassa_ai'::text, 'riopay'::text, 'severpay'::text, 'rollypay'::text, 'aurapay'::text, 'wata'::text, 'cloudpayments'::text, 'lava'::text, 'overpay'::text, 'paritypay'::text, 'payhot'::text, 'cispay'::text, 'antilopay'::text, 'anore'::text, 'donut'::text, 'etoplatezhi'::text, 'jupiter'::text, 'tabpay'::text]))),
    CONSTRAINT auto_renew_settings_saved_method_present CHECK (((funding <> 'saved_method'::text) OR (payment_method_id IS NOT NULL))),
    CONSTRAINT auto_renew_settings_state_check CHECK ((state = ANY (ARRAY['idle'::text, 'scheduled'::text, 'dunning'::text, 'suspended'::text]))),
    CONSTRAINT auto_renew_settings_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT auto_renew_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX auto_renew_settings_target_idx ON auto_renew_settings USING btree (user_id, subscription_id) NULLS NOT DISTINCT;

CREATE TABLE support_attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    message_id bigint NOT NULL,
    kind text NOT NULL,
    telegram_file_id text,
    file_name text,
    mime_type text,
    size_bytes bigint NOT NULL,
    retain_until timestamp with time zone DEFAULT (now() + '90 days'::interval) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    origin text DEFAULT 'telegram'::text NOT NULL,
    storage_key text,
    CONSTRAINT support_attachments_pkey PRIMARY KEY (id),
    CONSTRAINT support_attachments_message_id_telegram_file_id_key UNIQUE (message_id, telegram_file_id),
    CONSTRAINT support_attachments_file_name_check CHECK (((file_name IS NULL) OR (char_length(file_name) <= 200))),
    CONSTRAINT support_attachments_kind_check CHECK ((kind = ANY (ARRAY['photo'::text, 'document'::text]))),
    CONSTRAINT support_attachments_mime_type_check CHECK (((mime_type IS NULL) OR (char_length(mime_type) <= 120))),
    CONSTRAINT support_attachments_origin_known CHECK ((origin = ANY (ARRAY['telegram'::text, 'local'::text]))),
    CONSTRAINT support_attachments_reference_matches_origin CHECK ((((origin = 'telegram'::text) AND (telegram_file_id IS NOT NULL) AND (storage_key IS NULL)) OR ((origin = 'local'::text) AND (storage_key IS NOT NULL) AND (telegram_file_id IS NULL)))),
    CONSTRAINT support_attachments_size_bytes_check CHECK (((size_bytes > 0) AND (size_bytes <= 10485760))),
    CONSTRAINT support_attachments_storage_key_shape CHECK (((storage_key IS NULL) OR (storage_key ~ '^[0-9a-f]{64}$'::text))),
    CONSTRAINT support_attachments_message_id_fkey FOREIGN KEY (message_id) REFERENCES support_messages(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX support_attachments_local_file_idx ON support_attachments USING btree (message_id, storage_key) WHERE (storage_key IS NOT NULL);

CREATE INDEX support_attachments_retention_idx ON support_attachments USING btree (retain_until);

CREATE TABLE news_posts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    category text NOT NULL,
    class text DEFAULT 'transactional'::text NOT NULL,
    published_at timestamp with time zone,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    scheduled_for timestamp with time zone,
    unpublished_at timestamp with time zone,
    archived_at timestamp with time zone,
    created_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT news_posts_pkey PRIMARY KEY (id),
    CONSTRAINT news_posts_slug_key UNIQUE (slug),
    CONSTRAINT news_posts_category_check CHECK ((category = ANY (ARRAY['news'::text, 'announcement'::text, 'incident'::text, 'maintenance'::text]))),
    CONSTRAINT news_posts_check CHECK (((expires_at IS NULL) OR (published_at IS NULL) OR (expires_at > published_at))),
    CONSTRAINT news_posts_class_check CHECK ((class = ANY (ARRAY['transactional'::text, 'marketing'::text]))),
    CONSTRAINT news_posts_schedule_present CHECK (((status <> 'scheduled'::text) OR (scheduled_for IS NOT NULL))),
    CONSTRAINT news_posts_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9_-]{2,63}$'::text)),
    CONSTRAINT news_posts_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'scheduled'::text, 'published'::text, 'unpublished'::text, 'archived'::text])))
);

CREATE INDEX news_posts_published_idx ON news_posts USING btree (published_at DESC) WHERE (published_at IS NOT NULL);

CREATE INDEX news_posts_scheduled_idx ON news_posts USING btree (scheduled_for) WHERE (status = 'scheduled'::text);

CREATE TABLE news_post_localizations (
    post_id uuid NOT NULL,
    locale text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    CONSTRAINT news_post_localizations_pkey PRIMARY KEY (post_id, locale),
    CONSTRAINT news_post_localizations_body_check CHECK (((char_length(body) >= 1) AND (char_length(body) <= 3500))),
    CONSTRAINT news_post_localizations_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT news_post_localizations_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 200))),
    CONSTRAINT news_post_localizations_post_id_fkey FOREIGN KEY (post_id) REFERENCES news_posts(id) ON DELETE CASCADE
);

CREATE TABLE news_reads (
    user_id uuid NOT NULL,
    post_id uuid NOT NULL,
    read_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT news_reads_pkey PRIMARY KEY (user_id, post_id),
    CONSTRAINT news_reads_post_id_fkey FOREIGN KEY (post_id) REFERENCES news_posts(id) ON DELETE CASCADE,
    CONSTRAINT news_reads_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE referral_programs (
    singleton boolean DEFAULT true NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    currency text DEFAULT 'RUB'::text NOT NULL,
    inviter_reward_minor bigint DEFAULT 0 NOT NULL,
    invitee_reward_minor bigint DEFAULT 0 NOT NULL,
    qualification text DEFAULT 'first_paid_order'::text NOT NULL,
    inviter_reward_cap integer,
    attribution_validity_days integer DEFAULT 90 NOT NULL,
    reward_expiry_days integer,
    terms_url text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT referral_programs_pkey PRIMARY KEY (singleton),
    CONSTRAINT referral_programs_attribution_validity_days_check CHECK ((attribution_validity_days > 0)),
    CONSTRAINT referral_programs_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT referral_programs_invitee_reward_minor_check CHECK ((invitee_reward_minor >= 0)),
    CONSTRAINT referral_programs_inviter_reward_cap_check CHECK (((inviter_reward_cap IS NULL) OR (inviter_reward_cap > 0))),
    CONSTRAINT referral_programs_inviter_reward_minor_check CHECK ((inviter_reward_minor >= 0)),
    CONSTRAINT referral_programs_qualification_check CHECK ((qualification = 'first_paid_order'::text)),
    CONSTRAINT referral_programs_reward_expiry_days_check CHECK (((reward_expiry_days IS NULL) OR (reward_expiry_days > 0))),
    CONSTRAINT referral_programs_singleton_check CHECK (singleton)
);

CREATE TABLE referral_rewards (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    referred_user_id uuid NOT NULL,
    beneficiary_user_id uuid NOT NULL,
    role text NOT NULL,
    order_id uuid NOT NULL,
    amount_minor bigint NOT NULL,
    currency text NOT NULL,
    ledger_transaction_id uuid NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    reversed_at timestamp with time zone,
    reversed_by uuid,
    reversal_reason text,
    reversal_ledger_transaction_id uuid,
    CONSTRAINT referral_rewards_pkey PRIMARY KEY (id),
    CONSTRAINT referral_rewards_referred_user_id_role_key UNIQUE (referred_user_id, role),
    CONSTRAINT referral_rewards_amount_minor_check CHECK ((amount_minor > 0)),
    CONSTRAINT referral_rewards_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT referral_rewards_reversal_complete CHECK (((reversed_at IS NULL) = (reversal_ledger_transaction_id IS NULL))),
    CONSTRAINT referral_rewards_reversal_reason_check CHECK (((reversal_reason IS NULL) OR (char_length(reversal_reason) <= 400))),
    CONSTRAINT referral_rewards_role_check CHECK ((role = ANY (ARRAY['inviter'::text, 'invitee'::text]))),
    CONSTRAINT referral_rewards_beneficiary_user_id_fkey FOREIGN KEY (beneficiary_user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT referral_rewards_ledger_transaction_id_fkey FOREIGN KEY (ledger_transaction_id) REFERENCES ledger_transactions(id),
    CONSTRAINT referral_rewards_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT referral_rewards_referred_user_id_fkey FOREIGN KEY (referred_user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT referral_rewards_reversal_ledger_transaction_id_fkey FOREIGN KEY (reversal_ledger_transaction_id) REFERENCES ledger_transactions(id)
);

CREATE INDEX referral_rewards_beneficiary_idx ON referral_rewards USING btree (beneficiary_user_id, granted_at DESC);

-- ----------------------------------------------------------------------------
-- Subscriptions, add-ons, wallet, carts, backups and maintenance
-- ----------------------------------------------------------------------------

CREATE TABLE subscriptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    slot integer NOT NULL,
    label text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    remnawave_user_id bigint,
    remnawave_username text,
    observed_state jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    CONSTRAINT subscriptions_pkey PRIMARY KEY (id),
    CONSTRAINT subscriptions_user_id_slot_key UNIQUE (user_id, slot),
    CONSTRAINT subscriptions_label_check CHECK (((char_length(label) >= 1) AND (char_length(label) <= 40))),
    CONSTRAINT subscriptions_remnawave_user_id_check CHECK (((remnawave_user_id IS NULL) OR (remnawave_user_id > 0))),
    CONSTRAINT subscriptions_remnawave_username_check CHECK (((remnawave_username IS NULL) OR ((char_length(remnawave_username) >= 1) AND (char_length(remnawave_username) <= 64)))),
    CONSTRAINT subscriptions_slot_check CHECK ((slot > 0)),
    CONSTRAINT subscriptions_status_check CHECK ((status = ANY (ARRAY['active'::text, 'closed'::text]))),
    CONSTRAINT subscriptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX subscriptions_active_idx ON subscriptions USING btree (user_id, slot) WHERE (status = 'active'::text);

CREATE UNIQUE INDEX subscriptions_remnawave_user_idx ON subscriptions USING btree (remnawave_user_id) WHERE (remnawave_user_id IS NOT NULL);

CREATE INDEX subscriptions_username_idx ON subscriptions USING btree (remnawave_username) WHERE (remnawave_username IS NOT NULL);

CREATE TABLE plan_version_squads (
    plan_version_id uuid NOT NULL,
    squad_id uuid NOT NULL,
    label_ru text NOT NULL,
    label_en text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    CONSTRAINT plan_version_squads_pkey PRIMARY KEY (plan_version_id, squad_id),
    CONSTRAINT plan_version_squads_label_en_check CHECK (((char_length(label_en) >= 1) AND (char_length(label_en) <= 80))),
    CONSTRAINT plan_version_squads_label_ru_check CHECK (((char_length(label_ru) >= 1) AND (char_length(label_ru) <= 80))),
    CONSTRAINT plan_version_squads_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id)
);

CREATE TABLE addons (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    kind text NOT NULL,
    visible boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT addons_pkey PRIMARY KEY (id),
    CONSTRAINT addons_code_key UNIQUE (code),
    CONSTRAINT addons_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT addons_kind_check CHECK ((kind = ANY (ARRAY['traffic'::text, 'devices'::text, 'squads'::text])))
);

CREATE TABLE addon_localizations (
    addon_id uuid NOT NULL,
    locale text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    CONSTRAINT addon_localizations_pkey PRIMARY KEY (addon_id, locale),
    CONSTRAINT addon_localizations_description_check CHECK ((char_length(description) <= 2000)),
    CONSTRAINT addon_localizations_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT addon_localizations_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 120))),
    CONSTRAINT addon_localizations_addon_id_fkey FOREIGN KEY (addon_id) REFERENCES addons(id)
);

CREATE TABLE addon_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    addon_id uuid NOT NULL,
    version integer NOT NULL,
    traffic_bytes bigint,
    device_slots integer,
    remnawave_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    max_quantity integer DEFAULT 1 NOT NULL,
    proration text DEFAULT 'full_price'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT addon_versions_pkey PRIMARY KEY (id),
    CONSTRAINT addon_versions_addon_id_version_key UNIQUE (addon_id, version),
    CONSTRAINT addon_versions_check CHECK (((traffic_bytes IS NOT NULL) OR (device_slots IS NOT NULL) OR (cardinality(remnawave_squad_ids) > 0))),
    CONSTRAINT addon_versions_device_slots_check CHECK (((device_slots IS NULL) OR (device_slots > 0))),
    CONSTRAINT addon_versions_max_quantity_check CHECK ((max_quantity > 0)),
    CONSTRAINT addon_versions_proration_check CHECK ((proration = ANY (ARRAY['full_price'::text, 'remaining_period'::text, 'daily_rate'::text]))),
    CONSTRAINT addon_versions_traffic_bytes_check CHECK (((traffic_bytes IS NULL) OR (traffic_bytes > 0))),
    CONSTRAINT addon_versions_version_check CHECK ((version > 0)),
    CONSTRAINT addon_versions_addon_id_fkey FOREIGN KEY (addon_id) REFERENCES addons(id)
);

CREATE TABLE addon_prices (
    addon_version_id uuid NOT NULL,
    currency text NOT NULL,
    amount_minor bigint NOT NULL,
    CONSTRAINT addon_prices_pkey PRIMARY KEY (addon_version_id, currency),
    CONSTRAINT addon_prices_amount_minor_check CHECK ((amount_minor >= 0)),
    CONSTRAINT addon_prices_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT addon_prices_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id)
);

CREATE TABLE plan_version_addons (
    plan_version_id uuid NOT NULL,
    addon_id uuid NOT NULL,
    CONSTRAINT plan_version_addons_pkey PRIMARY KEY (plan_version_id, addon_id),
    CONSTRAINT plan_version_addons_addon_id_fkey FOREIGN KEY (addon_id) REFERENCES addons(id),
    CONSTRAINT plan_version_addons_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id)
);

CREATE TABLE order_addon_lines (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    addon_id uuid NOT NULL,
    addon_version_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    unit_amount_minor bigint NOT NULL,
    charged_minor bigint NOT NULL,
    proration text NOT NULL,
    snapshot jsonb NOT NULL,
    CONSTRAINT order_addon_lines_pkey PRIMARY KEY (id),
    CONSTRAINT order_addon_lines_order_id_addon_version_id_key UNIQUE (order_id, addon_version_id),
    CONSTRAINT order_addon_lines_charged_minor_check CHECK ((charged_minor >= 0)),
    CONSTRAINT order_addon_lines_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT order_addon_lines_unit_amount_minor_check CHECK ((unit_amount_minor >= 0)),
    CONSTRAINT order_addon_lines_addon_id_fkey FOREIGN KEY (addon_id) REFERENCES addons(id),
    CONSTRAINT order_addon_lines_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id),
    CONSTRAINT order_addon_lines_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE TABLE entitlement_addons (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    entitlement_id uuid NOT NULL,
    order_id uuid NOT NULL,
    addon_version_id uuid NOT NULL,
    quantity integer NOT NULL,
    traffic_bytes bigint,
    device_slots integer,
    remnawave_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT entitlement_addons_pkey PRIMARY KEY (id),
    CONSTRAINT entitlement_addons_order_id_addon_version_id_key UNIQUE (order_id, addon_version_id),
    CONSTRAINT entitlement_addons_device_slots_check CHECK (((device_slots IS NULL) OR (device_slots >= 0))),
    CONSTRAINT entitlement_addons_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT entitlement_addons_traffic_bytes_check CHECK (((traffic_bytes IS NULL) OR (traffic_bytes >= 0))),
    CONSTRAINT entitlement_addons_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id),
    CONSTRAINT entitlement_addons_entitlement_id_fkey FOREIGN KEY (entitlement_id) REFERENCES entitlements(id),
    CONSTRAINT entitlement_addons_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id)
);

CREATE INDEX entitlement_addons_entitlement_idx ON entitlement_addons USING btree (entitlement_id, applied_at);

CREATE TABLE wallet_topups (
    order_id uuid NOT NULL,
    user_id uuid NOT NULL,
    currency text NOT NULL,
    requested_minor bigint NOT NULL,
    credited_minor bigint DEFAULT 0 NOT NULL,
    ledger_transaction_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    credited_at timestamp with time zone,
    CONSTRAINT wallet_topups_pkey PRIMARY KEY (order_id),
    CONSTRAINT wallet_topups_check CHECK (((ledger_transaction_id IS NULL) = (credited_at IS NULL))),
    CONSTRAINT wallet_topups_credited_minor_check CHECK ((credited_minor >= 0)),
    CONSTRAINT wallet_topups_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT wallet_topups_requested_minor_check CHECK ((requested_minor > 0)),
    CONSTRAINT wallet_topups_ledger_transaction_id_fkey FOREIGN KEY (ledger_transaction_id) REFERENCES ledger_transactions(id),
    CONSTRAINT wallet_topups_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT wallet_topups_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX wallet_topups_history_idx ON wallet_topups USING btree (user_id, created_at DESC);

CREATE INDEX wallet_topups_window_idx ON wallet_topups USING btree (user_id, currency, credited_at) WHERE (credited_at IS NOT NULL);

CREATE TABLE carts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    subscription_id uuid,
    plan_version_id uuid,
    operation text NOT NULL,
    currency text NOT NULL,
    promo_code text,
    selected_squad_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    auto_purchase boolean DEFAULT true NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    idempotency_key text NOT NULL,
    order_id uuid,
    last_failure text,
    attempt_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone DEFAULT (now() + '30 days'::interval) NOT NULL,
    kind text DEFAULT 'plan'::text NOT NULL,
    CONSTRAINT carts_pkey PRIMARY KEY (id),
    CONSTRAINT carts_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT carts_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT carts_check CHECK (((status = 'purchased'::text) = (order_id IS NOT NULL))),
    CONSTRAINT carts_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT carts_kind_check CHECK ((kind = ANY (ARRAY['plan'::text, 'goods'::text]))),
    CONSTRAINT carts_operation_check CHECK ((operation = ANY (ARRAY['purchase'::text, 'upgrade'::text, 'downgrade'::text, 'extension'::text]))),
    CONSTRAINT carts_plan_shape CHECK (((kind = 'plan'::text) = (plan_version_id IS NOT NULL))),
    CONSTRAINT carts_promo_code_check CHECK (((promo_code IS NULL) OR (promo_code ~ '^[A-Z0-9_-]{3,64}$'::text))),
    CONSTRAINT carts_status_check CHECK ((status = ANY (ARRAY['open'::text, 'purchased'::text, 'cancelled'::text, 'expired'::text]))),
    CONSTRAINT carts_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT carts_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT carts_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id),
    CONSTRAINT carts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX carts_expiry_idx ON carts USING btree (expires_at) WHERE (status = 'open'::text);

CREATE UNIQUE INDEX carts_one_open_per_customer_idx ON carts USING btree (user_id) WHERE (status = 'open'::text);

CREATE TABLE cart_addons (
    cart_id uuid NOT NULL,
    addon_version_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    CONSTRAINT cart_addons_pkey PRIMARY KEY (cart_id, addon_version_id),
    CONSTRAINT cart_addons_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT cart_addons_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id),
    CONSTRAINT cart_addons_cart_id_fkey FOREIGN KEY (cart_id) REFERENCES carts(id) ON DELETE CASCADE
);

CREATE TABLE bot_checkout_addons (
    checkout_id uuid NOT NULL,
    addon_version_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    CONSTRAINT bot_checkout_addons_pkey PRIMARY KEY (checkout_id, addon_version_id),
    CONSTRAINT bot_checkout_addons_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT bot_checkout_addons_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id),
    CONSTRAINT bot_checkout_addons_checkout_id_fkey FOREIGN KEY (checkout_id) REFERENCES bot_checkout_sessions(id) ON DELETE CASCADE
);

CREATE TABLE operator_topics (
    kind text NOT NULL,
    chat_id bigint NOT NULL,
    topic_id bigint,
    status text DEFAULT 'pending'::text NOT NULL,
    last_error_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT operator_topics_pkey PRIMARY KEY (kind),
    CONSTRAINT operator_topics_check CHECK (((status = 'bound'::text) = (topic_id IS NOT NULL))),
    CONSTRAINT operator_topics_kind_known CHECK ((kind = ANY (ARRAY['purchase'::text, 'renewal'::text, 'topup'::text, 'refund'::text, 'fulfillment_failure'::text, 'incident'::text, 'backup'::text, 'security'::text, 'anomaly'::text, 'campaign_test'::text, 'notice_test'::text, 'support'::text]))),
    CONSTRAINT operator_topics_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'bound'::text, 'failed'::text]))),
    CONSTRAINT operator_topics_topic_id_check CHECK (((topic_id IS NULL) OR (topic_id > 0)))
);

CREATE TABLE operator_notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    dedupe_key text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sent_at timestamp with time zone,
    CONSTRAINT operator_notifications_pkey PRIMARY KEY (id),
    CONSTRAINT operator_notifications_kind_dedupe_key_key UNIQUE (kind, dedupe_key),
    CONSTRAINT operator_notifications_kind_known CHECK ((kind = ANY (ARRAY['purchase'::text, 'renewal'::text, 'topup'::text, 'refund'::text, 'fulfillment_failure'::text, 'incident'::text, 'backup'::text, 'security'::text, 'anomaly'::text, 'support'::text]))),
    CONSTRAINT operator_notifications_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'suppressed'::text, 'failed'::text])))
);

CREATE INDEX operator_notifications_pending_idx ON operator_notifications USING btree (created_at) WHERE (status = 'pending'::text);

CREATE INDEX operator_notifications_volume_idx ON operator_notifications USING btree (kind, sent_at DESC) WHERE (status = 'sent'::text);

CREATE TABLE backups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text DEFAULT 'scheduled'::text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    file_name text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    sha256 bytea,
    encrypted boolean DEFAULT true NOT NULL,
    verified_at timestamp with time zone,
    error_code text,
    requested_by text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    retain_until timestamp with time zone NOT NULL,
    CONSTRAINT backups_pkey PRIMARY KEY (id),
    CONSTRAINT backups_file_name_key UNIQUE (file_name),
    CONSTRAINT backups_file_name_check CHECK ((file_name ~ '^[A-Za-z0-9._-]{1,128}$'::text)),
    CONSTRAINT backups_kind_check CHECK ((kind = ANY (ARRAY['scheduled'::text, 'manual'::text]))),
    CONSTRAINT backups_size_bytes_check CHECK ((size_bytes >= 0)),
    CONSTRAINT backups_status_check CHECK ((status = ANY (ARRAY['running'::text, 'completed'::text, 'failed'::text, 'pruned'::text])))
);

CREATE INDEX backups_recent_idx ON backups USING btree (started_at DESC);

CREATE INDEX backups_retention_idx ON backups USING btree (retain_until) WHERE (status = 'completed'::text);

CREATE TABLE backup_restores (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    backup_id uuid NOT NULL,
    status text DEFAULT 'requested'::text NOT NULL,
    operator_id text NOT NULL,
    reason text NOT NULL,
    error_code text,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT backup_restores_pkey PRIMARY KEY (id),
    CONSTRAINT backup_restores_status_check CHECK ((status = ANY (ARRAY['requested'::text, 'running'::text, 'completed'::text, 'failed'::text]))),
    CONSTRAINT backup_restores_backup_id_fkey FOREIGN KEY (backup_id) REFERENCES backups(id)
);

CREATE INDEX backup_restores_backup_idx ON backup_restores USING btree (backup_id, requested_at DESC);

CREATE TABLE maintenance_state (
    singleton boolean DEFAULT true NOT NULL,
    active boolean DEFAULT false NOT NULL,
    source text DEFAULT 'manual'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    notice_ru text DEFAULT ''::text NOT NULL,
    notice_en text DEFAULT ''::text NOT NULL,
    expected_return_at timestamp with time zone,
    activated_at timestamp with time zone,
    cleared_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT maintenance_state_pkey PRIMARY KEY (singleton),
    CONSTRAINT maintenance_state_check CHECK (((NOT active) OR (activated_at IS NOT NULL))),
    CONSTRAINT maintenance_state_notice_en_check CHECK ((char_length(notice_en) <= 1000)),
    CONSTRAINT maintenance_state_notice_ru_check CHECK ((char_length(notice_ru) <= 1000)),
    CONSTRAINT maintenance_state_reason_check CHECK ((char_length(reason) <= 200)),
    CONSTRAINT maintenance_state_singleton_check CHECK (singleton),
    CONSTRAINT maintenance_state_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'remnawave'::text, 'database'::text, 'valkey'::text])))
);

CREATE TABLE maintenance_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    action text NOT NULL,
    source text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    actor_type text NOT NULL,
    actor_id text,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT maintenance_events_pkey PRIMARY KEY (id),
    CONSTRAINT maintenance_events_action_check CHECK ((action = ANY (ARRAY['activated'::text, 'cleared'::text]))),
    CONSTRAINT maintenance_events_actor_type_check CHECK ((actor_type = ANY (ARRAY['operator'::text, 'system'::text]))),
    CONSTRAINT maintenance_events_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'remnawave'::text, 'database'::text, 'valkey'::text])))
);

CREATE INDEX maintenance_events_recent_idx ON maintenance_events USING btree (occurred_at DESC);

-- ----------------------------------------------------------------------------
-- Operators
-- ----------------------------------------------------------------------------

CREATE TABLE admin_users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    email_normalized text NOT NULL,
    display_name text NOT NULL,
    password_hash text,
    status text DEFAULT 'active'::text NOT NULL,
    locale text DEFAULT 'en'::text NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    preferences jsonb DEFAULT '{}'::jsonb NOT NULL,
    totp_secret_ciphertext bytea,
    totp_confirmed_at timestamp with time zone,
    failed_login_count integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    password_changed_at timestamp with time zone,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    disabled_at timestamp with time zone,
    totp_failed_count integer DEFAULT 0 NOT NULL,
    totp_last_used_step bigint,
    CONSTRAINT admin_users_pkey PRIMARY KEY (id),
    CONSTRAINT admin_users_email_normalized_key UNIQUE (email_normalized),
    CONSTRAINT admin_users_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 80))),
    CONSTRAINT admin_users_email_check CHECK (((char_length(email) >= 3) AND (char_length(email) <= 254))),
    CONSTRAINT admin_users_email_normalized_check CHECK (((char_length(email_normalized) >= 3) AND (char_length(email_normalized) <= 254))),
    CONSTRAINT admin_users_failed_login_count_check CHECK ((failed_login_count >= 0)),
    CONSTRAINT admin_users_locale_check CHECK ((locale = ANY (ARRAY['en'::text, 'ru'::text]))),
    CONSTRAINT admin_users_password_hash_check CHECK (((password_hash IS NULL) OR ((char_length(password_hash) >= 16) AND (char_length(password_hash) <= 512)))),
    CONSTRAINT admin_users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'disabled'::text]))),
    CONSTRAINT admin_users_totp_enrolment_complete CHECK (((totp_confirmed_at IS NULL) OR (totp_secret_ciphertext IS NOT NULL))),
    CONSTRAINT admin_users_totp_failed_count_nonnegative CHECK ((totp_failed_count >= 0)),
    CONSTRAINT admin_users_totp_last_used_step_nonnegative CHECK (((totp_last_used_step IS NULL) OR (totp_last_used_step >= 0)))
);

CREATE INDEX admin_users_active_idx ON admin_users USING btree (created_at DESC) WHERE (status = 'active'::text);

CREATE TABLE admin_user_roles (
    admin_user_id uuid NOT NULL,
    role text NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    granted_by uuid,
    CONSTRAINT admin_user_roles_pkey PRIMARY KEY (admin_user_id, role),
    CONSTRAINT admin_user_roles_role_check CHECK ((role = ANY (ARRAY['owner'::text, 'administrator'::text, 'support'::text, 'finance'::text, 'marketing'::text, 'auditor'::text]))),
    CONSTRAINT admin_user_roles_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE,
    CONSTRAINT admin_user_roles_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES admin_users(id)
);

CREATE INDEX admin_user_roles_owners_idx ON admin_user_roles USING btree (admin_user_id) WHERE (role = 'owner'::text);

CREATE TABLE admin_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    csrf_secret bytea NOT NULL,
    pending_totp boolean DEFAULT false NOT NULL,
    auth_methods text[] DEFAULT '{}'::text[] NOT NULL,
    ip inet,
    user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone DEFAULT now() NOT NULL,
    idle_expires_at timestamp with time zone NOT NULL,
    absolute_expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    revoked_reason text,
    challenge_failures integer DEFAULT 0 NOT NULL,
    CONSTRAINT admin_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT admin_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT admin_sessions_absolute_after_idle CHECK ((absolute_expires_at >= created_at)),
    CONSTRAINT admin_sessions_auth_methods_known CHECK ((auth_methods <@ ARRAY['password'::text, 'totp'::text, 'recovery_code'::text, 'oidc'::text, 'passkey'::text])),
    CONSTRAINT admin_sessions_challenge_failures_nonnegative CHECK ((challenge_failures >= 0)),
    CONSTRAINT admin_sessions_csrf_secret_check CHECK ((octet_length(csrf_secret) = 32)),
    CONSTRAINT admin_sessions_revocation_complete CHECK (((revoked_at IS NULL) = (revoked_reason IS NULL))),
    CONSTRAINT admin_sessions_revoked_reason_known CHECK (((revoked_reason IS NULL) OR (revoked_reason = ANY (ARRAY['logout'::text, 'logout_all'::text, 'password_change'::text, 'admin_revoked'::text, 'expired'::text, 'challenge_failed'::text])))),
    CONSTRAINT admin_sessions_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT admin_sessions_user_agent_check CHECK (((user_agent IS NULL) OR (char_length(user_agent) <= 400))),
    CONSTRAINT admin_sessions_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

CREATE INDEX admin_sessions_expiry_idx ON admin_sessions USING btree (absolute_expires_at) WHERE (revoked_at IS NULL);

CREATE INDEX admin_sessions_live_idx ON admin_sessions USING btree (admin_user_id, last_seen_at DESC) WHERE (revoked_at IS NULL);

CREATE TABLE admin_recovery_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_user_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    used_at timestamp with time zone,
    CONSTRAINT admin_recovery_codes_pkey PRIMARY KEY (id),
    CONSTRAINT admin_recovery_codes_admin_user_id_code_hash_key UNIQUE (admin_user_id, code_hash),
    CONSTRAINT admin_recovery_codes_code_hash_check CHECK ((octet_length(code_hash) = 32)),
    CONSTRAINT admin_recovery_codes_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

CREATE INDEX admin_recovery_codes_unused_idx ON admin_recovery_codes USING btree (admin_user_id) WHERE (used_at IS NULL);

CREATE TABLE admin_password_resets (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    requested_ip inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    CONSTRAINT admin_password_resets_pkey PRIMARY KEY (id),
    CONSTRAINT admin_password_resets_token_hash_key UNIQUE (token_hash),
    CONSTRAINT admin_password_resets_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT admin_password_resets_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT admin_password_resets_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

CREATE INDEX admin_password_resets_pending_idx ON admin_password_resets USING btree (admin_user_id, created_at DESC) WHERE (used_at IS NULL);

CREATE TABLE admin_setup_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    token_hash bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    consumed_by uuid,
    CONSTRAINT admin_setup_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT admin_setup_tokens_token_hash_key UNIQUE (token_hash),
    CONSTRAINT admin_setup_tokens_consumption_complete CHECK (((consumed_at IS NULL) = (consumed_by IS NULL))),
    CONSTRAINT admin_setup_tokens_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT admin_setup_tokens_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT admin_setup_tokens_consumed_by_fkey FOREIGN KEY (consumed_by) REFERENCES admin_users(id)
);

CREATE TABLE admin_oidc_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    display_name text NOT NULL,
    issuer text NOT NULL,
    discovery_url text NOT NULL,
    client_id text NOT NULL,
    client_secret_ciphertext bytea,
    scopes text[] DEFAULT '{openid,email,profile}'::text[] NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    require_verified_email boolean DEFAULT true NOT NULL,
    allow_auto_provision boolean DEFAULT false NOT NULL,
    auto_provision_role text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT admin_oidc_providers_pkey PRIMARY KEY (id),
    CONSTRAINT admin_oidc_providers_slug_key UNIQUE (slug),
    CONSTRAINT admin_oidc_providers_auto_provision_role_check CHECK (((auto_provision_role IS NULL) OR (auto_provision_role = ANY (ARRAY['administrator'::text, 'support'::text, 'finance'::text, 'marketing'::text, 'auditor'::text])))),
    CONSTRAINT admin_oidc_providers_auto_provision_role_present CHECK (((allow_auto_provision = false) OR (auto_provision_role IS NOT NULL))),
    CONSTRAINT admin_oidc_providers_client_id_check CHECK (((char_length(client_id) >= 1) AND (char_length(client_id) <= 256))),
    CONSTRAINT admin_oidc_providers_discovery_url_check CHECK ((discovery_url ~ '^https://'::text)),
    CONSTRAINT admin_oidc_providers_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 80))),
    CONSTRAINT admin_oidc_providers_issuer_check CHECK ((issuer ~ '^https://'::text)),
    CONSTRAINT admin_oidc_providers_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$'::text))
);

CREATE TABLE admin_oidc_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_user_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    subject text NOT NULL,
    linked_at timestamp with time zone DEFAULT now() NOT NULL,
    last_login_at timestamp with time zone,
    issuer text NOT NULL,
    CONSTRAINT admin_oidc_identities_pkey PRIMARY KEY (id),
    CONSTRAINT admin_oidc_identities_admin_user_id_provider_id_key UNIQUE (admin_user_id, provider_id),
    CONSTRAINT admin_oidc_identities_provider_id_subject_key UNIQUE (provider_id, subject),
    CONSTRAINT admin_oidc_identities_issuer_https CHECK ((issuer ~ '^https://'::text)),
    CONSTRAINT admin_oidc_identities_subject_check CHECK (((char_length(subject) >= 1) AND (char_length(subject) <= 256))),
    CONSTRAINT admin_oidc_identities_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE,
    CONSTRAINT admin_oidc_identities_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES admin_oidc_providers(id) ON DELETE CASCADE
);

-- ----------------------------------------------------------------------------
-- Payment settings, goods, gifts, blocklists and bulk operations
-- ----------------------------------------------------------------------------

CREATE TABLE commerce_settings (
    singleton boolean DEFAULT true NOT NULL,
    topup_enabled boolean DEFAULT true NOT NULL,
    topup_currency text DEFAULT 'RUB'::text NOT NULL,
    topup_presets_minor bigint[] DEFAULT '{}'::bigint[] NOT NULL,
    topup_minimum_minor bigint DEFAULT 10000 NOT NULL,
    topup_maximum_minor bigint DEFAULT 5000000 NOT NULL,
    topup_window_seconds bigint DEFAULT 86400 NOT NULL,
    topup_window_limit_minor bigint DEFAULT 10000000 NOT NULL,
    multi_subscription_enabled boolean DEFAULT false NOT NULL,
    max_subscriptions_per_customer integer DEFAULT 3 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    channel_grace_seconds bigint DEFAULT 259200 NOT NULL,
    channel_recheck_seconds bigint DEFAULT 21600 NOT NULL,
    CONSTRAINT commerce_settings_pkey PRIMARY KEY (singleton),
    CONSTRAINT commerce_settings_channel_grace_seconds_check CHECK ((channel_grace_seconds >= 0)),
    CONSTRAINT commerce_settings_channel_recheck_seconds_check CHECK ((channel_recheck_seconds >= 300)),
    CONSTRAINT commerce_settings_max_subscriptions_per_customer_check CHECK ((max_subscriptions_per_customer > 0)),
    CONSTRAINT commerce_settings_singleton_check CHECK (singleton),
    CONSTRAINT commerce_settings_topup_currency_check CHECK ((topup_currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT commerce_settings_topup_maximum_minor_check CHECK ((topup_maximum_minor > 0)),
    CONSTRAINT commerce_settings_topup_minimum_minor_check CHECK ((topup_minimum_minor > 0)),
    CONSTRAINT commerce_settings_topup_range CHECK ((topup_minimum_minor <= topup_maximum_minor)),
    CONSTRAINT commerce_settings_topup_window_limit_minor_check CHECK ((topup_window_limit_minor >= 0)),
    CONSTRAINT commerce_settings_topup_window_seconds_check CHECK ((topup_window_seconds > 0)),
    CONSTRAINT commerce_settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE payment_provider_settings (
    provider text NOT NULL,
    merchant_id text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    display_order integer DEFAULT 0 NOT NULL,
    credentials_ciphertext bytea,
    webhook_secret_ciphertext bytea,
    recurring_enabled boolean DEFAULT false NOT NULL,
    recurring_test_status text DEFAULT 'untested'::text NOT NULL,
    recurring_tested_at timestamp with time zone,
    connection_status text DEFAULT 'unknown'::text NOT NULL,
    connection_checked_at timestamp with time zone,
    connection_error_code text,
    webhook_status text DEFAULT 'unknown'::text NOT NULL,
    webhook_last_event_at timestamp with time zone,
    webhook_last_error_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT payment_provider_settings_pkey PRIMARY KEY (provider, merchant_id),
    CONSTRAINT payment_provider_settings_connection_status_check CHECK ((connection_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'failing'::text]))),
    CONSTRAINT payment_provider_settings_merchant_id_check CHECK ((char_length(merchant_id) <= 64)),
    CONSTRAINT payment_provider_settings_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'telegram_stars'::text, 'cryptobot'::text, 'yookassa'::text, 'heleket'::text, 'mulenpay'::text, 'pal24'::text, 'platega'::text, 'cashera'::text, 'tribute'::text, 'freekassa'::text, 'kassa_ai'::text, 'riopay'::text, 'severpay'::text, 'rollypay'::text, 'aurapay'::text, 'wata'::text, 'cloudpayments'::text, 'lava'::text, 'overpay'::text, 'paritypay'::text, 'payhot'::text, 'cispay'::text, 'antilopay'::text, 'anore'::text, 'donut'::text, 'etoplatezhi'::text, 'jupiter'::text, 'tabpay'::text]))),
    CONSTRAINT payment_provider_settings_recurring_test_status_check CHECK ((recurring_test_status = ANY (ARRAY['untested'::text, 'passed'::text, 'failed'::text]))),
    CONSTRAINT payment_provider_settings_recurring_tested CHECK (((recurring_enabled = false) OR (recurring_test_status = 'passed'::text))),
    CONSTRAINT payment_provider_settings_webhook_status_check CHECK ((webhook_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'failing'::text]))),
    CONSTRAINT payment_provider_settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE payment_methods (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    merchant_id text DEFAULT ''::text NOT NULL,
    provider_token text NOT NULL,
    display_label text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    consent_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT payment_methods_pkey PRIMARY KEY (id),
    CONSTRAINT payment_methods_provider_merchant_id_provider_token_key UNIQUE (provider, merchant_id, provider_token),
    CONSTRAINT payment_methods_display_label_check CHECK ((char_length(display_label) <= 64)),
    CONSTRAINT payment_methods_merchant_id_check CHECK ((char_length(merchant_id) <= 64)),
    CONSTRAINT payment_methods_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'telegram_stars'::text, 'cryptobot'::text, 'yookassa'::text, 'heleket'::text, 'mulenpay'::text, 'pal24'::text, 'platega'::text, 'cashera'::text, 'tribute'::text, 'freekassa'::text, 'kassa_ai'::text, 'riopay'::text, 'severpay'::text, 'rollypay'::text, 'aurapay'::text, 'wata'::text, 'cloudpayments'::text, 'lava'::text, 'overpay'::text, 'paritypay'::text, 'payhot'::text, 'cispay'::text, 'antilopay'::text, 'anore'::text, 'donut'::text, 'etoplatezhi'::text, 'jupiter'::text, 'tabpay'::text]))),
    CONSTRAINT payment_methods_provider_token_check CHECK (((char_length(provider_token) >= 1) AND (char_length(provider_token) <= 256))),
    CONSTRAINT payment_methods_revocation_complete CHECK (((status = 'revoked'::text) = (revoked_at IS NOT NULL))),
    CONSTRAINT payment_methods_status_check CHECK ((status = ANY (ARRAY['active'::text, 'expired'::text, 'revoked'::text, 'failed'::text]))),
    CONSTRAINT payment_methods_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX payment_methods_customer_idx ON payment_methods USING btree (user_id, created_at DESC) WHERE (status = 'active'::text);

CREATE UNIQUE INDEX payment_methods_one_default_idx ON payment_methods USING btree (user_id) WHERE (is_default AND (status = 'active'::text));

CREATE TABLE dunning_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    subscription_id uuid,
    cycle_key text NOT NULL,
    attempt integer NOT NULL,
    funding text NOT NULL,
    payment_method_id uuid,
    order_id uuid,
    outcome text DEFAULT 'scheduled'::text NOT NULL,
    failure_code text,
    scheduled_for timestamp with time zone NOT NULL,
    occurred_at timestamp with time zone,
    notified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    notify_required boolean DEFAULT false NOT NULL,
    CONSTRAINT dunning_attempts_pkey PRIMARY KEY (id),
    CONSTRAINT dunning_attempts_cycle_key_attempt_key UNIQUE (cycle_key, attempt),
    CONSTRAINT dunning_attempts_attempt_check CHECK ((attempt > 0)),
    CONSTRAINT dunning_attempts_cycle_key_check CHECK (((char_length(cycle_key) >= 1) AND (char_length(cycle_key) <= 128))),
    CONSTRAINT dunning_attempts_funding_check CHECK ((funding = ANY (ARRAY['wallet'::text, 'saved_method'::text]))),
    CONSTRAINT dunning_attempts_outcome_check CHECK ((outcome = ANY (ARRAY['scheduled'::text, 'succeeded'::text, 'failed'::text, 'abandoned'::text]))),
    CONSTRAINT dunning_attempts_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT dunning_attempts_payment_method_id_fkey FOREIGN KEY (payment_method_id) REFERENCES payment_methods(id),
    CONSTRAINT dunning_attempts_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id),
    CONSTRAINT dunning_attempts_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX dunning_attempts_customer_idx ON dunning_attempts USING btree (user_id, created_at DESC);

CREATE INDEX dunning_attempts_due_idx ON dunning_attempts USING btree (scheduled_for) WHERE (outcome = 'scheduled'::text);

CREATE INDEX dunning_attempts_notify_idx ON dunning_attempts USING btree (occurred_at) WHERE (notify_required AND (notified_at IS NULL));

CREATE TABLE personal_offers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    plan_id uuid,
    title_ru text NOT NULL,
    title_en text NOT NULL,
    terms_ru text DEFAULT ''::text NOT NULL,
    terms_en text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    starts_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    order_id uuid,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT personal_offers_pkey PRIMARY KEY (id),
    CONSTRAINT personal_offers_redemption_complete CHECK (((status = 'redeemed'::text) = (order_id IS NOT NULL))),
    CONSTRAINT personal_offers_status_check CHECK ((status = ANY (ARRAY['active'::text, 'redeemed'::text, 'dismissed'::text, 'expired'::text, 'revoked'::text]))),
    CONSTRAINT personal_offers_terms_en_check CHECK ((char_length(terms_en) <= 1000)),
    CONSTRAINT personal_offers_terms_ru_check CHECK ((char_length(terms_ru) <= 1000)),
    CONSTRAINT personal_offers_title_en_check CHECK (((char_length(title_en) >= 1) AND (char_length(title_en) <= 120))),
    CONSTRAINT personal_offers_title_ru_check CHECK (((char_length(title_ru) >= 1) AND (char_length(title_ru) <= 120))),
    CONSTRAINT personal_offers_window CHECK ((expires_at > starts_at)),
    CONSTRAINT personal_offers_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id),
    CONSTRAINT personal_offers_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT personal_offers_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id),
    CONSTRAINT personal_offers_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES promotions(id),
    CONSTRAINT personal_offers_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX personal_offers_customer_idx ON personal_offers USING btree (user_id, created_at DESC);

CREATE INDEX personal_offers_expiry_idx ON personal_offers USING btree (expires_at) WHERE (status = 'active'::text);

CREATE UNIQUE INDEX personal_offers_one_active_idx ON personal_offers USING btree (user_id, promotion_id) WHERE (status = 'active'::text);

CREATE TABLE gifts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    sender_user_id uuid NOT NULL,
    kind text NOT NULL,
    plan_version_id uuid,
    addon_version_id uuid,
    credit_minor bigint,
    currency text NOT NULL,
    code_hash bytea NOT NULL,
    code_hint text NOT NULL,
    recipient_telegram_id bigint,
    recipient_user_id uuid,
    sender_message text,
    status text DEFAULT 'pending'::text NOT NULL,
    claim_attempts integer DEFAULT 0 NOT NULL,
    claim_entitlement_id uuid,
    claim_ledger_transaction_id uuid,
    expires_at timestamp with time zone NOT NULL,
    claimed_at timestamp with time zone,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    revoke_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT gifts_pkey PRIMARY KEY (id),
    CONSTRAINT gifts_code_hash_key UNIQUE (code_hash),
    CONSTRAINT gifts_order_id_key UNIQUE (order_id),
    CONSTRAINT gifts_addon_payload CHECK (((kind = 'addon'::text) = (addon_version_id IS NOT NULL))),
    CONSTRAINT gifts_claim_attempts_check CHECK ((claim_attempts >= 0)),
    CONSTRAINT gifts_claim_complete CHECK (((status = 'claimed'::text) = ((claimed_at IS NOT NULL) AND (recipient_user_id IS NOT NULL)))),
    CONSTRAINT gifts_code_hash_check CHECK ((octet_length(code_hash) = 32)),
    CONSTRAINT gifts_code_hint_check CHECK ((code_hint ~ '^[A-Z0-9]{4}$'::text)),
    CONSTRAINT gifts_credit_minor_check CHECK (((credit_minor IS NULL) OR (credit_minor > 0))),
    CONSTRAINT gifts_credit_payload CHECK (((kind = 'wallet_credit'::text) = (credit_minor IS NOT NULL))),
    CONSTRAINT gifts_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT gifts_kind_check CHECK ((kind = ANY (ARRAY['subscription'::text, 'addon'::text, 'wallet_credit'::text]))),
    CONSTRAINT gifts_recipient_telegram_id_check CHECK (((recipient_telegram_id IS NULL) OR (recipient_telegram_id > 0))),
    CONSTRAINT gifts_revocation_complete CHECK (((status = 'revoked'::text) = ((revoked_at IS NOT NULL) AND (revoke_reason IS NOT NULL)))),
    CONSTRAINT gifts_revoke_reason_check CHECK (((revoke_reason IS NULL) OR (char_length(revoke_reason) <= 400))),
    CONSTRAINT gifts_sender_message_check CHECK (((sender_message IS NULL) OR (char_length(sender_message) <= 200))),
    CONSTRAINT gifts_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'deliverable'::text, 'claimed'::text, 'expired'::text, 'revoked'::text, 'refunded'::text]))),
    CONSTRAINT gifts_subscription_payload CHECK (((kind = 'subscription'::text) = (plan_version_id IS NOT NULL))),
    CONSTRAINT gifts_addon_version_id_fkey FOREIGN KEY (addon_version_id) REFERENCES addon_versions(id),
    CONSTRAINT gifts_claim_entitlement_id_fkey FOREIGN KEY (claim_entitlement_id) REFERENCES entitlements(id),
    CONSTRAINT gifts_claim_ledger_transaction_id_fkey FOREIGN KEY (claim_ledger_transaction_id) REFERENCES ledger_transactions(id),
    CONSTRAINT gifts_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT gifts_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT gifts_recipient_user_id_fkey FOREIGN KEY (recipient_user_id) REFERENCES users(id),
    CONSTRAINT gifts_revoked_by_fkey FOREIGN KEY (revoked_by) REFERENCES admin_users(id),
    CONSTRAINT gifts_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES users(id)
);

CREATE INDEX gifts_expiry_idx ON gifts USING btree (expires_at) WHERE (status = 'deliverable'::text);

CREATE INDEX gifts_recipient_idx ON gifts USING btree (recipient_user_id, claimed_at DESC) WHERE (recipient_user_id IS NOT NULL);

CREATE INDEX gifts_sender_idx ON gifts USING btree (sender_user_id, created_at DESC);

CREATE TABLE goods_providers (
    slug text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    credentials_ciphertext bytea,
    balance_minor bigint,
    balance_currency text,
    low_balance_threshold_minor bigint,
    spend_limit_minor bigint DEFAULT 0 NOT NULL,
    spend_window_seconds bigint DEFAULT 86400 NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    last_error_code text,
    last_checked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT goods_providers_pkey PRIMARY KEY (slug),
    CONSTRAINT goods_providers_balance_currency_check CHECK (((balance_currency IS NULL) OR (balance_currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT goods_providers_balance_currency_present CHECK (((balance_minor IS NULL) = (balance_currency IS NULL))),
    CONSTRAINT goods_providers_balance_minor_check CHECK (((balance_minor IS NULL) OR (balance_minor >= 0))),
    CONSTRAINT goods_providers_low_balance_threshold_minor_check CHECK (((low_balance_threshold_minor IS NULL) OR (low_balance_threshold_minor >= 0))),
    CONSTRAINT goods_providers_slug_check CHECK ((slug ~ '^[a-z][a-z0-9_-]{1,31}$'::text)),
    CONSTRAINT goods_providers_spend_limit_minor_check CHECK ((spend_limit_minor >= 0)),
    CONSTRAINT goods_providers_spend_window_seconds_check CHECK ((spend_window_seconds > 0)),
    CONSTRAINT goods_providers_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'failing'::text])))
);

CREATE TABLE goods_products (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    provider_slug text NOT NULL,
    kind text NOT NULL,
    duration_months smallint,
    star_quantity integer,
    visible boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    min_star_quantity integer,
    max_star_quantity integer,
    star_presets integer[] DEFAULT '{}'::integer[] NOT NULL,
    CONSTRAINT goods_products_pkey PRIMARY KEY (id),
    CONSTRAINT goods_products_code_key UNIQUE (code),
    CONSTRAINT goods_products_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT goods_products_custom_amount_range CHECK ((((min_star_quantity IS NULL) = (max_star_quantity IS NULL)) AND ((min_star_quantity IS NULL) OR ((star_quantity IS NULL) AND (min_star_quantity > 0) AND (max_star_quantity >= min_star_quantity))))),
    CONSTRAINT goods_products_duration_months_check CHECK (((duration_months IS NULL) OR (duration_months = ANY (ARRAY[3, 6, 12])))),
    CONSTRAINT goods_products_kind_check CHECK ((kind = ANY (ARRAY['telegram_premium'::text, 'telegram_stars'::text]))),
    CONSTRAINT goods_products_premium_shape CHECK (((kind = 'telegram_premium'::text) = (duration_months IS NOT NULL))),
    CONSTRAINT goods_products_star_presets_bounded CHECK ((cardinality(star_presets) <= 12)),
    CONSTRAINT goods_products_star_quantity_check CHECK (((star_quantity IS NULL) OR (star_quantity > 0))),
    CONSTRAINT goods_products_stars_amount_shape CHECK (((kind = 'telegram_stars'::text) = ((star_quantity IS NOT NULL) OR (min_star_quantity IS NOT NULL)))),
    CONSTRAINT goods_products_provider_slug_fkey FOREIGN KEY (provider_slug) REFERENCES goods_providers(slug)
);

CREATE INDEX goods_products_catalog_idx ON goods_products USING btree (sort_order, code) WHERE (visible AND (archived_at IS NULL));

CREATE TABLE goods_product_localizations (
    product_id uuid NOT NULL,
    locale text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    CONSTRAINT goods_product_localizations_pkey PRIMARY KEY (product_id, locale),
    CONSTRAINT goods_product_localizations_description_check CHECK ((char_length(description) <= 2000)),
    CONSTRAINT goods_product_localizations_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT goods_product_localizations_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 120))),
    CONSTRAINT goods_product_localizations_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id) ON DELETE CASCADE
);

CREATE TABLE goods_pricing (
    product_id uuid NOT NULL,
    currency text NOT NULL,
    markup_bps integer DEFAULT 0 NOT NULL,
    rounding text DEFAULT 'up_minor'::text NOT NULL,
    fixed_amount_minor bigint,
    quote_ttl_seconds integer DEFAULT 300 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    unit_price_minor bigint,
    CONSTRAINT goods_pricing_pkey PRIMARY KEY (product_id),
    CONSTRAINT goods_pricing_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT goods_pricing_fixed_amount_minor_check CHECK (((fixed_amount_minor IS NULL) OR (fixed_amount_minor > 0))),
    CONSTRAINT goods_pricing_markup_bps_check CHECK (((markup_bps >= 0) AND (markup_bps <= 100000))),
    CONSTRAINT goods_pricing_quote_ttl_seconds_check CHECK (((quote_ttl_seconds >= 30) AND (quote_ttl_seconds <= 3600))),
    CONSTRAINT goods_pricing_rounding_check CHECK ((rounding = ANY (ARRAY['none'::text, 'up_minor'::text, 'up_unit'::text, 'up_ten_units'::text, 'up_hundred_units'::text]))),
    CONSTRAINT goods_pricing_single_operator_price CHECK (((fixed_amount_minor IS NULL) OR (unit_price_minor IS NULL))),
    CONSTRAINT goods_pricing_unit_price_positive CHECK (((unit_price_minor IS NULL) OR (unit_price_minor > 0))),
    CONSTRAINT goods_pricing_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id) ON DELETE CASCADE,
    CONSTRAINT goods_pricing_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE goods_orders (
    order_id uuid NOT NULL,
    user_id uuid NOT NULL,
    product_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    recipient_username text NOT NULL,
    recipient_is_self boolean DEFAULT false NOT NULL,
    quoted_cost_minor bigint NOT NULL,
    quoted_price_minor bigint NOT NULL,
    currency text NOT NULL,
    quote_expires_at timestamp with time zone NOT NULL,
    status text DEFAULT 'quoted'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    cost_known boolean DEFAULT true NOT NULL,
    discount_minor bigint DEFAULT 0 NOT NULL,
    star_quantity integer,
    source text DEFAULT 'unknown'::text NOT NULL,
    CONSTRAINT goods_orders_pkey PRIMARY KEY (order_id),
    CONSTRAINT goods_orders_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT goods_orders_discount_minor_check CHECK ((discount_minor >= 0)),
    CONSTRAINT goods_orders_discount_respects_cost CHECK (((NOT cost_known) OR ((quoted_price_minor - discount_minor) >= quoted_cost_minor))),
    CONSTRAINT goods_orders_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT goods_orders_quoted_cost_minor_check CHECK ((quoted_cost_minor >= 0)),
    CONSTRAINT goods_orders_quoted_price_minor_check CHECK ((quoted_price_minor >= 0)),
    CONSTRAINT goods_orders_recipient_username_check CHECK ((recipient_username ~ '^[A-Za-z0-9_]{5,32}$'::text)),
    CONSTRAINT goods_orders_source_known CHECK ((source = ANY (ARRAY['bot'::text, 'web'::text, 'auto_cart'::text, 'unknown'::text]))),
    CONSTRAINT goods_orders_star_quantity_positive CHECK (((star_quantity IS NULL) OR (star_quantity > 0))),
    CONSTRAINT goods_orders_status_known CHECK ((status = ANY (ARRAY['quoted'::text, 'paid'::text, 'delivering'::text, 'delivered'::text, 'failed'::text, 'refunded'::text, 'needs_review'::text]))),
    CONSTRAINT goods_orders_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT goods_orders_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id),
    CONSTRAINT goods_orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

COMMENT ON COLUMN goods_orders.cost_known IS 'False when the provider publishes no cost for this product and the operator set the price directly. Margin is unknown, not zero.';

CREATE INDEX goods_orders_customer_idx ON goods_orders USING btree (user_id, created_at DESC);

CREATE INDEX goods_orders_recipient_search_idx ON goods_orders USING btree (lower(recipient_username));

CREATE INDEX goods_orders_status_idx ON goods_orders USING btree (status, created_at DESC);

CREATE TABLE goods_deliveries (
    order_id uuid NOT NULL,
    provider_slug text NOT NULL,
    idempotency_key text NOT NULL,
    provider_reference text,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    failure_class text,
    last_error_code text,
    refund_ledger_transaction_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    broadcasting_at timestamp with time zone,
    provider_request_id text,
    settlement_asset text,
    settlement_cost_nano bigint,
    settlement_tx_hash text,
    provider_confirmed boolean,
    dry_run boolean DEFAULT false NOT NULL,
    settled_cost_minor bigint,
    settled_rate_minor bigint,
    settled_rate_source text,
    CONSTRAINT goods_deliveries_pkey PRIMARY KEY (order_id),
    CONSTRAINT goods_deliveries_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT goods_deliveries_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT goods_deliveries_delivery_complete CHECK (((status = 'delivered'::text) = (delivered_at IS NOT NULL))),
    CONSTRAINT goods_deliveries_failure_class_known CHECK (((failure_class IS NULL) OR (failure_class = ANY (ARRAY['retryable'::text, 'permanent'::text, 'recipient_invalid'::text, 'provider_balance'::text, 'provider_unavailable'::text, 'ambiguous'::text])))),
    CONSTRAINT goods_deliveries_settled_cost_nonnegative CHECK (((settled_cost_minor IS NULL) OR (settled_cost_minor >= 0))),
    CONSTRAINT goods_deliveries_settled_rate_positive CHECK (((settled_rate_minor IS NULL) OR (settled_rate_minor > 0))),
    CONSTRAINT goods_deliveries_settled_rate_source_known CHECK (((settled_rate_source IS NULL) OR (settled_rate_source = ANY (ARRAY['manual'::text, 'tonapi'::text, 'coingecko'::text])))),
    CONSTRAINT goods_deliveries_settlement_asset_format CHECK (((settlement_asset IS NULL) OR (settlement_asset ~ '^[A-Z]{2,10}$'::text))),
    CONSTRAINT goods_deliveries_settlement_cost_nonnegative CHECK (((settlement_cost_nano IS NULL) OR (settlement_cost_nano >= 0))),
    CONSTRAINT goods_deliveries_status_known CHECK ((status = ANY (ARRAY['pending'::text, 'submitted'::text, 'delivered'::text, 'failed'::text, 'cancelled'::text, 'needs_review'::text]))),
    CONSTRAINT goods_deliveries_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id),
    CONSTRAINT goods_deliveries_provider_slug_fkey FOREIGN KEY (provider_slug) REFERENCES goods_providers(slug),
    CONSTRAINT goods_deliveries_refund_ledger_transaction_id_fkey FOREIGN KEY (refund_ledger_transaction_id) REFERENCES ledger_transactions(id)
);

CREATE INDEX goods_deliveries_due_idx ON goods_deliveries USING btree (next_attempt_at) WHERE (status = ANY (ARRAY['pending'::text, 'submitted'::text]));

CREATE INDEX goods_deliveries_review_idx ON goods_deliveries USING btree (updated_at DESC) WHERE (status = 'needs_review'::text);

CREATE TABLE goods_delivery_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    attempt integer NOT NULL,
    outcome text NOT NULL,
    failure_class text,
    error_code text,
    provider_reference text,
    correlation_id text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT goods_delivery_attempts_pkey PRIMARY KEY (id),
    CONSTRAINT goods_delivery_attempts_order_id_attempt_key UNIQUE (order_id, attempt),
    CONSTRAINT goods_delivery_attempts_attempt_check CHECK ((attempt > 0)),
    CONSTRAINT goods_delivery_attempts_outcome_check CHECK ((outcome = ANY (ARRAY['submitted'::text, 'delivered'::text, 'failed'::text]))),
    CONSTRAINT goods_delivery_attempts_order_id_fkey FOREIGN KEY (order_id) REFERENCES goods_deliveries(order_id) ON DELETE CASCADE
);

CREATE TABLE blocklist_sources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    display_name text NOT NULL,
    subject_kind text NOT NULL,
    url text NOT NULL,
    auth_header_ciphertext bytea,
    enabled boolean DEFAULT false NOT NULL,
    refresh_interval_seconds bigint DEFAULT 86400 NOT NULL,
    entry_count integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    last_error_code text,
    last_refresh_at timestamp with time zone,
    next_refresh_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT blocklist_sources_pkey PRIMARY KEY (id),
    CONSTRAINT blocklist_sources_slug_key UNIQUE (slug),
    CONSTRAINT blocklist_sources_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 80))),
    CONSTRAINT blocklist_sources_entry_count_check CHECK ((entry_count >= 0)),
    CONSTRAINT blocklist_sources_refresh_interval_seconds_check CHECK ((refresh_interval_seconds >= 300)),
    CONSTRAINT blocklist_sources_slug_check CHECK ((slug ~ '^[a-z][a-z0-9-]{1,38}[a-z0-9]$'::text)),
    CONSTRAINT blocklist_sources_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'healthy'::text, 'failing'::text]))),
    CONSTRAINT blocklist_sources_subject_kind_check CHECK ((subject_kind = ANY (ARRAY['telegram_id'::text, 'email'::text, 'username'::text]))),
    CONSTRAINT blocklist_sources_url_check CHECK ((url ~ '^https://'::text)),
    CONSTRAINT blocklist_sources_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE INDEX blocklist_sources_due_idx ON blocklist_sources USING btree (next_refresh_at) WHERE enabled;

CREATE TABLE blocklist_entries (
    source_id uuid NOT NULL,
    value_fingerprint bytea NOT NULL,
    reason_code text,
    imported_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT blocklist_entries_pkey PRIMARY KEY (source_id, value_fingerprint),
    CONSTRAINT blocklist_entries_reason_code_check CHECK (((reason_code IS NULL) OR (char_length(reason_code) <= 64))),
    CONSTRAINT blocklist_entries_value_fingerprint_check CHECK ((octet_length(value_fingerprint) = 32)),
    CONSTRAINT blocklist_entries_source_id_fkey FOREIGN KEY (source_id) REFERENCES blocklist_sources(id) ON DELETE CASCADE
);

CREATE TABLE blocklist_matches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    source_id uuid NOT NULL,
    subject_kind text NOT NULL,
    value_fingerprint bytea NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    decision_reason text,
    decided_by uuid,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    CONSTRAINT blocklist_matches_pkey PRIMARY KEY (id),
    CONSTRAINT blocklist_matches_user_id_source_id_value_fingerprint_key UNIQUE (user_id, source_id, value_fingerprint),
    CONSTRAINT blocklist_matches_decision_complete CHECK (((status = ANY (ARRAY['open'::text, 'appealed'::text])) = (decided_at IS NULL))),
    CONSTRAINT blocklist_matches_decision_reason_check CHECK (((decision_reason IS NULL) OR (char_length(decision_reason) <= 400))),
    CONSTRAINT blocklist_matches_status_check CHECK ((status = ANY (ARRAY['open'::text, 'allowed'::text, 'blocked'::text, 'appealed'::text]))),
    CONSTRAINT blocklist_matches_subject_kind_check CHECK ((subject_kind = ANY (ARRAY['telegram_id'::text, 'email'::text, 'username'::text]))),
    CONSTRAINT blocklist_matches_value_fingerprint_check CHECK ((octet_length(value_fingerprint) = 32)),
    CONSTRAINT blocklist_matches_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES admin_users(id),
    CONSTRAINT blocklist_matches_source_id_fkey FOREIGN KEY (source_id) REFERENCES blocklist_sources(id) ON DELETE CASCADE,
    CONSTRAINT blocklist_matches_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX blocklist_matches_open_idx ON blocklist_matches USING btree (detected_at DESC) WHERE (status = ANY (ARRAY['open'::text, 'appealed'::text]));

CREATE TABLE blocklist_allowlist (
    user_id uuid NOT NULL,
    reason text NOT NULL,
    added_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT blocklist_allowlist_pkey PRIMARY KEY (user_id),
    CONSTRAINT blocklist_allowlist_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 400))),
    CONSTRAINT blocklist_allowlist_added_by_fkey FOREIGN KEY (added_by) REFERENCES admin_users(id),
    CONSTRAINT blocklist_allowlist_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE anomaly_rules (
    metric text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    window_seconds bigint DEFAULT 3600 NOT NULL,
    warn_threshold bigint NOT NULL,
    alert_threshold bigint NOT NULL,
    minimum_sample integer DEFAULT 3 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT anomaly_rules_pkey PRIMARY KEY (metric),
    CONSTRAINT anomaly_rules_alert_threshold_check CHECK ((alert_threshold > 0)),
    CONSTRAINT anomaly_rules_metric_check CHECK ((metric = ANY (ARRAY['traffic'::text, 'purchase'::text, 'refund'::text, 'referral'::text]))),
    CONSTRAINT anomaly_rules_minimum_sample_check CHECK ((minimum_sample >= 0)),
    CONSTRAINT anomaly_rules_threshold_order CHECK ((alert_threshold >= warn_threshold)),
    CONSTRAINT anomaly_rules_warn_threshold_check CHECK ((warn_threshold > 0)),
    CONSTRAINT anomaly_rules_window_seconds_check CHECK ((window_seconds >= 300)),
    CONSTRAINT anomaly_rules_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE anomaly_signals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    metric text NOT NULL,
    severity text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    observed bigint NOT NULL,
    threshold bigint NOT NULL,
    sample_size integer DEFAULT 0 NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    window_ended_at timestamp with time zone NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    reviewed_by uuid,
    review_reason text,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    reviewed_at timestamp with time zone,
    dedupe_key text NOT NULL,
    CONSTRAINT anomaly_signals_pkey PRIMARY KEY (id),
    CONSTRAINT anomaly_signals_metric_dedupe_key_key UNIQUE (metric, dedupe_key),
    CONSTRAINT anomaly_signals_review_complete CHECK (((status = 'open'::text) = (reviewed_at IS NULL))),
    CONSTRAINT anomaly_signals_review_reason_check CHECK (((review_reason IS NULL) OR (char_length(review_reason) <= 400))),
    CONSTRAINT anomaly_signals_sample_size_check CHECK ((sample_size >= 0)),
    CONSTRAINT anomaly_signals_severity_check CHECK ((severity = ANY (ARRAY['warning'::text, 'alert'::text]))),
    CONSTRAINT anomaly_signals_status_check CHECK ((status = ANY (ARRAY['open'::text, 'acknowledged'::text, 'dismissed'::text]))),
    CONSTRAINT anomaly_signals_subject_id_check CHECK (((char_length(subject_id) >= 1) AND (char_length(subject_id) <= 128))),
    CONSTRAINT anomaly_signals_subject_type_check CHECK ((subject_type = ANY (ARRAY['installation'::text, 'customer'::text, 'plan'::text, 'provider'::text]))),
    CONSTRAINT anomaly_signals_window CHECK ((window_ended_at > window_started_at)),
    CONSTRAINT anomaly_signals_reviewed_by_fkey FOREIGN KEY (reviewed_by) REFERENCES admin_users(id)
);

CREATE INDEX anomaly_signals_open_idx ON anomaly_signals USING btree (detected_at DESC) WHERE (status = 'open'::text);

CREATE INDEX anomaly_signals_subject_idx ON anomaly_signals USING btree (subject_type, subject_id, detected_at DESC);

CREATE TABLE bulk_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'previewing'::text NOT NULL,
    requested_by uuid NOT NULL,
    reason text NOT NULL,
    parameters jsonb DEFAULT '{}'::jsonb NOT NULL,
    total_count integer DEFAULT 0 NOT NULL,
    succeeded_count integer DEFAULT 0 NOT NULL,
    failed_count integer DEFAULT 0 NOT NULL,
    skipped_count integer DEFAULT 0 NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT bulk_operations_pkey PRIMARY KEY (id),
    CONSTRAINT bulk_operations_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT bulk_operations_failed_count_check CHECK ((failed_count >= 0)),
    CONSTRAINT bulk_operations_kind_check CHECK ((kind = ANY (ARRAY['customer_export'::text, 'subscription_extend'::text, 'subscription_disable'::text, 'subscription_enable'::text, 'wallet_credit'::text]))),
    CONSTRAINT bulk_operations_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 400))),
    CONSTRAINT bulk_operations_skipped_count_check CHECK ((skipped_count >= 0)),
    CONSTRAINT bulk_operations_status_check CHECK ((status = ANY (ARRAY['previewing'::text, 'ready'::text, 'running'::text, 'completed'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT bulk_operations_succeeded_count_check CHECK ((succeeded_count >= 0)),
    CONSTRAINT bulk_operations_total_count_check CHECK ((total_count >= 0)),
    CONSTRAINT bulk_operations_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES admin_users(id)
);

CREATE INDEX bulk_operations_recent_idx ON bulk_operations USING btree (created_at DESC);

CREATE TABLE bulk_operation_items (
    operation_id uuid NOT NULL,
    "position" integer NOT NULL,
    target_type text NOT NULL,
    target_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    error_code text,
    processed_at timestamp with time zone,
    CONSTRAINT bulk_operation_items_pkey PRIMARY KEY (operation_id, "position"),
    CONSTRAINT bulk_operation_items_position_check CHECK (("position" >= 0)),
    CONSTRAINT bulk_operation_items_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'succeeded'::text, 'failed'::text, 'skipped'::text]))),
    CONSTRAINT bulk_operation_items_target_type_check CHECK ((target_type = ANY (ARRAY['customer'::text, 'subscription'::text]))),
    CONSTRAINT bulk_operation_items_operation_id_fkey FOREIGN KEY (operation_id) REFERENCES bulk_operations(id) ON DELETE CASCADE
);

CREATE INDEX bulk_operation_items_pending_idx ON bulk_operation_items USING btree (operation_id, "position") WHERE (status = 'pending'::text);

-- ----------------------------------------------------------------------------
-- Support desk
-- ----------------------------------------------------------------------------

CREATE TABLE support_queues (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    name_en text NOT NULL,
    name_ru text NOT NULL,
    first_response_target_seconds bigint DEFAULT 0 NOT NULL,
    resolution_target_seconds bigint DEFAULT 0 NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT support_queues_pkey PRIMARY KEY (id),
    CONSTRAINT support_queues_code_key UNIQUE (code),
    CONSTRAINT support_queues_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT support_queues_first_response_target_seconds_check CHECK ((first_response_target_seconds >= 0)),
    CONSTRAINT support_queues_name_en_check CHECK (((char_length(name_en) >= 1) AND (char_length(name_en) <= 80))),
    CONSTRAINT support_queues_name_ru_check CHECK (((char_length(name_ru) >= 1) AND (char_length(name_ru) <= 80))),
    CONSTRAINT support_queues_resolution_target_seconds_check CHECK ((resolution_target_seconds >= 0))
);

CREATE UNIQUE INDEX support_queues_one_default_idx ON support_queues USING btree (is_default) WHERE (is_default AND (archived_at IS NULL));

CREATE TABLE support_tags (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    name_en text NOT NULL,
    name_ru text NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT support_tags_pkey PRIMARY KEY (id),
    CONSTRAINT support_tags_code_key UNIQUE (code),
    CONSTRAINT support_tags_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT support_tags_name_en_check CHECK (((char_length(name_en) >= 1) AND (char_length(name_en) <= 60))),
    CONSTRAINT support_tags_name_ru_check CHECK (((char_length(name_ru) >= 1) AND (char_length(name_ru) <= 60)))
);

CREATE TABLE support_ticket_tags (
    ticket_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    tagged_by uuid,
    tagged_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT support_ticket_tags_pkey PRIMARY KEY (ticket_id, tag_id),
    CONSTRAINT support_ticket_tags_tag_id_fkey FOREIGN KEY (tag_id) REFERENCES support_tags(id) ON DELETE CASCADE,
    CONSTRAINT support_ticket_tags_tagged_by_fkey FOREIGN KEY (tagged_by) REFERENCES admin_users(id),
    CONSTRAINT support_ticket_tags_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES support_tickets(id) ON DELETE CASCADE
);

CREATE INDEX support_ticket_tags_tag_idx ON support_ticket_tags USING btree (tag_id);

CREATE TABLE support_notes (
    id bigint GENERATED ALWAYS AS IDENTITY,
    ticket_id uuid NOT NULL,
    author_id uuid NOT NULL,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT support_notes_pkey PRIMARY KEY (id),
    CONSTRAINT support_notes_body_check CHECK (((char_length(body) >= 1) AND (char_length(body) <= 4000))),
    CONSTRAINT support_notes_author_id_fkey FOREIGN KEY (author_id) REFERENCES admin_users(id),
    CONSTRAINT support_notes_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES support_tickets(id) ON DELETE CASCADE
);

CREATE INDEX support_notes_ticket_idx ON support_notes USING btree (ticket_id, created_at);

CREATE TABLE support_canned_responses (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    title_en text NOT NULL,
    title_ru text NOT NULL,
    body_en text NOT NULL,
    body_ru text NOT NULL,
    requires_permission text DEFAULT 'support.write'::text NOT NULL,
    usage_count bigint DEFAULT 0 NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT support_canned_responses_pkey PRIMARY KEY (id),
    CONSTRAINT support_canned_responses_code_key UNIQUE (code),
    CONSTRAINT support_canned_responses_body_en_check CHECK (((char_length(body_en) >= 1) AND (char_length(body_en) <= 4000))),
    CONSTRAINT support_canned_responses_body_ru_check CHECK (((char_length(body_ru) >= 1) AND (char_length(body_ru) <= 4000))),
    CONSTRAINT support_canned_responses_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT support_canned_responses_requires_permission_check CHECK (((char_length(requires_permission) >= 3) AND (char_length(requires_permission) <= 64))),
    CONSTRAINT support_canned_responses_title_en_check CHECK (((char_length(title_en) >= 1) AND (char_length(title_en) <= 80))),
    CONSTRAINT support_canned_responses_title_ru_check CHECK (((char_length(title_ru) >= 1) AND (char_length(title_ru) <= 80))),
    CONSTRAINT support_canned_responses_usage_count_check CHECK ((usage_count >= 0)),
    CONSTRAINT support_canned_responses_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE INDEX support_canned_responses_active_idx ON support_canned_responses USING btree (code) WHERE (archived_at IS NULL);

-- ----------------------------------------------------------------------------
-- Referrals and loyalty
-- ----------------------------------------------------------------------------

CREATE TABLE referral_signals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    referred_user_id uuid NOT NULL,
    code text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT referral_signals_pkey PRIMARY KEY (id),
    CONSTRAINT referral_signals_referred_user_id_code_key UNIQUE (referred_user_id, code),
    CONSTRAINT referral_signals_code_check CHECK ((code ~ '^[a-z][a-z0-9_]{2,63}$'::text)),
    CONSTRAINT referral_signals_referred_user_id_fkey FOREIGN KEY (referred_user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX referral_signals_recent_idx ON referral_signals USING btree (detected_at DESC);

CREATE TABLE loyalty_programs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    version integer NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    metric text DEFAULT 'spend'::text NOT NULL,
    currency text DEFAULT 'RUB'::text NOT NULL,
    window_days integer DEFAULT 365 NOT NULL,
    grace_days integer DEFAULT 30 NOT NULL,
    published_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    CONSTRAINT loyalty_programs_pkey PRIMARY KEY (id),
    CONSTRAINT loyalty_programs_version_key UNIQUE (version),
    CONSTRAINT loyalty_programs_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT loyalty_programs_grace_days_check CHECK ((grace_days >= 0)),
    CONSTRAINT loyalty_programs_metric_check CHECK ((metric = ANY (ARRAY['spend'::text, 'tenure'::text, 'orders'::text]))),
    CONSTRAINT loyalty_programs_version_check CHECK ((version > 0)),
    CONSTRAINT loyalty_programs_window_days_check CHECK ((window_days > 0)),
    CONSTRAINT loyalty_programs_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id)
);

CREATE UNIQUE INDEX loyalty_programs_one_enabled_idx ON loyalty_programs USING btree (enabled) WHERE enabled;

CREATE TABLE loyalty_tiers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    program_id uuid NOT NULL,
    code text NOT NULL,
    name_en text NOT NULL,
    name_ru text NOT NULL,
    threshold bigint NOT NULL,
    discount_bps integer DEFAULT 0 NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    CONSTRAINT loyalty_tiers_pkey PRIMARY KEY (id),
    CONSTRAINT loyalty_tiers_program_id_code_key UNIQUE (program_id, code),
    CONSTRAINT loyalty_tiers_program_id_threshold_key UNIQUE (program_id, threshold),
    CONSTRAINT loyalty_tiers_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,31}$'::text)),
    CONSTRAINT loyalty_tiers_discount_bps_check CHECK (((discount_bps >= 0) AND (discount_bps <= 10000))),
    CONSTRAINT loyalty_tiers_name_en_check CHECK (((char_length(name_en) >= 1) AND (char_length(name_en) <= 60))),
    CONSTRAINT loyalty_tiers_name_ru_check CHECK (((char_length(name_ru) >= 1) AND (char_length(name_ru) <= 60))),
    CONSTRAINT loyalty_tiers_threshold_check CHECK ((threshold >= 0)),
    CONSTRAINT loyalty_tiers_program_id_fkey FOREIGN KEY (program_id) REFERENCES loyalty_programs(id) ON DELETE CASCADE
);

CREATE TABLE loyalty_standings (
    user_id uuid NOT NULL,
    program_id uuid NOT NULL,
    tier_id uuid NOT NULL,
    evaluated_metric bigint NOT NULL,
    evaluated_at timestamp with time zone DEFAULT now() NOT NULL,
    grace_until timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT loyalty_standings_pkey PRIMARY KEY (user_id),
    CONSTRAINT loyalty_standings_evaluated_metric_check CHECK ((evaluated_metric >= 0)),
    CONSTRAINT loyalty_standings_program_id_fkey FOREIGN KEY (program_id) REFERENCES loyalty_programs(id),
    CONSTRAINT loyalty_standings_tier_id_fkey FOREIGN KEY (tier_id) REFERENCES loyalty_tiers(id),
    CONSTRAINT loyalty_standings_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX loyalty_standings_tier_idx ON loyalty_standings USING btree (tier_id);

CREATE TABLE loyalty_standing_history (
    id bigint GENERATED ALWAYS AS IDENTITY,
    user_id uuid NOT NULL,
    from_tier_id uuid,
    to_tier_id uuid NOT NULL,
    evaluated_metric bigint NOT NULL,
    reason text NOT NULL,
    actor_id uuid,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT loyalty_standing_history_pkey PRIMARY KEY (id),
    CONSTRAINT loyalty_standing_history_reason_check CHECK ((reason = ANY (ARRAY['evaluation'::text, 'operator_override'::text, 'program_change'::text]))),
    CONSTRAINT loyalty_standing_history_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES admin_users(id),
    CONSTRAINT loyalty_standing_history_from_tier_id_fkey FOREIGN KEY (from_tier_id) REFERENCES loyalty_tiers(id),
    CONSTRAINT loyalty_standing_history_to_tier_id_fkey FOREIGN KEY (to_tier_id) REFERENCES loyalty_tiers(id),
    CONSTRAINT loyalty_standing_history_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX loyalty_standing_history_customer_idx ON loyalty_standing_history USING btree (user_id, occurred_at DESC);

-- ----------------------------------------------------------------------------
-- Campaigns
-- ----------------------------------------------------------------------------

CREATE TABLE audience_segments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    name_en text NOT NULL,
    name_ru text NOT NULL,
    filters jsonb DEFAULT '{}'::jsonb NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT audience_segments_pkey PRIMARY KEY (id),
    CONSTRAINT audience_segments_code_key UNIQUE (code),
    CONSTRAINT audience_segments_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT audience_segments_name_en_check CHECK (((char_length(name_en) >= 1) AND (char_length(name_en) <= 80))),
    CONSTRAINT audience_segments_name_ru_check CHECK (((char_length(name_ru) >= 1) AND (char_length(name_ru) <= 80))),
    CONSTRAINT audience_segments_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id)
);

CREATE TABLE message_templates (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    class text DEFAULT 'marketing'::text NOT NULL,
    subject_en text DEFAULT ''::text NOT NULL,
    subject_ru text DEFAULT ''::text NOT NULL,
    body_en text NOT NULL,
    body_ru text NOT NULL,
    variables text[] DEFAULT '{}'::text[] NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT message_templates_pkey PRIMARY KEY (id),
    CONSTRAINT message_templates_code_key UNIQUE (code),
    CONSTRAINT message_templates_body_en_check CHECK (((char_length(body_en) >= 1) AND (char_length(body_en) <= 3500))),
    CONSTRAINT message_templates_body_ru_check CHECK (((char_length(body_ru) >= 1) AND (char_length(body_ru) <= 3500))),
    CONSTRAINT message_templates_class_check CHECK ((class = ANY (ARRAY['transactional'::text, 'marketing'::text]))),
    CONSTRAINT message_templates_code_check CHECK ((code ~ '^[a-z][a-z0-9_-]{1,63}$'::text)),
    CONSTRAINT message_templates_subject_en_check CHECK ((char_length(subject_en) <= 200)),
    CONSTRAINT message_templates_subject_ru_check CHECK ((char_length(subject_ru) <= 200)),
    CONSTRAINT message_templates_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE campaigns (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    template_id uuid NOT NULL,
    segment_id uuid NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    scheduled_for timestamp with time zone,
    estimated_audience integer DEFAULT 0 NOT NULL,
    queued_count integer DEFAULT 0 NOT NULL,
    sent_count integer DEFAULT 0 NOT NULL,
    failed_count integer DEFAULT 0 NOT NULL,
    suppressed_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT campaigns_pkey PRIMARY KEY (id),
    CONSTRAINT campaigns_estimated_audience_check CHECK ((estimated_audience >= 0)),
    CONSTRAINT campaigns_failed_count_check CHECK ((failed_count >= 0)),
    CONSTRAINT campaigns_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 120))),
    CONSTRAINT campaigns_queued_count_check CHECK ((queued_count >= 0)),
    CONSTRAINT campaigns_schedule_present CHECK (((status <> 'scheduled'::text) OR (scheduled_for IS NOT NULL))),
    CONSTRAINT campaigns_sent_count_check CHECK ((sent_count >= 0)),
    CONSTRAINT campaigns_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'scheduled'::text, 'running'::text, 'paused'::text, 'completed'::text, 'cancelled'::text]))),
    CONSTRAINT campaigns_suppressed_count_check CHECK ((suppressed_count >= 0)),
    CONSTRAINT campaigns_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id),
    CONSTRAINT campaigns_segment_id_fkey FOREIGN KEY (segment_id) REFERENCES audience_segments(id),
    CONSTRAINT campaigns_template_id_fkey FOREIGN KEY (template_id) REFERENCES message_templates(id)
);

CREATE INDEX campaigns_due_idx ON campaigns USING btree (scheduled_for) WHERE (status = 'scheduled'::text);

CREATE INDEX campaigns_running_idx ON campaigns USING btree (status) WHERE (status = 'running'::text);

CREATE TABLE campaign_recipients (
    campaign_id uuid NOT NULL,
    user_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    suppression_reason text,
    error_code text,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT campaign_recipients_pkey PRIMARY KEY (campaign_id, user_id),
    CONSTRAINT campaign_recipients_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'sent'::text, 'failed'::text, 'suppressed'::text, 'blocked'::text]))),
    CONSTRAINT campaign_recipients_suppression_reason_check CHECK (((suppression_reason IS NULL) OR (suppression_reason = ANY (ARRAY['no_consent'::text, 'suppressed'::text, 'frequency_cap'::text, 'quiet_hours'::text, 'delivery_blocked'::text, 'no_telegram'::text])))),
    CONSTRAINT campaign_recipients_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE,
    CONSTRAINT campaign_recipients_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX campaign_recipients_pending_idx ON campaign_recipients USING btree (campaign_id) WHERE (status = 'queued'::text);

CREATE TABLE communication_suppressions (
    user_id uuid NOT NULL,
    reason text NOT NULL,
    note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    CONSTRAINT communication_suppressions_pkey PRIMARY KEY (user_id),
    CONSTRAINT communication_suppressions_note_check CHECK (((note IS NULL) OR (char_length(note) <= 400))),
    CONSTRAINT communication_suppressions_reason_check CHECK ((reason = ANY (ARRAY['customer_request'::text, 'bounced'::text, 'complaint'::text, 'operator'::text]))),
    CONSTRAINT communication_suppressions_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id),
    CONSTRAINT communication_suppressions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- ----------------------------------------------------------------------------
-- Required channels
-- ----------------------------------------------------------------------------

CREATE TABLE required_channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    telegram_chat_id bigint NOT NULL,
    username text,
    title text NOT NULL,
    invite_url text,
    enabled boolean DEFAULT false NOT NULL,
    require_for_purchase boolean DEFAULT true NOT NULL,
    require_for_activation boolean DEFAULT false NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    CONSTRAINT required_channels_pkey PRIMARY KEY (id),
    CONSTRAINT required_channels_telegram_chat_id_key UNIQUE (telegram_chat_id),
    CONSTRAINT required_channels_invite_url_check CHECK (((invite_url IS NULL) OR (char_length(invite_url) <= 400))),
    CONSTRAINT required_channels_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 120))),
    CONSTRAINT required_channels_username_check CHECK (((username IS NULL) OR (username ~ '^[A-Za-z0-9_]{5,32}$'::text))),
    CONSTRAINT required_channels_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id)
);

CREATE INDEX required_channels_active_idx ON required_channels USING btree (sort_order) WHERE enabled;

CREATE TABLE channel_memberships (
    user_id uuid NOT NULL,
    channel_id uuid NOT NULL,
    state text NOT NULL,
    checked_at timestamp with time zone DEFAULT now() NOT NULL,
    left_at timestamp with time zone,
    CONSTRAINT channel_memberships_pkey PRIMARY KEY (user_id, channel_id),
    CONSTRAINT channel_memberships_state_check CHECK ((state = ANY (ARRAY['member'::text, 'absent'::text, 'unknown'::text]))),
    CONSTRAINT channel_memberships_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES required_channels(id) ON DELETE CASCADE,
    CONSTRAINT channel_memberships_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX channel_memberships_absent_idx ON channel_memberships USING btree (left_at) WHERE (state = 'absent'::text);

CREATE INDEX channel_memberships_stale_idx ON channel_memberships USING btree (checked_at);

CREATE TABLE channel_exemptions (
    user_id uuid NOT NULL,
    reason text NOT NULL,
    granted_by uuid,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT channel_exemptions_pkey PRIMARY KEY (user_id),
    CONSTRAINT channel_exemptions_reason_check CHECK (((char_length(reason) >= 1) AND (char_length(reason) <= 400))),
    CONSTRAINT channel_exemptions_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES admin_users(id),
    CONSTRAINT channel_exemptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE channel_enforcement (
    user_id uuid NOT NULL,
    state text DEFAULT 'compliant'::text NOT NULL,
    warned_at timestamp with time zone,
    grace_until timestamp with time zone,
    suspended_at timestamp with time zone,
    restored_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT channel_enforcement_pkey PRIMARY KEY (user_id),
    CONSTRAINT channel_enforcement_state_check CHECK ((state = ANY (ARRAY['compliant'::text, 'warned'::text, 'suspended'::text, 'exempt'::text]))),
    CONSTRAINT channel_enforcement_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX channel_enforcement_due_idx ON channel_enforcement USING btree (grace_until) WHERE (state = 'warned'::text);

-- ----------------------------------------------------------------------------
-- AI, MCP and installation settings
-- ----------------------------------------------------------------------------

CREATE TABLE installation_settings (
    section text NOT NULL,
    document jsonb DEFAULT '{}'::jsonb NOT NULL,
    secrets_ciphertext bytea,
    version integer DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT installation_settings_pkey PRIMARY KEY (section),
    CONSTRAINT installation_settings_section_known CHECK ((section = ANY (ARRAY['branding'::text, 'remnawave'::text, 'telegram'::text, 'operator_group'::text, 'required_channels'::text, 'maintenance'::text, 'notifications'::text, 'telemetry'::text, 'backup'::text, 'security'::text, 'ai'::text, 'mcp'::text, 'customer_auth'::text, 'theme'::text, 'analytics'::text]))),
    CONSTRAINT installation_settings_version_check CHECK ((version > 0)),
    CONSTRAINT installation_settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

COMMENT ON COLUMN installation_settings.secrets_ciphertext IS 'Sealed with APP_DATA_ENCRYPTION_KEY. Never returned to a client, never included in a diagnostics bundle.';

CREATE TABLE ai_providers (
    slug text NOT NULL,
    kind text NOT NULL,
    display_name text NOT NULL,
    base_url text,
    credentials_ciphertext bytea,
    enabled boolean DEFAULT false NOT NULL,
    zero_retention boolean DEFAULT false NOT NULL,
    trains_on_data boolean DEFAULT false NOT NULL,
    retention_notice text,
    data_region text,
    last_checked_at timestamp with time zone,
    last_check_ok boolean,
    last_check_detail text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT ai_providers_pkey PRIMARY KEY (slug),
    CONSTRAINT ai_providers_base_url_check CHECK (((base_url IS NULL) OR (char_length(base_url) <= 400))),
    CONSTRAINT ai_providers_data_region_check CHECK (((data_region IS NULL) OR (char_length(data_region) <= 60))),
    CONSTRAINT ai_providers_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 120))),
    CONSTRAINT ai_providers_kind_check CHECK ((kind = ANY (ARRAY['openai_compatible'::text, 'anthropic'::text, 'gemini'::text]))),
    CONSTRAINT ai_providers_last_check_detail_check CHECK (((last_check_detail IS NULL) OR (char_length(last_check_detail) <= 500))),
    CONSTRAINT ai_providers_retention_notice_check CHECK (((retention_notice IS NULL) OR (char_length(retention_notice) <= 2000))),
    CONSTRAINT ai_providers_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9_-]{1,40}$'::text)),
    CONSTRAINT ai_providers_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE ai_features (
    feature text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    provider_slug text,
    model text,
    temperature numeric(3,2),
    max_tokens integer,
    timeout_ms integer,
    budget_tokens bigint,
    budget_window_seconds integer,
    budget_cost_minor bigint,
    retain_prompts boolean DEFAULT false NOT NULL,
    retain_outputs boolean DEFAULT true NOT NULL,
    retention_days integer DEFAULT 30 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT ai_features_pkey PRIMARY KEY (feature),
    CONSTRAINT ai_features_budget_cost_minor_check CHECK (((budget_cost_minor IS NULL) OR (budget_cost_minor >= 0))),
    CONSTRAINT ai_features_budget_tokens_check CHECK (((budget_tokens IS NULL) OR (budget_tokens >= 0))),
    CONSTRAINT ai_features_budget_window_seconds_check CHECK (((budget_window_seconds IS NULL) OR (budget_window_seconds > 0))),
    CONSTRAINT ai_features_enabled_needs_provider CHECK (((NOT enabled) OR ((provider_slug IS NOT NULL) AND (model IS NOT NULL)))),
    CONSTRAINT ai_features_feature_check CHECK ((feature = ANY (ARRAY['support_summary'::text, 'support_reply'::text, 'support_rewrite'::text, 'support_translate'::text, 'support_classify'::text, 'marketing_draft'::text, 'risk_analysis'::text, 'copilot'::text, 'mcp_tools'::text]))),
    CONSTRAINT ai_features_max_tokens_check CHECK (((max_tokens IS NULL) OR ((max_tokens >= 1) AND (max_tokens <= 200000)))),
    CONSTRAINT ai_features_model_check CHECK (((model IS NULL) OR (char_length(model) <= 120))),
    CONSTRAINT ai_features_retention_days_check CHECK (((retention_days >= 0) AND (retention_days <= 3650))),
    CONSTRAINT ai_features_temperature_check CHECK (((temperature IS NULL) OR ((temperature >= (0)::numeric) AND (temperature <= (2)::numeric)))),
    CONSTRAINT ai_features_timeout_ms_check CHECK (((timeout_ms IS NULL) OR ((timeout_ms >= 1000) AND (timeout_ms <= 600000)))),
    CONSTRAINT ai_features_provider_slug_fkey FOREIGN KEY (provider_slug) REFERENCES ai_providers(slug) ON DELETE RESTRICT,
    CONSTRAINT ai_features_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE ai_usage_limits (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scope text NOT NULL,
    scope_ref text,
    feature text,
    window_seconds integer DEFAULT 86400 NOT NULL,
    max_requests integer,
    max_tokens bigint,
    max_cost_minor bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT ai_usage_limits_pkey PRIMARY KEY (id),
    CONSTRAINT ai_usage_limits_bounds_something CHECK (((max_requests IS NOT NULL) OR (max_tokens IS NOT NULL) OR (max_cost_minor IS NOT NULL))),
    CONSTRAINT ai_usage_limits_feature_check CHECK (((feature IS NULL) OR (char_length(feature) <= 60))),
    CONSTRAINT ai_usage_limits_max_cost_minor_check CHECK (((max_cost_minor IS NULL) OR (max_cost_minor >= 0))),
    CONSTRAINT ai_usage_limits_max_requests_check CHECK (((max_requests IS NULL) OR (max_requests >= 0))),
    CONSTRAINT ai_usage_limits_max_tokens_check CHECK (((max_tokens IS NULL) OR (max_tokens >= 0))),
    CONSTRAINT ai_usage_limits_scope_check CHECK ((scope = ANY (ARRAY['installation'::text, 'role'::text, 'operator'::text, 'feature'::text]))),
    CONSTRAINT ai_usage_limits_scope_ref_check CHECK ((char_length(scope_ref) <= 120)),
    CONSTRAINT ai_usage_limits_scope_ref_shape CHECK ((((scope = 'installation'::text) AND (scope_ref IS NULL)) OR ((scope = ANY (ARRAY['role'::text, 'operator'::text])) AND (scope_ref IS NOT NULL)) OR ((scope = 'feature'::text) AND (scope_ref IS NULL) AND (feature IS NOT NULL)))),
    CONSTRAINT ai_usage_limits_window_seconds_check CHECK ((window_seconds > 0)),
    CONSTRAINT ai_usage_limits_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE UNIQUE INDEX ai_usage_limits_scope_idx ON ai_usage_limits USING btree (scope, COALESCE(scope_ref, ''::text), COALESCE(feature, ''::text));

CREATE TABLE ai_usage_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    feature text NOT NULL,
    task text NOT NULL,
    provider_slug text NOT NULL,
    model text NOT NULL,
    operator_id uuid,
    operator_role text,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    latency_ms integer DEFAULT 0 NOT NULL,
    estimated_cost_minor bigint DEFAULT 0 NOT NULL,
    currency text,
    outcome text NOT NULL,
    error_code text,
    redaction_summary jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT ai_usage_events_pkey PRIMARY KEY (id),
    CONSTRAINT ai_usage_events_currency_check CHECK (((currency IS NULL) OR (currency ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT ai_usage_events_error_code_check CHECK (((error_code IS NULL) OR (char_length(error_code) <= 60))),
    CONSTRAINT ai_usage_events_estimated_cost_minor_check CHECK ((estimated_cost_minor >= 0)),
    CONSTRAINT ai_usage_events_feature_check CHECK (((char_length(feature) >= 1) AND (char_length(feature) <= 60))),
    CONSTRAINT ai_usage_events_input_tokens_check CHECK ((input_tokens >= 0)),
    CONSTRAINT ai_usage_events_latency_ms_check CHECK ((latency_ms >= 0)),
    CONSTRAINT ai_usage_events_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 120))),
    CONSTRAINT ai_usage_events_operator_role_check CHECK (((operator_role IS NULL) OR (char_length(operator_role) <= 60))),
    CONSTRAINT ai_usage_events_outcome_check CHECK ((outcome = ANY (ARRAY['succeeded'::text, 'refused'::text, 'failed'::text, 'timed_out'::text, 'budget_exhausted'::text]))),
    CONSTRAINT ai_usage_events_output_tokens_check CHECK ((output_tokens >= 0)),
    CONSTRAINT ai_usage_events_provider_slug_check CHECK (((char_length(provider_slug) >= 1) AND (char_length(provider_slug) <= 60))),
    CONSTRAINT ai_usage_events_task_check CHECK (((char_length(task) >= 1) AND (char_length(task) <= 60))),
    CONSTRAINT ai_usage_events_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES admin_users(id) ON DELETE SET NULL
);

CREATE INDEX ai_usage_events_operator_idx ON ai_usage_events USING btree (operator_id, occurred_at DESC) WHERE (operator_id IS NOT NULL);

CREATE INDEX ai_usage_events_window_idx ON ai_usage_events USING btree (occurred_at DESC, feature);

CREATE TABLE ai_decisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    feature text NOT NULL,
    provider_slug text NOT NULL,
    model text NOT NULL,
    policy_version text,
    operator_id uuid NOT NULL,
    disposition text NOT NULL,
    consequential boolean DEFAULT false NOT NULL,
    summary text,
    audit_event_id uuid,
    CONSTRAINT ai_decisions_pkey PRIMARY KEY (id),
    CONSTRAINT ai_decisions_disposition_check CHECK ((disposition = ANY (ARRAY['accepted'::text, 'edited'::text, 'rejected'::text]))),
    CONSTRAINT ai_decisions_feature_check CHECK (((char_length(feature) >= 1) AND (char_length(feature) <= 60))),
    CONSTRAINT ai_decisions_model_check CHECK (((char_length(model) >= 1) AND (char_length(model) <= 120))),
    CONSTRAINT ai_decisions_policy_version_check CHECK (((policy_version IS NULL) OR (char_length(policy_version) <= 60))),
    CONSTRAINT ai_decisions_provider_slug_check CHECK (((char_length(provider_slug) >= 1) AND (char_length(provider_slug) <= 60))),
    CONSTRAINT ai_decisions_subject_id_check CHECK (((char_length(subject_id) >= 1) AND (char_length(subject_id) <= 120))),
    CONSTRAINT ai_decisions_subject_type_check CHECK ((subject_type = ANY (ARRAY['support_ticket'::text, 'customer'::text, 'order'::text, 'refund'::text, 'campaign'::text, 'news_post'::text, 'subscription'::text, 'risk_assessment'::text]))),
    CONSTRAINT ai_decisions_summary_check CHECK (((summary IS NULL) OR (char_length(summary) <= 500))),
    CONSTRAINT ai_decisions_audit_event_id_fkey FOREIGN KEY (audit_event_id) REFERENCES audit_events(id),
    CONSTRAINT ai_decisions_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES admin_users(id)
);

CREATE INDEX ai_decisions_consequential_idx ON ai_decisions USING btree (occurred_at DESC) WHERE consequential;

CREATE INDEX ai_decisions_subject_idx ON ai_decisions USING btree (subject_type, subject_id, occurred_at DESC);

CREATE TABLE ai_retention_holds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    reason text NOT NULL,
    placed_by uuid NOT NULL,
    placed_at timestamp with time zone DEFAULT now() NOT NULL,
    released_at timestamp with time zone,
    released_by uuid,
    CONSTRAINT ai_retention_holds_pkey PRIMARY KEY (id),
    CONSTRAINT ai_retention_holds_reason_check CHECK (((char_length(reason) >= 3) AND (char_length(reason) <= 500))),
    CONSTRAINT ai_retention_holds_release_shape CHECK (((released_at IS NULL) = (released_by IS NULL))),
    CONSTRAINT ai_retention_holds_subject_id_check CHECK (((char_length(subject_id) >= 1) AND (char_length(subject_id) <= 120))),
    CONSTRAINT ai_retention_holds_subject_type_check CHECK (((char_length(subject_type) >= 1) AND (char_length(subject_type) <= 60))),
    CONSTRAINT ai_retention_holds_placed_by_fkey FOREIGN KEY (placed_by) REFERENCES admin_users(id),
    CONSTRAINT ai_retention_holds_released_by_fkey FOREIGN KEY (released_by) REFERENCES admin_users(id)
);

CREATE UNIQUE INDEX ai_retention_holds_active_idx ON ai_retention_holds USING btree (subject_type, subject_id) WHERE (released_at IS NULL);

CREATE TABLE mcp_servers (
    slug text NOT NULL,
    display_name text NOT NULL,
    endpoint text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    credentials_ciphertext bytea,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    allow_private_network boolean DEFAULT false NOT NULL,
    timeout_ms integer DEFAULT 20000 NOT NULL,
    max_response_bytes bigint DEFAULT 262144 NOT NULL,
    max_calls_per_request integer DEFAULT 8 NOT NULL,
    max_depth integer DEFAULT 3 NOT NULL,
    cost_limit_minor bigint,
    protocol_version text,
    server_name text,
    server_version text,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    discovered_at timestamp with time zone,
    last_checked_at timestamp with time zone,
    last_check_ok boolean,
    last_check_detail text,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT mcp_servers_pkey PRIMARY KEY (slug),
    CONSTRAINT mcp_servers_consecutive_failures_check CHECK ((consecutive_failures >= 0)),
    CONSTRAINT mcp_servers_cost_limit_minor_check CHECK (((cost_limit_minor IS NULL) OR (cost_limit_minor >= 0))),
    CONSTRAINT mcp_servers_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 120))),
    CONSTRAINT mcp_servers_endpoint_check CHECK (((char_length(endpoint) >= 8) AND (char_length(endpoint) <= 500))),
    CONSTRAINT mcp_servers_last_check_detail_check CHECK (((last_check_detail IS NULL) OR (char_length(last_check_detail) <= 500))),
    CONSTRAINT mcp_servers_max_calls_per_request_check CHECK (((max_calls_per_request >= 1) AND (max_calls_per_request <= 100))),
    CONSTRAINT mcp_servers_max_depth_check CHECK (((max_depth >= 1) AND (max_depth <= 10))),
    CONSTRAINT mcp_servers_max_response_bytes_check CHECK (((max_response_bytes >= 1024) AND (max_response_bytes <= 8388608))),
    CONSTRAINT mcp_servers_protocol_version_check CHECK (((protocol_version IS NULL) OR (char_length(protocol_version) <= 40))),
    CONSTRAINT mcp_servers_requires_tls CHECK (((endpoint ~~ 'https://%'::text) OR (allow_private_network AND (endpoint ~~ 'http://%'::text)))),
    CONSTRAINT mcp_servers_server_name_check CHECK (((server_name IS NULL) OR (char_length(server_name) <= 200))),
    CONSTRAINT mcp_servers_server_version_check CHECK (((server_version IS NULL) OR (char_length(server_version) <= 60))),
    CONSTRAINT mcp_servers_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9_-]{1,40}$'::text)),
    CONSTRAINT mcp_servers_timeout_ms_check CHECK (((timeout_ms >= 1000) AND (timeout_ms <= 120000))),
    CONSTRAINT mcp_servers_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE mcp_tools (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    server_slug text NOT NULL,
    tool_name text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    permission text NOT NULL,
    writes boolean DEFAULT true NOT NULL,
    input_schema jsonb,
    output_schema jsonb,
    description text,
    schema_usable boolean DEFAULT false NOT NULL,
    schema_problem text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT mcp_tools_pkey PRIMARY KEY (id),
    CONSTRAINT mcp_tools_server_slug_tool_name_key UNIQUE (server_slug, tool_name),
    CONSTRAINT mcp_tools_description_check CHECK (((description IS NULL) OR (char_length(description) <= 2000))),
    CONSTRAINT mcp_tools_permission_check CHECK (((char_length(permission) >= 3) AND (char_length(permission) <= 60))),
    CONSTRAINT mcp_tools_schema_problem_check CHECK (((schema_problem IS NULL) OR (char_length(schema_problem) <= 500))),
    CONSTRAINT mcp_tools_tool_name_check CHECK (((char_length(tool_name) >= 1) AND (char_length(tool_name) <= 120))),
    CONSTRAINT mcp_tools_server_slug_fkey FOREIGN KEY (server_slug) REFERENCES mcp_servers(slug) ON DELETE CASCADE
);

CREATE TABLE mcp_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    kind text NOT NULL,
    server_slug text,
    tool_name text,
    operator_id uuid,
    arguments jsonb DEFAULT '{}'::jsonb NOT NULL,
    confirmed boolean DEFAULT false NOT NULL,
    reason text,
    outcome text NOT NULL,
    detail text,
    response_bytes bigint DEFAULT 0 NOT NULL,
    duration_ms integer DEFAULT 0 NOT NULL,
    findings text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT mcp_events_pkey PRIMARY KEY (id),
    CONSTRAINT mcp_events_detail_check CHECK (((detail IS NULL) OR (char_length(detail) <= 1000))),
    CONSTRAINT mcp_events_duration_ms_check CHECK ((duration_ms >= 0)),
    CONSTRAINT mcp_events_kind_check CHECK ((kind = ANY (ARRAY['connection_changed'::text, 'discovery'::text, 'tool_call'::text, 'confirmation'::text, 'failure'::text]))),
    CONSTRAINT mcp_events_outcome_check CHECK ((outcome = ANY (ARRAY['allowed'::text, 'refused'::text, 'failed'::text, 'replayed'::text]))),
    CONSTRAINT mcp_events_reason_check CHECK (((reason IS NULL) OR (char_length(reason) <= 500))),
    CONSTRAINT mcp_events_response_bytes_check CHECK ((response_bytes >= 0)),
    CONSTRAINT mcp_events_server_slug_check CHECK (((server_slug IS NULL) OR (char_length(server_slug) <= 60))),
    CONSTRAINT mcp_events_tool_name_check CHECK (((tool_name IS NULL) OR (char_length(tool_name) <= 120))),
    CONSTRAINT mcp_events_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES admin_users(id) ON DELETE SET NULL
);

CREATE INDEX mcp_events_recent_idx ON mcp_events USING btree (occurred_at DESC);

CREATE INDEX mcp_events_server_idx ON mcp_events USING btree (server_slug, occurred_at DESC) WHERE (server_slug IS NOT NULL);

-- ----------------------------------------------------------------------------
-- Goods promotions and carts
-- ----------------------------------------------------------------------------

CREATE TABLE promotion_goods (
    promotion_id uuid NOT NULL,
    product_id uuid NOT NULL,
    CONSTRAINT promotion_goods_pkey PRIMARY KEY (promotion_id, product_id),
    CONSTRAINT promotion_goods_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id),
    CONSTRAINT promotion_goods_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES promotions(id) ON DELETE CASCADE
);

CREATE TABLE cart_goods (
    cart_id uuid NOT NULL,
    product_id uuid NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    recipient_username text NOT NULL,
    recipient_is_self boolean DEFAULT false NOT NULL,
    saved_price_minor bigint NOT NULL,
    currency text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    star_quantity integer,
    CONSTRAINT cart_goods_pkey PRIMARY KEY (cart_id),
    CONSTRAINT cart_goods_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT cart_goods_quantity_check CHECK ((quantity > 0)),
    CONSTRAINT cart_goods_recipient_username_check CHECK ((recipient_username ~ '^[A-Za-z0-9_]{5,32}$'::text)),
    CONSTRAINT cart_goods_saved_price_minor_check CHECK ((saved_price_minor >= 0)),
    CONSTRAINT cart_goods_star_quantity_positive CHECK (((star_quantity IS NULL) OR (star_quantity > 0))),
    CONSTRAINT cart_goods_cart_id_fkey FOREIGN KEY (cart_id) REFERENCES carts(id) ON DELETE CASCADE,
    CONSTRAINT cart_goods_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id)
);

-- ----------------------------------------------------------------------------
-- Customer web panel
-- ----------------------------------------------------------------------------

CREATE TABLE customer_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    csrf_secret bytea NOT NULL,
    auth_method text NOT NULL,
    auth_provider text,
    ip inet,
    user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone DEFAULT now() NOT NULL,
    idle_expires_at timestamp with time zone NOT NULL,
    absolute_expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    revoked_reason text,
    CONSTRAINT customer_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT customer_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT customer_sessions_absolute_after_creation CHECK ((absolute_expires_at >= created_at)),
    CONSTRAINT customer_sessions_auth_method_check CHECK ((auth_method = ANY (ARRAY['telegram'::text, 'magic_link'::text, 'oidc'::text]))),
    CONSTRAINT customer_sessions_auth_provider_check CHECK (((auth_provider IS NULL) OR ((char_length(auth_provider) >= 1) AND (char_length(auth_provider) <= 40)))),
    CONSTRAINT customer_sessions_csrf_secret_check CHECK ((octet_length(csrf_secret) = 32)),
    CONSTRAINT customer_sessions_provider_present_for_oidc CHECK (((auth_method = 'oidc'::text) = (auth_provider IS NOT NULL))),
    CONSTRAINT customer_sessions_revocation_complete CHECK (((revoked_at IS NULL) = (revoked_reason IS NULL))),
    CONSTRAINT customer_sessions_revoked_reason_known CHECK (((revoked_reason IS NULL) OR (revoked_reason = ANY (ARRAY['logout'::text, 'logout_all'::text, 'session_revoked'::text, 'identity_unlinked'::text, 'provider_disabled'::text, 'account_suspended'::text, 'account_deleted'::text, 'expired'::text, 'reauthenticated'::text])))),
    CONSTRAINT customer_sessions_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT customer_sessions_user_agent_check CHECK (((user_agent IS NULL) OR (char_length(user_agent) <= 400))),
    CONSTRAINT customer_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX customer_sessions_expiry_idx ON customer_sessions USING btree (absolute_expires_at) WHERE (revoked_at IS NULL);

CREATE INDEX customer_sessions_live_idx ON customer_sessions USING btree (user_id, last_seen_at DESC) WHERE (revoked_at IS NULL);

CREATE TABLE customer_magic_links (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    requested_ip inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    consumed_ip inet,
    CONSTRAINT customer_magic_links_pkey PRIMARY KEY (id),
    CONSTRAINT customer_magic_links_token_hash_key UNIQUE (token_hash),
    CONSTRAINT customer_magic_links_consumption_complete CHECK (((consumed_ip IS NULL) OR (consumed_at IS NOT NULL))),
    CONSTRAINT customer_magic_links_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT customer_magic_links_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT customer_magic_links_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX customer_magic_links_pending_idx ON customer_magic_links USING btree (user_id, created_at DESC) WHERE (consumed_at IS NULL);

CREATE TABLE customer_security_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    event text NOT NULL,
    ip inet,
    user_agent text,
    request_id text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT customer_security_events_pkey PRIMARY KEY (id),
    CONSTRAINT customer_security_events_event_check CHECK ((event = ANY (ARRAY['signed_in'::text, 'signed_out'::text, 'signed_out_all'::text, 'session_revoked'::text, 'magic_link_requested'::text, 'identity_linked'::text, 'identity_unlinked'::text, 'subscription_key_rotated'::text, 'device_removed'::text, 'devices_removed_all'::text]))),
    CONSTRAINT customer_security_events_request_id_check CHECK (((request_id IS NULL) OR (char_length(request_id) <= 64))),
    CONSTRAINT customer_security_events_user_agent_check CHECK (((user_agent IS NULL) OR (char_length(user_agent) <= 400))),
    CONSTRAINT customer_security_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX customer_security_events_recent_idx ON customer_security_events USING btree (user_id, occurred_at DESC);

CREATE TABLE customer_oidc_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    display_name text NOT NULL,
    issuer text NOT NULL,
    discovery_url text NOT NULL,
    client_id text NOT NULL,
    client_secret_ciphertext bytea,
    scopes text[] DEFAULT '{openid,email,profile}'::text[] NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    icon text,
    sort_order integer DEFAULT 0 NOT NULL,
    require_verified_email boolean DEFAULT true NOT NULL,
    allow_auto_provision boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    disabled_at timestamp with time zone,
    CONSTRAINT customer_oidc_providers_pkey PRIMARY KEY (id),
    CONSTRAINT customer_oidc_providers_slug_key UNIQUE (slug),
    CONSTRAINT customer_oidc_providers_client_id_check CHECK (((char_length(client_id) >= 1) AND (char_length(client_id) <= 256))),
    CONSTRAINT customer_oidc_providers_discovery_url_check CHECK ((discovery_url ~ '^https://'::text)),
    CONSTRAINT customer_oidc_providers_display_name_check CHECK (((char_length(display_name) >= 1) AND (char_length(display_name) <= 80))),
    CONSTRAINT customer_oidc_providers_icon_check CHECK (((icon IS NULL) OR (icon ~ '^[a-z0-9][a-z0-9-]{0,38}$'::text))),
    CONSTRAINT customer_oidc_providers_issuer_check CHECK ((issuer ~ '^https://'::text)),
    CONSTRAINT customer_oidc_providers_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$'::text))
);

CREATE INDEX customer_oidc_providers_enabled_idx ON customer_oidc_providers USING btree (sort_order, slug) WHERE enabled;

-- ----------------------------------------------------------------------------
-- Campaign test sends
-- ----------------------------------------------------------------------------

CREATE TABLE campaign_test_sends (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    campaign_id uuid NOT NULL,
    locale text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    error_code text,
    requested_by uuid,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT campaign_test_sends_pkey PRIMARY KEY (id),
    CONSTRAINT campaign_test_sends_error_code_check CHECK (((error_code IS NULL) OR (char_length(error_code) <= 60))),
    CONSTRAINT campaign_test_sends_locale_check CHECK ((locale = ANY (ARRAY['en'::text, 'ru'::text]))),
    CONSTRAINT campaign_test_sends_resolution_shape CHECK (((status = 'queued'::text) = (resolved_at IS NULL))),
    CONSTRAINT campaign_test_sends_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT campaign_test_sends_campaign_id_fkey FOREIGN KEY (campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE,
    CONSTRAINT campaign_test_sends_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES admin_users(id)
);

CREATE INDEX campaign_test_sends_campaign_idx ON campaign_test_sends USING btree (campaign_id, requested_at DESC);

CREATE INDEX campaign_test_sends_pending_idx ON campaign_test_sends USING btree (requested_at) WHERE (status = 'queued'::text);

-- ----------------------------------------------------------------------------
-- Operator passkeys
-- ----------------------------------------------------------------------------

CREATE TABLE admin_passkeys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    admin_user_id uuid NOT NULL,
    credential_id bytea NOT NULL,
    public_key bytea NOT NULL,
    label text NOT NULL,
    sign_count bigint DEFAULT 0 NOT NULL,
    aaguid bytea,
    discoverable boolean DEFAULT true NOT NULL,
    user_verified boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_ip inet,
    last_used_at timestamp with time zone,
    last_used_ip inet,
    CONSTRAINT admin_passkeys_pkey PRIMARY KEY (id),
    CONSTRAINT admin_passkeys_credential_id_key UNIQUE (credential_id),
    CONSTRAINT admin_passkeys_label_check CHECK (((char_length(label) >= 1) AND (char_length(label) <= 60))),
    CONSTRAINT admin_passkeys_sign_count_check CHECK ((sign_count >= 0)),
    CONSTRAINT admin_passkeys_admin_user_id_fkey FOREIGN KEY (admin_user_id) REFERENCES admin_users(id) ON DELETE CASCADE
);

COMMENT ON COLUMN admin_passkeys.public_key IS 'The credential public key. The private half never leaves the authenticator, so this column holds nothing an attacker can use.';

COMMENT ON COLUMN admin_passkeys.sign_count IS 'Last signature counter seen. A decrease indicates a cloned authenticator; a permanent zero means the authenticator does not implement one.';

CREATE INDEX admin_passkeys_owner_idx ON admin_passkeys USING btree (admin_user_id, created_at DESC);

-- ----------------------------------------------------------------------------
-- Branding assets
-- ----------------------------------------------------------------------------

CREATE TABLE branding_assets (
    kind text NOT NULL,
    content_type text NOT NULL,
    bytes bytea NOT NULL,
    checksum text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT branding_assets_pkey PRIMARY KEY (kind),
    CONSTRAINT branding_assets_bytes_check CHECK (((octet_length(bytes) >= 1) AND (octet_length(bytes) <= 262144))),
    CONSTRAINT branding_assets_checksum_check CHECK ((checksum ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT branding_assets_content_type_check CHECK ((content_type = ANY (ARRAY['image/png'::text, 'image/jpeg'::text, 'image/webp'::text]))),
    CONSTRAINT branding_assets_kind_check CHECK ((kind = ANY (ARRAY['logo_light'::text, 'logo_dark'::text, 'favicon'::text]))),
    CONSTRAINT branding_assets_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

COMMENT ON TABLE branding_assets IS 'Operator-supplied brand images, served publicly by checksum. Never customer data.';

-- ----------------------------------------------------------------------------
-- Client catalogue
-- ----------------------------------------------------------------------------

CREATE TABLE connect_platforms (
    slug text NOT NULL,
    label_en text NOT NULL,
    label_ru text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT connect_platforms_pkey PRIMARY KEY (slug),
    CONSTRAINT connect_platforms_label_en_check CHECK (((char_length(label_en) >= 1) AND (char_length(label_en) <= 40))),
    CONSTRAINT connect_platforms_label_ru_check CHECK (((char_length(label_ru) >= 1) AND (char_length(label_ru) <= 40))),
    CONSTRAINT connect_platforms_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9_-]{0,30}$'::text)),
    CONSTRAINT connect_platforms_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE TABLE connect_clients (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    platform_slug text NOT NULL,
    name text NOT NULL,
    scheme text NOT NULL,
    download_url text,
    instructions_en text,
    instructions_ru text,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT connect_clients_pkey PRIMARY KEY (id),
    CONSTRAINT connect_clients_platform_slug_name_key UNIQUE (platform_slug, name),
    CONSTRAINT connect_clients_download_url_check CHECK (((download_url IS NULL) OR ((download_url ~ '^https://'::text) AND ((char_length(download_url) >= 11) AND (char_length(download_url) <= 300)) AND (download_url !~ '[[:space:]]'::text)))),
    CONSTRAINT connect_clients_instructions_en_check CHECK (((instructions_en IS NULL) OR (char_length(instructions_en) <= 2000))),
    CONSTRAINT connect_clients_instructions_ru_check CHECK (((instructions_ru IS NULL) OR (char_length(instructions_ru) <= 2000))),
    CONSTRAINT connect_clients_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 60))),
    CONSTRAINT connect_clients_scheme_check CHECK ((scheme ~ '^[a-z][a-z0-9+.-]{1,30}://[A-Za-z0-9/_.~-]{0,60}$'::text)),
    CONSTRAINT connect_clients_scheme_safe CHECK ((lower(split_part(scheme, ':'::text, 1)) <> ALL (ARRAY['javascript'::text, 'data'::text, 'vbscript'::text, 'file'::text]))),
    CONSTRAINT connect_clients_platform_slug_fkey FOREIGN KEY (platform_slug) REFERENCES connect_platforms(slug) ON DELETE CASCADE,
    CONSTRAINT connect_clients_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

COMMENT ON TABLE connect_clients IS 'Operator-editable connection guidance. Read by the bot and the customer web panel from one place so they cannot recommend different applications.';

CREATE INDEX connect_clients_platform_idx ON connect_clients USING btree (platform_slug, sort_order, name);

-- ----------------------------------------------------------------------------
-- Info pages
-- ----------------------------------------------------------------------------

CREATE TABLE info_pages (
    slug text NOT NULL,
    kind text NOT NULL,
    published_at timestamp with time zone,
    listed boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT info_pages_pkey PRIMARY KEY (slug),
    CONSTRAINT info_pages_kind_check CHECK ((kind = ANY (ARRAY['faq'::text, 'terms'::text, 'offer'::text, 'privacy'::text, 'custom'::text]))),
    CONSTRAINT info_pages_slug_check CHECK ((slug ~ '^[a-z0-9][a-z0-9-]{1,48}$'::text)),
    CONSTRAINT info_pages_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

CREATE INDEX info_pages_published_idx ON info_pages USING btree (sort_order, slug) WHERE (published_at IS NOT NULL);

CREATE TABLE info_page_localizations (
    page_slug text NOT NULL,
    locale text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    CONSTRAINT info_page_localizations_pkey PRIMARY KEY (page_slug, locale),
    CONSTRAINT info_page_localizations_body_check CHECK (((char_length(body) >= 1) AND (char_length(body) <= 40000))),
    CONSTRAINT info_page_localizations_locale_check CHECK ((locale = ANY (ARRAY['ru'::text, 'en'::text]))),
    CONSTRAINT info_page_localizations_title_check CHECK (((char_length(title) >= 1) AND (char_length(title) <= 200))),
    CONSTRAINT info_page_localizations_page_slug_fkey FOREIGN KEY (page_slug) REFERENCES info_pages(slug) ON DELETE CASCADE
);

COMMENT ON COLUMN info_page_localizations.body IS 'Operator source text. Parsed into typed blocks by internal/infopage and never rendered as HTML.';

-- ----------------------------------------------------------------------------
-- Access code batches
-- ----------------------------------------------------------------------------

CREATE TABLE code_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    reference text NOT NULL,
    plan_version_id uuid NOT NULL,
    quantity integer NOT NULL,
    unit_price_minor bigint NOT NULL,
    currency text NOT NULL,
    note text,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    revoke_reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    CONSTRAINT code_batches_pkey PRIMARY KEY (id),
    CONSTRAINT code_batches_reference_key UNIQUE (reference),
    CONSTRAINT code_batches_currency_check CHECK ((currency ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT code_batches_note_check CHECK (((note IS NULL) OR (char_length(note) <= 500))),
    CONSTRAINT code_batches_quantity_check CHECK (((quantity >= 1) AND (quantity <= 10000))),
    CONSTRAINT code_batches_reference_check CHECK ((reference ~ '^[a-zA-Z0-9][a-zA-Z0-9 ._-]{2,79}$'::text)),
    CONSTRAINT code_batches_revocation_complete CHECK (((revoked_at IS NULL) = (revoke_reason IS NULL))),
    CONSTRAINT code_batches_revoke_reason_check CHECK (((revoke_reason IS NULL) OR ((char_length(revoke_reason) >= 3) AND (char_length(revoke_reason) <= 500)))),
    CONSTRAINT code_batches_unit_price_minor_check CHECK ((unit_price_minor >= 0)),
    CONSTRAINT code_batches_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id),
    CONSTRAINT code_batches_plan_version_id_fkey FOREIGN KEY (plan_version_id) REFERENCES plan_versions(id),
    CONSTRAINT code_batches_revoked_by_fkey FOREIGN KEY (revoked_by) REFERENCES admin_users(id)
);

CREATE TABLE access_codes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    batch_id uuid NOT NULL,
    code_hash bytea NOT NULL,
    code_hint text NOT NULL,
    status text DEFAULT 'issued'::text NOT NULL,
    redeemed_by uuid,
    redeemed_entitlement_id uuid,
    redeemed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT access_codes_pkey PRIMARY KEY (id),
    CONSTRAINT access_codes_code_hash_key UNIQUE (code_hash),
    CONSTRAINT access_codes_code_hash_check CHECK ((octet_length(code_hash) = 32)),
    CONSTRAINT access_codes_code_hint_check CHECK ((code_hint ~ '^[0-9A-Z]{4}$'::text)),
    CONSTRAINT access_codes_redemption_complete CHECK ((((status = 'redeemed'::text) = (redeemed_at IS NOT NULL)) AND ((redeemed_at IS NULL) = (redeemed_by IS NULL)))),
    CONSTRAINT access_codes_status_check CHECK ((status = ANY (ARRAY['issued'::text, 'redeemed'::text, 'revoked'::text]))),
    CONSTRAINT access_codes_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES code_batches(id) ON DELETE CASCADE,
    CONSTRAINT access_codes_redeemed_by_fkey FOREIGN KEY (redeemed_by) REFERENCES users(id),
    CONSTRAINT access_codes_redeemed_entitlement_id_fkey FOREIGN KEY (redeemed_entitlement_id) REFERENCES entitlements(id)
);

COMMENT ON COLUMN access_codes.code_hash IS 'SHA-256 of the code. The plaintext is returned once, when the batch is generated, and is never recoverable afterwards.';

CREATE INDEX access_codes_batch_idx ON access_codes USING btree (batch_id, status);

-- ----------------------------------------------------------------------------
-- Notice overrides
-- ----------------------------------------------------------------------------

CREATE TABLE notice_overrides (
    code text NOT NULL,
    locale text NOT NULL,
    body text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT notice_overrides_pkey PRIMARY KEY (code, locale),
    CONSTRAINT notice_overrides_body_length CHECK (((char_length(body) >= 1) AND (char_length(body) <= 2000))),
    CONSTRAINT notice_overrides_code_shape CHECK ((code ~ '^[a-z][a-z0-9_]{1,63}$'::text)),
    CONSTRAINT notice_overrides_locale_known CHECK ((locale = ANY (ARRAY['en'::text, 'ru'::text]))),
    CONSTRAINT notice_overrides_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES admin_users(id)
);

COMMENT ON TABLE notice_overrides IS 'Operator wording for transactional notices. A missing row means the shipped default, so deleting one is a real revert.';

CREATE TABLE notice_test_sends (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    locale text NOT NULL,
    body text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    error_code text,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    requested_by uuid,
    CONSTRAINT notice_test_sends_pkey PRIMARY KEY (id),
    CONSTRAINT notice_test_sends_body_length CHECK (((char_length(body) >= 1) AND (char_length(body) <= 2000))),
    CONSTRAINT notice_test_sends_locale_known CHECK ((locale = ANY (ARRAY['en'::text, 'ru'::text]))),
    CONSTRAINT notice_test_sends_resolution CHECK (((status = 'pending'::text) = (resolved_at IS NULL))),
    CONSTRAINT notice_test_sends_status_known CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT notice_test_sends_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES admin_users(id)
);

COMMENT ON TABLE notice_test_sends IS 'One rendered copy of a transactional notice, queued for the operator group. Never sent to a customer.';

CREATE INDEX notice_test_sends_pending_idx ON notice_test_sends USING btree (requested_at) WHERE (status = 'pending'::text);

CREATE INDEX notice_test_sends_recent_idx ON notice_test_sends USING btree (code, requested_at DESC);

-- ----------------------------------------------------------------------------
-- Advertising measurement
-- ----------------------------------------------------------------------------

CREATE TABLE order_attributions (
    order_id uuid NOT NULL,
    click_id text,
    click_source text,
    utm_source text,
    utm_medium text,
    utm_campaign text,
    utm_content text,
    utm_term text,
    recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT order_attributions_pkey PRIMARY KEY (order_id),
    CONSTRAINT order_attributions_campaign_length CHECK (((COALESCE(char_length(utm_source), 0) <= 120) AND (COALESCE(char_length(utm_medium), 0) <= 120) AND (COALESCE(char_length(utm_campaign), 0) <= 120) AND (COALESCE(char_length(utm_content), 0) <= 120) AND (COALESCE(char_length(utm_term), 0) <= 120))),
    CONSTRAINT order_attributions_click_pair CHECK (((click_id IS NULL) = (click_source IS NULL))),
    CONSTRAINT order_attributions_click_shape CHECK (((click_id IS NULL) OR (click_id ~ '^[A-Za-z0-9_.-]{6,200}$'::text))),
    CONSTRAINT order_attributions_not_empty CHECK (((click_id IS NOT NULL) OR (utm_source IS NOT NULL) OR (utm_medium IS NOT NULL) OR (utm_campaign IS NOT NULL) OR (utm_content IS NOT NULL) OR (utm_term IS NOT NULL))),
    CONSTRAINT order_attributions_source_known CHECK (((click_source IS NULL) OR (click_source = ANY (ARRAY['google'::text, 'yandex'::text, 'meta'::text, 'microsoft'::text, 'tiktok'::text, 'x'::text])))),
    CONSTRAINT order_attributions_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE
);

COMMENT ON TABLE order_attributions IS 'Where one order came from, for the operator''s own advertising measurement. Never sent anywhere by this software; an operator exports a file and uploads it themselves.';

CREATE INDEX order_attributions_source_idx ON order_attributions USING btree (click_source) WHERE (click_id IS NOT NULL);

-- ----------------------------------------------------------------------------
-- Shop reviews
-- ----------------------------------------------------------------------------

CREATE TABLE bot_shop_reviews (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    product_id uuid NOT NULL,
    recipient text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone DEFAULT (now() + '1 day'::interval) NOT NULL,
    CONSTRAINT bot_shop_reviews_pkey PRIMARY KEY (id),
    CONSTRAINT bot_shop_reviews_recipient_check CHECK (((char_length(recipient) >= 1) AND (char_length(recipient) <= 64))),
    CONSTRAINT bot_shop_reviews_product_id_fkey FOREIGN KEY (product_id) REFERENCES goods_products(id) ON DELETE CASCADE,
    CONSTRAINT bot_shop_reviews_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX bot_shop_reviews_expiry_idx ON bot_shop_reviews USING btree (expires_at);

CREATE INDEX bot_shop_reviews_user_idx ON bot_shop_reviews USING btree (user_id, expires_at);

-- ----------------------------------------------------------------------------
-- Operator invitations
-- ----------------------------------------------------------------------------

CREATE TABLE admin_invitations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    email_normalized text NOT NULL,
    roles text[] NOT NULL,
    token_hash bytea NOT NULL,
    invited_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone,
    accepted_admin_user_id uuid,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    CONSTRAINT admin_invitations_pkey PRIMARY KEY (id),
    CONSTRAINT admin_invitations_token_hash_key UNIQUE (token_hash),
    CONSTRAINT admin_invitations_acceptance_complete CHECK (((accepted_at IS NULL) = (accepted_admin_user_id IS NULL))),
    CONSTRAINT admin_invitations_email_check CHECK (((char_length(email) >= 3) AND (char_length(email) <= 254))),
    CONSTRAINT admin_invitations_email_normalized_check CHECK (((char_length(email_normalized) >= 3) AND (char_length(email_normalized) <= 254))),
    CONSTRAINT admin_invitations_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT admin_invitations_revocation_complete CHECK (((revoked_at IS NULL) = (revoked_by IS NULL))),
    CONSTRAINT admin_invitations_roles_known CHECK (
        cardinality(roles) BETWEEN 1 AND 6
        AND roles <@ ARRAY['owner', 'administrator', 'support', 'finance', 'marketing', 'auditor']::text[]
    ),
    CONSTRAINT admin_invitations_settled_once CHECK (((accepted_at IS NULL) OR (revoked_at IS NULL))),
    CONSTRAINT admin_invitations_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT admin_invitations_accepted_admin_user_id_fkey FOREIGN KEY (accepted_admin_user_id) REFERENCES admin_users(id),
    CONSTRAINT admin_invitations_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES admin_users(id),
    CONSTRAINT admin_invitations_revoked_by_fkey FOREIGN KEY (revoked_by) REFERENCES admin_users(id)
);

CREATE UNIQUE INDEX admin_invitations_one_open_per_email_idx ON admin_invitations USING btree (email_normalized) WHERE ((accepted_at IS NULL) AND (revoked_at IS NULL));

CREATE INDEX admin_invitations_open_idx ON admin_invitations USING btree (created_at DESC) WHERE ((accepted_at IS NULL) AND (revoked_at IS NULL));

-- ----------------------------------------------------------------------------
-- Bedolaga import
-- ----------------------------------------------------------------------------

CREATE TABLE bedolaga_import_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    report jsonb DEFAULT '{}'::jsonb NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT bedolaga_import_runs_pkey PRIMARY KEY (id),
    CONSTRAINT bedolaga_import_runs_actor_length CHECK (((char_length(actor) >= 1) AND (char_length(actor) <= 120))),
    CONSTRAINT bedolaga_import_runs_finish_consistent CHECK (((status = 'running'::text) = (finished_at IS NULL))),
    CONSTRAINT bedolaga_import_runs_status_known CHECK ((status = ANY (ARRAY['running'::text, 'completed'::text, 'failed'::text])))
);

CREATE TABLE bedolaga_import_items (
    kind text NOT NULL,
    source_id bigint NOT NULL,
    status text NOT NULL,
    codes text[] DEFAULT '{}'::text[] NOT NULL,
    user_id uuid,
    subscription_id uuid,
    entitlement_id uuid,
    fingerprint bytea NOT NULL,
    last_run_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_at timestamp with time zone,
    CONSTRAINT bedolaga_import_items_pkey PRIMARY KEY (kind, source_id),
    CONSTRAINT bedolaga_import_items_applied_complete CHECK ((((status = 'applied'::text) = (applied_at IS NOT NULL)) AND ((status <> 'applied'::text) OR (user_id IS NOT NULL)))),
    CONSTRAINT bedolaga_import_items_fingerprint_length CHECK ((octet_length(fingerprint) = 32)),
    CONSTRAINT bedolaga_import_items_kind_known CHECK ((kind = ANY (ARRAY['user'::text, 'subscription'::text, 'referral'::text]))),
    CONSTRAINT bedolaga_import_items_status_known CHECK ((status = ANY (ARRAY['applied'::text, 'conflict'::text, 'skipped'::text]))),
    CONSTRAINT bedolaga_import_items_entitlement_id_fkey FOREIGN KEY (entitlement_id) REFERENCES entitlements(id),
    CONSTRAINT bedolaga_import_items_last_run_id_fkey FOREIGN KEY (last_run_id) REFERENCES bedolaga_import_runs(id),
    CONSTRAINT bedolaga_import_items_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id),
    CONSTRAINT bedolaga_import_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE INDEX bedolaga_import_items_status_idx ON bedolaga_import_items USING btree (kind, status);

CREATE INDEX bedolaga_import_items_user_idx ON bedolaga_import_items USING btree (user_id) WHERE (user_id IS NOT NULL);

-- ----------------------------------------------------------------------------
-- River job queue
-- ----------------------------------------------------------------------------

CREATE TYPE river_job_state AS ENUM (
    'available',
    'cancelled',
    'completed',
    'discarded',
    'pending',
    'retryable',
    'running',
    'scheduled'
);

CREATE FUNCTION river_job_state_in_bitmask(bitmask bit, state river_job_state) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
    SELECT CASE state
        WHEN 'available' THEN get_bit(bitmask, 7)
        WHEN 'cancelled' THEN get_bit(bitmask, 6)
        WHEN 'completed' THEN get_bit(bitmask, 5)
        WHEN 'discarded' THEN get_bit(bitmask, 4)
        WHEN 'pending'   THEN get_bit(bitmask, 3)
        WHEN 'retryable' THEN get_bit(bitmask, 2)
        WHEN 'running'   THEN get_bit(bitmask, 1)
        WHEN 'scheduled' THEN get_bit(bitmask, 0)
        ELSE 0
    END = 1;
$$;

CREATE TABLE river_migration (
    line text NOT NULL,
    version bigint CONSTRAINT river_migration_version_not_null1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT river_migration_created_at_not_null1 NOT NULL,
    CONSTRAINT river_migration_pkey1 PRIMARY KEY (line, version),
    CONSTRAINT line_length CHECK (((char_length(line) > 0) AND (char_length(line) < 128))),
    CONSTRAINT version_gte_1 CHECK ((version >= 1))
);

CREATE TABLE river_job (
    id bigserial,
    state river_job_state DEFAULT 'available'::river_job_state NOT NULL,
    attempt smallint DEFAULT 0 NOT NULL,
    max_attempts smallint DEFAULT 25 NOT NULL,
    attempted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    finalized_at timestamp with time zone,
    scheduled_at timestamp with time zone DEFAULT now() NOT NULL,
    priority smallint DEFAULT 1 NOT NULL,
    args jsonb NOT NULL,
    attempted_by text[],
    errors jsonb[],
    kind text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    queue text DEFAULT 'default'::text NOT NULL,
    tags character varying(255)[] DEFAULT '{}'::character varying[] NOT NULL,
    unique_key bytea,
    unique_states bit(8),
    CONSTRAINT river_job_pkey PRIMARY KEY (id),
    CONSTRAINT finalized_or_finalized_at_null CHECK ((((finalized_at IS NULL) AND (state <> ALL (ARRAY['cancelled'::river_job_state, 'completed'::river_job_state, 'discarded'::river_job_state]))) OR ((finalized_at IS NOT NULL) AND (state = ANY (ARRAY['cancelled'::river_job_state, 'completed'::river_job_state, 'discarded'::river_job_state]))))),
    CONSTRAINT kind_length CHECK (((char_length(kind) > 0) AND (char_length(kind) < 128))),
    CONSTRAINT max_attempts_is_positive CHECK ((max_attempts > 0)),
    CONSTRAINT priority_in_range CHECK (((priority >= 1) AND (priority <= 4))),
    CONSTRAINT queue_length CHECK (((char_length(queue) > 0) AND (char_length(queue) < 128)))
);

CREATE INDEX river_job_args_index ON river_job USING gin (args);

CREATE INDEX river_job_kind ON river_job USING btree (kind);

CREATE INDEX river_job_metadata_index ON river_job USING gin (metadata);

CREATE INDEX river_job_prioritized_fetching_index ON river_job USING btree (state, queue, priority, scheduled_at, id);

CREATE INDEX river_job_state_and_finalized_at_index ON river_job USING btree (state, finalized_at) WHERE (finalized_at IS NOT NULL);

CREATE UNIQUE INDEX river_job_unique_idx ON river_job USING btree (unique_key) WHERE ((unique_key IS NOT NULL) AND (unique_states IS NOT NULL) AND river_job_state_in_bitmask(unique_states, state));

CREATE UNLOGGED TABLE river_leader (
    elected_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    leader_id text NOT NULL,
    name text DEFAULT 'default'::text NOT NULL,
    CONSTRAINT river_leader_pkey PRIMARY KEY (name),
    CONSTRAINT leader_id_length CHECK (((char_length(leader_id) > 0) AND (char_length(leader_id) < 128))),
    CONSTRAINT name_length CHECK ((name = 'default'::text))
);

CREATE TABLE river_queue (
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    paused_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT river_queue_pkey PRIMARY KEY (name)
);

CREATE TABLE river_notification (
    id bigserial,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    payload text NOT NULL,
    topic text NOT NULL,
    CONSTRAINT river_notification_pkey PRIMARY KEY (id),
    CONSTRAINT topic_length CHECK (((length(topic) > 0) AND (length(topic) < 128)))
);

CREATE INDEX river_notification_created_at_idx ON river_notification USING btree (created_at);

CREATE INDEX river_notification_topic_id_idx ON river_notification USING btree (topic, id);

-- ----------------------------------------------------------------------------
-- Foreign keys to tables created later
-- ----------------------------------------------------------------------------

ALTER TABLE support_tickets
    ADD CONSTRAINT support_tickets_assignee_id_fkey FOREIGN KEY (assignee_id) REFERENCES admin_users(id);

ALTER TABLE support_tickets
    ADD CONSTRAINT support_tickets_queue_id_fkey FOREIGN KEY (queue_id) REFERENCES support_queues(id);

ALTER TABLE support_messages
    ADD CONSTRAINT support_messages_author_id_fkey FOREIGN KEY (author_id) REFERENCES admin_users(id);

ALTER TABLE support_messages
    ADD CONSTRAINT support_messages_canned_response_id_fkey FOREIGN KEY (canned_response_id) REFERENCES support_canned_responses(id);

ALTER TABLE referral_attributions
    ADD CONSTRAINT referral_attributions_qualifying_order_id_fkey FOREIGN KEY (qualifying_order_id) REFERENCES orders(id);

ALTER TABLE referral_attributions
    ADD CONSTRAINT referral_attributions_reviewed_by_fkey FOREIGN KEY (reviewed_by) REFERENCES admin_users(id);

ALTER TABLE notification_deliveries
    ADD CONSTRAINT notification_deliveries_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id);

ALTER TABLE orders
    ADD CONSTRAINT orders_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id);

ALTER TABLE entitlements
    ADD CONSTRAINT entitlements_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id);

ALTER TABLE bot_checkout_sessions
    ADD CONSTRAINT bot_checkout_sessions_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id);

ALTER TABLE auto_renew_settings
    ADD CONSTRAINT auto_renew_settings_payment_method_id_fkey FOREIGN KEY (payment_method_id) REFERENCES payment_methods(id);

ALTER TABLE auto_renew_settings
    ADD CONSTRAINT auto_renew_settings_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id);

ALTER TABLE news_posts
    ADD CONSTRAINT news_posts_created_by_fkey FOREIGN KEY (created_by) REFERENCES admin_users(id);

ALTER TABLE referral_rewards
    ADD CONSTRAINT referral_rewards_reversed_by_fkey FOREIGN KEY (reversed_by) REFERENCES admin_users(id);

-- ----------------------------------------------------------------------------
-- Triggers
-- ----------------------------------------------------------------------------

CREATE FUNCTION cart_goods_kind_matches() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM carts c WHERE c.id = NEW.cart_id AND c.kind = 'goods'
  ) THEN
    RAISE EXCEPTION 'cart % is not a goods cart', NEW.cart_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION promotion_goods_scope_matches() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM promotions p
    WHERE p.id = NEW.promotion_id AND p.applies_to = 'goods'
  ) THEN
    RAISE EXCEPTION 'promotion % does not apply to goods', NEW.promotion_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END;
$$;

CREATE FUNCTION queue_support_ticket_notice() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  INSERT INTO operator_notifications (kind, dedupe_key, payload)
  VALUES ('support', 'ticket:' || NEW.id::text,
          jsonb_build_object('ticketId', NEW.id::text, 'queueId', NEW.queue_id::text))
  ON CONFLICT (kind, dedupe_key) DO NOTHING;
  RETURN NEW;
END;
$$;

CREATE TRIGGER cart_goods_kind_matches BEFORE INSERT OR UPDATE ON cart_goods FOR EACH ROW EXECUTE FUNCTION cart_goods_kind_matches();

CREATE TRIGGER promotion_goods_scope_matches BEFORE INSERT OR UPDATE ON promotion_goods FOR EACH ROW EXECUTE FUNCTION promotion_goods_scope_matches();

CREATE TRIGGER support_tickets_queue_notice AFTER INSERT ON support_tickets FOR EACH ROW EXECUTE FUNCTION queue_support_ticket_notice();

-- ----------------------------------------------------------------------------
-- Seed rows
-- ----------------------------------------------------------------------------

-- The installation's anonymous telemetry identity, generated once.
INSERT INTO telemetry_installation (singleton) VALUES (true);

-- The catch-all support queue every installation starts with. An operator can
-- rename it, add queues beside it, or move the default elsewhere, but there is
-- always one.
INSERT INTO support_queues (code, name_en, name_ru, is_default, sort_order)
VALUES ('general', 'General', 'Общие вопросы', true, 0);

-- AI features, seeded disabled so the settings screen renders the full list of
-- what an installation could enable, and the absence of a row never has to
-- mean "off" in application code.
INSERT INTO ai_features (feature) VALUES
  ('support_summary'), ('support_reply'), ('support_rewrite'), ('support_translate'),
  ('support_classify'), ('marketing_draft'), ('risk_analysis'), ('copilot'), ('mcp_tools');

-- Settings sections, empty and unversioned until an owner saves one. A settings
-- screen that 404s until somebody saves once is a screen nobody can open to
-- make the first save.
INSERT INTO installation_settings (section) VALUES
  ('branding'), ('remnawave'), ('telegram'), ('operator_group'), ('required_channels'),
  ('maintenance'), ('notifications'), ('telemetry'), ('backup'), ('security'), ('ai'), ('mcp'),
  ('theme'), ('analytics');

-- The connection guidance the bot and the customer panel start with.
INSERT INTO connect_platforms (slug, label_en, label_ru, sort_order) VALUES
  ('ios', '🍎 iPhone / iPad', '🍎 iPhone / iPad', 10),
  ('android', '🤖 Android', '🤖 Android', 20),
  ('windows', '🪟 Windows', '🪟 Windows', 30),
  ('macos', '💻 macOS', '💻 macOS', 40),
  ('linux', '🐧 Linux', '🐧 Linux', 50);

INSERT INTO connect_clients (platform_slug, name, scheme, sort_order) VALUES
  ('ios', 'Happ', 'happ://add/', 10),
  ('ios', 'v2RayTun', 'v2raytun://import/', 20),
  ('ios', 'Streisand', 'streisand://import/', 30),
  ('android', 'Happ', 'happ://add/', 10),
  ('android', 'v2RayTun', 'v2raytun://import/', 20),
  ('android', 'Hiddify', 'hiddify://import/', 30),
  ('windows', 'Hiddify', 'hiddify://import/', 10),
  ('windows', 'v2RayTun', 'v2raytun://import/', 20),
  ('macos', 'Happ', 'happ://add/', 10),
  ('macos', 'Streisand', 'streisand://import/', 20),
  ('linux', 'Hiddify', 'hiddify://import/', 10);

-- The direct Fragment provider is listed, switched off, so an operator can
-- enable it from the panel. It has no stored credential: its secrets are read
-- from the environment only.
INSERT INTO goods_providers (slug, enabled) VALUES ('fragment_direct', false);

-- River's own migration ledger: versions 1 through 7 of its `main` line.
INSERT INTO river_migration (line, version)
SELECT 'main', version FROM generate_series(1, 7) AS version;
