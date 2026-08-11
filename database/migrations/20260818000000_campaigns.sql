-- News authoring, audience segments, campaigns, and templates.
--
-- v0.4 could publish a news post and let the notifier fan it out to everybody
-- who had not read it. That is one audience, one schedule, and no way to see
-- what a message will look like before it goes. A campaign needs all three,
-- plus the ability to stop one that is already running.

-- ---------------------------------------------------------------------------
-- News authoring
-- ---------------------------------------------------------------------------

-- A post now has a lifecycle rather than a publication timestamp.
--
-- `published_at` alone cannot express "written but not ready", "scheduled for
-- Tuesday", or "taken down because it was wrong" — and the last of those is the
-- one an operator needs most urgently.
ALTER TABLE news_posts
  ADD COLUMN status text NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'scheduled', 'published', 'unpublished', 'archived')),
  ADD COLUMN scheduled_for timestamptz,
  ADD COLUMN unpublished_at timestamptz,
  ADD COLUMN archived_at timestamptz,
  ADD COLUMN created_by uuid REFERENCES admin_users(id),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),

  -- A scheduled post needs a time to go out at; a published one needs a time it
  -- went out. Neither is optional for its own state.
  ADD CONSTRAINT news_posts_schedule_present
    CHECK (status <> 'scheduled' OR scheduled_for IS NOT NULL);

-- Existing posts carry their real state rather than defaulting to draft, which
-- would silently unpublish everything an installation has already sent.
UPDATE news_posts SET status = 'published' WHERE published_at IS NOT NULL;

CREATE INDEX news_posts_scheduled_idx
  ON news_posts (scheduled_for) WHERE status = 'scheduled';

-- ---------------------------------------------------------------------------
-- Audience segments
-- ---------------------------------------------------------------------------

-- A segment is a set of explicit, reviewable filters.
--
-- It is deliberately not a saved SQL fragment. An operator must be able to read
-- what a segment selects and an auditor must be able to check it afterwards,
-- and neither is possible if the definition is a query somebody wrote once.
-- Every filter here maps to a column Omniflow already has.
CREATE TABLE audience_segments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  name_en text NOT NULL CHECK (char_length(name_en) BETWEEN 1 AND 80),
  name_ru text NOT NULL CHECK (char_length(name_ru) BETWEEN 1 AND 80),

  -- The filter set, validated on write against a closed vocabulary. Storing it
  -- as jsonb rather than columns keeps the set growable without a migration per
  -- filter, and the API refuses a key it does not recognise so an unreadable
  -- segment can never be saved.
  filters jsonb NOT NULL DEFAULT '{}'::jsonb,

  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Templates
-- ---------------------------------------------------------------------------

-- A template's variables are declared, so they can be validated before a send
-- rather than rendering as an empty string in front of a customer.
CREATE TABLE message_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  class text NOT NULL DEFAULT 'marketing' CHECK (class IN ('transactional', 'marketing')),
  subject_en text NOT NULL DEFAULT '' CHECK (char_length(subject_en) <= 200),
  subject_ru text NOT NULL DEFAULT '' CHECK (char_length(subject_ru) <= 200),
  body_en text NOT NULL CHECK (char_length(body_en) BETWEEN 1 AND 3500),
  body_ru text NOT NULL CHECK (char_length(body_ru) BETWEEN 1 AND 3500),

  -- The variables the body may use. A body referencing a variable that is not
  -- declared is refused on write: the alternative is a customer receiving
  -- "Hello {name}" because nobody checked.
  variables text[] NOT NULL DEFAULT '{}',

  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES admin_users(id)
);

-- ---------------------------------------------------------------------------
-- Campaigns
-- ---------------------------------------------------------------------------

-- A campaign is a template sent to a segment, on a schedule, that can be
-- stopped.
--
-- The states are the ones an operator actually needs. `paused` matters most: a
-- campaign that turns out to be wrong halfway through its audience has to stop
-- without cancelling what has already gone, and without losing the record of
-- who received it.
CREATE TABLE campaigns (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  template_id uuid NOT NULL REFERENCES message_templates(id),
  segment_id uuid NOT NULL REFERENCES audience_segments(id),

  status text NOT NULL DEFAULT 'draft' CHECK (
    status IN ('draft', 'scheduled', 'running', 'paused', 'completed', 'cancelled')
  ),
  scheduled_for timestamptz,

  -- The audience as estimated when the campaign was reviewed, kept beside what
  -- actually happened. Their difference is the honest measure of how much the
  -- audience moved between review and send.
  estimated_audience integer NOT NULL DEFAULT 0 CHECK (estimated_audience >= 0),
  queued_count integer NOT NULL DEFAULT 0 CHECK (queued_count >= 0),
  sent_count integer NOT NULL DEFAULT 0 CHECK (sent_count >= 0),
  failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
  suppressed_count integer NOT NULL DEFAULT 0 CHECK (suppressed_count >= 0),

  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id),
  started_at timestamptz,
  completed_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT campaigns_schedule_present
    CHECK (status <> 'scheduled' OR scheduled_for IS NOT NULL)
);

CREATE INDEX campaigns_due_idx ON campaigns (scheduled_for) WHERE status = 'scheduled';
CREATE INDEX campaigns_running_idx ON campaigns (status) WHERE status = 'running';

-- One recipient of one campaign, and what became of it.
--
-- The row exists before the message is sent, which is what makes a campaign
-- resumable and deduplicated: the primary key refuses a second attempt at the
-- same customer, so a paused-and-resumed campaign continues rather than
-- restarting.
--
-- The rendered body is deliberately not stored. The template and the variables
-- are, and the message itself lives in the delivery the bot made — copying it
-- here would duplicate customer-facing content into a second retention regime
-- for no operational gain.
CREATE TABLE campaign_recipients (
  campaign_id uuid NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  status text NOT NULL DEFAULT 'queued' CHECK (
    status IN ('queued', 'sent', 'failed', 'suppressed', 'blocked')
  ),
  -- Why a recipient was skipped. Consent, quiet hours, a frequency cap, and a
  -- suppression-list entry are different decisions and an operator reviewing a
  -- campaign's reach needs to tell them apart.
  suppression_reason text CHECK (
    suppression_reason IS NULL OR suppression_reason IN (
      'no_consent', 'suppressed', 'frequency_cap', 'quiet_hours',
      'delivery_blocked', 'no_telegram'
    )
  ),
  error_code text,
  queued_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,

  PRIMARY KEY (campaign_id, user_id)
);

CREATE INDEX campaign_recipients_pending_idx
  ON campaign_recipients (campaign_id) WHERE status = 'queued';

-- ---------------------------------------------------------------------------
-- Suppression list
-- ---------------------------------------------------------------------------

-- A customer who asked not to be contacted, or whom the operator must not
-- contact.
--
-- It is separate from the marketing consent flag because they mean different
-- things: consent is a preference the customer can toggle, and a suppression is
-- a standing instruction that survives them toggling it back on by accident.
CREATE TABLE communication_suppressions (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  reason text NOT NULL CHECK (
    reason IN ('customer_request', 'bounced', 'complaint', 'operator')
  ),
  note text CHECK (note IS NULL OR char_length(note) <= 400),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES admin_users(id)
);
