-- Blocks are one-directional: A blocking B says nothing about B's list.
--
-- A block suppresses friend requests in both directions and hides presence, but
-- only the blocker sees the block.
CREATE TABLE blocks (
    user_id         CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    blocked_user_id CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, blocked_user_id),
    CONSTRAINT blocks_not_self CHECK (user_id <> blocked_user_id)
);

CREATE INDEX blocks_blocked_idx ON blocks (blocked_user_id);
