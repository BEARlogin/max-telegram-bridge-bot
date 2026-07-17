-- SQLite doesn't support DROP COLUMN before 3.35.0, so recreate tables

CREATE TABLE pairs_backup (
    tg_chat_id  INTEGER NOT NULL,
    max_chat_id INTEGER NOT NULL,
    PRIMARY KEY (tg_chat_id, max_chat_id)
);
INSERT INTO pairs_backup SELECT tg_chat_id, max_chat_id FROM pairs;
DROP TABLE pairs;
ALTER TABLE pairs_backup RENAME TO pairs;
CREATE INDEX IF NOT EXISTS idx_pairs_tg ON pairs(tg_chat_id);
CREATE INDEX IF NOT EXISTS idx_pairs_max ON pairs(max_chat_id);

CREATE TABLE messages_backup (
    tg_chat_id  INTEGER NOT NULL,
    tg_msg_id   INTEGER NOT NULL,
    max_chat_id INTEGER NOT NULL,
    max_msg_id  TEXT NOT NULL,
    PRIMARY KEY (tg_chat_id, tg_msg_id)
);
INSERT INTO messages_backup SELECT tg_chat_id, tg_msg_id, max_chat_id, max_msg_id FROM messages;
DROP TABLE messages;
ALTER TABLE messages_backup RENAME TO messages;
CREATE INDEX IF NOT EXISTS idx_messages_max ON messages(max_msg_id);
