-- The Stars flow asks for an amount the preset buttons do not cover, which is
-- a step where the customer types a number. The shop's promo-code step was
-- also missing from this list, so typing a code there was refused by the
-- database rather than reaching the flow that handles it.
ALTER TABLE bot_sessions
  DROP CONSTRAINT bot_sessions_state_check;

ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_state_check CHECK (
    state IN ('support_message', 'support_reply', 'promo_code',
              'topup_amount', 'subscription_label',
              'gift_recipient', 'gift_message', 'gift_claim', 'goods_recipient',
              'goods_promo', 'goods_stars_amount')
  );
