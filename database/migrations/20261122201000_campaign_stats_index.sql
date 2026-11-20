-- The campaign report groups a campaign's attributions by day; the existing
-- index on campaign_id alone made it read every attribution row to sort them.
CREATE INDEX customer_campaign_attributions_campaign_day
  ON customer_campaign_attributions (campaign_id, attributed_at);
