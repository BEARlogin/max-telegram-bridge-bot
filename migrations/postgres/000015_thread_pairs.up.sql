ALTER TABLE pending ADD COLUMN thread_id BIGINT NOT NULL DEFAULT 0;

CREATE TABLE thread_pairs (
    tg_chat_id   BIGINT NOT NULL,
    tg_thread_id BIGINT NOT NULL,
    max_chat_id  BIGINT NOT NULL,
    created_at   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tg_chat_id, tg_thread_id)
);

CREATE UNIQUE INDEX idx_thread_pairs_max ON thread_pairs(max_chat_id);
CREATE INDEX idx_thread_pairs_tg ON thread_pairs(tg_chat_id);
