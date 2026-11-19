-- Advertising campaigns addressed by a Telegram /start parameter, as Bedolaga
-- runs them: a link t.me/<bot>?start=c_<code> (or the bare code) attributes a
-- new customer to the campaign and may grant a sign-up bonus. Reporting reads
-- registrations, trials, paying customers, and revenue per campaign.
CREATE TABLE ad_campaigns (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL,
  name text NOT NULL,
  bonus_kind text NOT NULL DEFAULT 'none',
  bonus_value bigint NOT NULL DEFAULT 0,
  currency text NOT NULL DEFAULT 'RUB',
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ad_campaigns_code_key UNIQUE (code),
  CONSTRAINT ad_campaigns_code_shape CHECK (code ~ '^[a-z0-9_]{2,40}$'),
  CONSTRAINT ad_campaigns_name_length CHECK (char_length(name) BETWEEN 1 AND 120),
  CONSTRAINT ad_campaigns_bonus_known CHECK (bonus_kind IN ('none', 'wallet', 'days')),
  CONSTRAINT ad_campaigns_bonus_value CHECK (
    (bonus_kind = 'none' AND bonus_value = 0) OR (bonus_kind = 'wallet' AND bonus_value > 0)
    OR (bonus_kind = 'days' AND bonus_value BETWEEN 1 AND 365)
  ),
  CONSTRAINT ad_campaigns_currency_shape CHECK (currency ~ '^[A-Z]{3}$')
);

-- One campaign per customer, first touch: the campaign that brought them.
CREATE TABLE customer_campaign_attributions (
  user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  campaign_id uuid NOT NULL REFERENCES ad_campaigns (id),
  attributed_at timestamptz NOT NULL DEFAULT now(),
  bonus_granted_at timestamptz
);
CREATE INDEX customer_campaign_attributions_campaign ON customer_campaign_attributions (campaign_id);
