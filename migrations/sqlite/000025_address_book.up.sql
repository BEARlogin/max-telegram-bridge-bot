CREATE TABLE IF NOT EXISTS message_authors (
    platform        TEXT    NOT NULL,
    chat_id         INTEGER NOT NULL,
    message_id      TEXT    NOT NULL,
    source_platform TEXT    NOT NULL,
    source_chat_id  INTEGER NOT NULL,
    source_user_id  INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (platform, chat_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_message_authors_created_at
    ON message_authors(created_at);

CREATE TABLE IF NOT EXISTS user_aliases (
    platform   TEXT    NOT NULL,
    chat_id    INTEGER NOT NULL,
    user_id    INTEGER NOT NULL,
    alias      TEXT    NOT NULL,
    updated_by INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (platform, chat_id, user_id)
);
