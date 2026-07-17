CREATE TABLE IF NOT EXISTS crossposts (
    tg_chat_id  INTEGER NOT NULL,
    max_chat_id INTEGER NOT NULL,
    direction   TEXT NOT NULL DEFAULT 'both',
    created_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tg_chat_id, max_chat_id)
);

CREATE INDEX IF NOT EXISTS idx_crossposts_tg ON crossposts(tg_chat_id);
CREATE INDEX IF NOT EXISTS idx_crossposts_max ON crossposts(max_chat_id);

ALTER TABLE pending ADD COLUMN command TEXT NOT NULL DEFAULT 'bridge';
