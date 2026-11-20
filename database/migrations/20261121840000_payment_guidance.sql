-- What a customer is told about paying outside a provider's checkout: the
-- operator's own transfer instructions for a manual payment, and whether the
-- wallet offers to top up through support (Bedolaga's SUPPORT_TOPUP_ENABLED).
ALTER TABLE commerce_settings
  ADD COLUMN manual_instructions_ru text NOT NULL DEFAULT '',
  ADD COLUMN manual_instructions_en text NOT NULL DEFAULT '',
  ADD COLUMN support_topup_enabled boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT commerce_settings_manual_instructions_length
    CHECK (char_length(manual_instructions_ru) <= 2000 AND char_length(manual_instructions_en) <= 2000);
