-- Friendships are mutual, and stored as two rows — one per direction.
--
-- The alternative, a single row with an ordered pair, is smaller but wrong:
-- 'favourite' is a per-direction property. A may mark B as a favourite without
-- B doing the same, and the console's friend list shows each side its own flag.
--
-- A row is only ever written together with its mirror, inside one transaction,
-- so the two directions cannot drift apart.
CREATE TABLE friendships (
    user_id          CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    friend_id        CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    favorite         BOOLEAN     NOT NULL DEFAULT FALSE,
    became_friend_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (user_id, friend_id),
    -- Nobody is their own friend. Without this, a self-friendship is possible,
    -- and a friend list containing yourself is a confusing way to discover that
    -- identity resolution went wrong somewhere upstream.
    CONSTRAINT friendships_not_self CHECK (user_id <> friend_id)
);

CREATE INDEX friendships_friend_idx ON friendships (friend_id);
