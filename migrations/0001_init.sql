-- 0001_init.sql — initial schema for the chat server.
-- Holds every accepted message durably so history survives restarts (FR-8) and
-- new joiners can be served the last N messages of a room (FR-7).

CREATE TABLE IF NOT EXISTS messages (
    id          BIGSERIAL   PRIMARY KEY,
    room        TEXT        NOT NULL,
    sender_id   TEXT        NOT NULL,
    sender_name TEXT        NOT NULL,
    content     TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- RecentByRoom fetches the newest messages for a room ordered by time; this index
-- makes that lookup an index scan rather than a full-table sort.
CREATE INDEX IF NOT EXISTS idx_messages_room_created_at
    ON messages (room, created_at DESC);
