CREATE TABLE IF NOT EXISTS crosspost_seen (
    platform TEXT NOT NULL,
    chat_id  INTEGER NOT NULL,
    msg_id   TEXT NOT NULL,
    at       INTEGER NOT NULL,
    PRIMARY KEY (platform, chat_id, msg_id)
);
