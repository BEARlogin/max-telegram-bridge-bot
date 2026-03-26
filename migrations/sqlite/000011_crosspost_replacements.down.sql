-- SQLite does not support DROP COLUMN before 3.35.0, so we recreate the table.
CREATE TABLE crossposts_backup AS SELECT tg_chat_id, max_chat_id, direction, created_at, owner_id, tg_owner_id, deleted_at, deleted_by FROM crossposts;
DROP TABLE crossposts;
CREATE TABLE crossposts (
    tg_chat_id  INTEGER NOT NULL,
    max_chat_id INTEGER NOT NULL,
    direction   TEXT NOT NULL DEFAULT 'both',
    created_at  INTEGER NOT NULL DEFAULT 0,
    owner_id    INTEGER NOT NULL DEFAULT 0,
    tg_owner_id INTEGER NOT NULL DEFAULT 0,
    deleted_at  INTEGER NOT NULL DEFAULT 0,
    deleted_by  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tg_chat_id, max_chat_id)
);
INSERT INTO crossposts SELECT * FROM crossposts_backup;
DROP TABLE crossposts_backup;
CREATE INDEX IF NOT EXISTS idx_crossposts_tg ON crossposts(tg_chat_id);
CREATE INDEX IF NOT EXISTS idx_crossposts_max ON crossposts(max_chat_id);
