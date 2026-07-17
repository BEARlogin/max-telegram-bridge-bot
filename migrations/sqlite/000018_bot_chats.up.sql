CREATE TABLE IF NOT EXISTS bot_chats (
    platform   TEXT NOT NULL,
    chat_id    INTEGER NOT NULL,
    title      TEXT NOT NULL DEFAULT '',
    chat_type  TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (platform, chat_id)
);
