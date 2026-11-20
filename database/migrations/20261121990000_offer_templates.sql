-- Bedolaga's promo offers: templates an operator sends to a whole segment,
-- with a balance bonus and a discount timer that starts when the customer
-- activates the offer rather than when it was sent.
CREATE TABLE offer_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code text NOT NULL UNIQUE,
  promotion_id uuid NOT NULL REFERENCES promotions (id),
  plan_id uuid REFERENCES plans (id),
  title_ru text NOT NULL,
  title_en text NOT NULL,
  terms_ru text NOT NULL DEFAULT '',
  terms_en text NOT NULL DEFAULT '',
  -- How long a sent offer can be activated.
  valid_hours integer NOT NULL DEFAULT 168,
  -- The discount timer once activated; NULL means the offer applies as soon
  -- as it is sent and lasts valid_hours.
  activation_hours integer,
  bonus_minor bigint NOT NULL DEFAULT 0,
  bonus_currency text,
  created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  archived_at timestamptz,
  CONSTRAINT offer_templates_code_shape CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  CONSTRAINT offer_templates_titles CHECK (char_length(title_ru) BETWEEN 1 AND 120 AND char_length(title_en) BETWEEN 1 AND 120),
  CONSTRAINT offer_templates_terms CHECK (char_length(terms_ru) <= 1000 AND char_length(terms_en) <= 1000),
  CONSTRAINT offer_templates_hours CHECK (valid_hours BETWEEN 1 AND 8760 AND (activation_hours IS NULL OR activation_hours BETWEEN 1 AND 8760)),
  CONSTRAINT offer_templates_bonus CHECK (bonus_minor >= 0 AND (bonus_minor = 0 OR bonus_currency ~ '^[A-Z]{3}$'))
);

ALTER TABLE personal_offers ADD COLUMN template_id uuid REFERENCES offer_templates (id);
ALTER TABLE personal_offers ADD COLUMN activation_hours integer;
ALTER TABLE personal_offers ADD COLUMN activated_at timestamptz;
ALTER TABLE personal_offers ADD COLUMN bonus_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE personal_offers ADD COLUMN bonus_currency text;
ALTER TABLE personal_offers ADD CONSTRAINT personal_offers_bonus CHECK (
  bonus_minor >= 0 AND (bonus_minor = 0 OR bonus_currency ~ '^[A-Z]{3}$')
  AND (activation_hours IS NULL OR activation_hours BETWEEN 1 AND 8760)
);

-- One bulk send of a template to a segment.
CREATE TABLE offer_sends (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  template_id uuid NOT NULL REFERENCES offer_templates (id),
  segment_id uuid NOT NULL REFERENCES audience_segments (id),
  offered integer NOT NULL DEFAULT 0,
  created_by uuid REFERENCES admin_users (id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
