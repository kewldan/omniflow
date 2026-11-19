-- A plan the operator marks as the best deal, shown with a badge in the bot
-- and on the web (Bedolaga's "выгодно" highlight).
ALTER TABLE plans ADD COLUMN highlighted boolean NOT NULL DEFAULT false;
