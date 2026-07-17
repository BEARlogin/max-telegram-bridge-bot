CREATE TABLE IF NOT EXISTS crosspost_seen (
    platform TEXT NOT NULL,
    chat_id  BIGINT NOT NULL,
    msg_id   TEXT NOT NULL,
    at       BIGINT NOT NULL,
    PRIMARY KEY (platform, chat_id, msg_id)
);
