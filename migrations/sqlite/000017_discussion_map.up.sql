CREATE TABLE IF NOT EXISTS discussion_map (
    channel_chat_id INTEGER NOT NULL,
    channel_msg_id  INTEGER NOT NULL,
    disc_chat_id    INTEGER NOT NULL,
    disc_msg_id     INTEGER NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_chat_id, channel_msg_id)
);
CREATE INDEX IF NOT EXISTS idx_discmap_disc ON discussion_map(disc_chat_id, disc_msg_id);
