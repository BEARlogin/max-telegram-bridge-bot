CREATE TABLE IF NOT EXISTS pinned_tg_messages (
    tg_chat_id BIGINT NOT NULL,
    tg_msg_id  BIGINT NOT NULL,
    media_group_id TEXT NOT NULL DEFAULT '',
    pinned_at  BIGINT NOT NULL,
    PRIMARY KEY (tg_chat_id, tg_msg_id)
);

CREATE INDEX IF NOT EXISTS idx_pinned_tg_messages_pinned_at
    ON pinned_tg_messages(pinned_at);
