CREATE TABLE IF NOT EXISTS send_queue (
    id SERIAL PRIMARY KEY,
    direction TEXT NOT NULL,
    src_chat_id BIGINT NOT NULL,
    dst_chat_id BIGINT NOT NULL,
    src_msg_id TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    att_type TEXT NOT NULL DEFAULT '',
    att_token TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    format TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    next_retry BIGINT NOT NULL DEFAULT 0
);
