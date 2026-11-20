-- The bot asks a newcomer for an invite code typed by hand, when the
-- Marketing settings switch that on, and waits for it in this state.
ALTER TABLE bot_sessions DROP CONSTRAINT bot_sessions_state_check;
ALTER TABLE bot_sessions ADD CONSTRAINT bot_sessions_state_check CHECK (state IN (
  'support_message', 'support_reply', 'promo_code', 'topup_amount', 'subscription_label',
  'gift_recipient', 'gift_message', 'gift_claim', 'goods_recipient', 'goods_promo',
  'goods_stars_amount', 'referral_code'
));
