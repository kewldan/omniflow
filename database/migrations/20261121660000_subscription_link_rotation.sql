-- When a subscription's access link was last re-issued by its customer, so an
-- operator can put a cooldown between re-issues. The bot and the web panel
-- both claim a re-issue through this column, which is what makes the cooldown
-- hold across the two surfaces rather than per surface.
ALTER TABLE subscriptions ADD COLUMN link_rotated_at timestamp with time zone;

COMMENT ON COLUMN subscriptions.link_rotated_at IS 'When the customer last re-issued this subscription''s access link. Restored to its previous value when the re-issue fails.';
