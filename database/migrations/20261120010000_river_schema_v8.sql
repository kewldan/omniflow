-- River v0.48 added version 8 of its `main` migration line. On PostgreSQL it
-- is a no-op (sequences already never reuse job IDs), so the only change is the
-- ledger row that tells River's validator the schema is current.
INSERT INTO river_migration (line, version) VALUES ('main', 8);
