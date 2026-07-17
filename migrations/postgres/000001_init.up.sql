CREATE TABLE IF NOT EXISTS pending (
    key      TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    chat_id  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS pairs (
    tg_chat_id  BIGINT NOT NULL,
    max_chat_id BIGINT NOT NULL,
    PRIMARY KEY (tg_chat_id, max_chat_id)
);

CREATE INDEX IF NOT EXISTS idx_pairs_tg ON pairs(tg_chat_id);
CREATE INDEX IF NOT EXISTS idx_pairs_max ON pairs(max_chat_id);

CREATE TABLE IF NOT EXISTS messages (
    tg_chat_id  BIGINT NOT NULL,
    tg_msg_id   INTEGER NOT NULL,
    max_chat_id BIGINT NOT NULL,
    max_msg_id  TEXT NOT NULL,
    PRIMARY KEY (tg_chat_id, tg_msg_id)
);

CREATE INDEX IF NOT EXISTS idx_messages_max ON messages(max_msg_id);
