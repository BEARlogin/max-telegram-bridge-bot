DROP INDEX IF EXISTS idx_thread_pairs_max;
DROP INDEX IF EXISTS idx_thread_pairs_tg;
DROP TABLE IF EXISTS thread_pairs;
ALTER TABLE pending DROP COLUMN thread_id;
