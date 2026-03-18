CREATE TABLE IF NOT EXISTS send_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    direction TEXT NOT NULL,           -- "tg2max" or "max2tg"
    src_chat_id INTEGER NOT NULL,
    dst_chat_id INTEGER NOT NULL,
    src_msg_id TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    att_type TEXT NOT NULL DEFAULT '',  -- "video", "file", "audio", ""
    att_token TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    format TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    next_retry INTEGER NOT NULL DEFAULT 0
);
