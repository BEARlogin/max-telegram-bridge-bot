CREATE TABLE pending_backup (key TEXT PRIMARY KEY, platform TEXT NOT NULL, chat_id INTEGER NOT NULL);
INSERT INTO pending_backup SELECT key, platform, chat_id FROM pending;
DROP TABLE pending;
ALTER TABLE pending_backup RENAME TO pending;
