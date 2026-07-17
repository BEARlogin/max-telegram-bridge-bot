CREATE TABLE IF NOT EXISTS users (
    user_id BIGINT PRIMARY KEY,
    platform TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    first_name TEXT NOT NULL DEFAULT '',
    first_seen BIGINT NOT NULL DEFAULT 0,
    last_seen BIGINT NOT NULL DEFAULT 0
);
