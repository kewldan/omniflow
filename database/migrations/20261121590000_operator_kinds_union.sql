-- Two changes widened the operator kinds independently: the operator streams
-- (trial, addon, promo, partner, error, infrastructure, report) and Remnawave
-- sync (node, monitoring). Whichever applied last replaced the other's list,
-- so the constraints now name every kind the code sends.
ALTER TABLE operator_notifications DROP CONSTRAINT operator_notifications_kind_known;
ALTER TABLE operator_notifications ADD CONSTRAINT operator_notifications_kind_known CHECK (
  kind = ANY (ARRAY[
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup',
    'security', 'anomaly', 'support',
    'trial', 'addon', 'promo', 'partner', 'error', 'infrastructure', 'report',
    'node', 'monitoring'
  ]::text[])
);
ALTER TABLE operator_topics DROP CONSTRAINT operator_topics_kind_known;
ALTER TABLE operator_topics ADD CONSTRAINT operator_topics_kind_known CHECK (
  kind = ANY (ARRAY[
    'purchase', 'renewal', 'topup', 'refund', 'fulfillment_failure', 'incident', 'backup',
    'security', 'anomaly', 'campaign_test', 'notice_test', 'support',
    'trial', 'addon', 'promo', 'partner', 'error', 'infrastructure', 'report',
    'node', 'monitoring'
  ]::text[])
);
