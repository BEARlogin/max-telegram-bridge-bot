DROP INDEX IF EXISTS idx_pairs_tg_owner;
ALTER TABLE pairs DROP COLUMN max_owner_id;
ALTER TABLE pairs DROP COLUMN tg_owner_id;
ALTER TABLE pending DROP COLUMN user_id;
