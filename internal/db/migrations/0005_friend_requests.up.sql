-- Friend requests.
--
-- A request keeps its row after it is resolved: the console reads its outbox to
-- show "request sent", and an operator debugging a friendship needs to see that
-- a request was denied rather than never delivered.
CREATE TABLE friend_requests (
    id           UUID PRIMARY KEY,
    from_user_id CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    to_user_id   CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- PENDING, ACCEPTED, DENIED or CANCELLED.
    state        TEXT        NOT NULL DEFAULT 'PENDING',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A request to yourself is refused. With a broken identity resolution
    -- upstream, "add a friend" becomes a self-request surprisingly often, and
    -- the resulting graph is nonsense that is hard to read back.
    CONSTRAINT friend_requests_not_self CHECK (from_user_id <> to_user_id),
    CONSTRAINT friend_requests_state CHECK (state IN ('PENDING', 'ACCEPTED', 'DENIED', 'CANCELLED'))
);

-- At most one request in flight per ordered pair. A console that retries a
-- request it thinks was lost then updates the existing one instead of filling
-- the inbox with duplicates.
CREATE UNIQUE INDEX friend_requests_pending_idx
    ON friend_requests (from_user_id, to_user_id)
    WHERE state = 'PENDING';

CREATE INDEX friend_requests_inbox_idx ON friend_requests (to_user_id, state);
CREATE INDEX friend_requests_outbox_idx ON friend_requests (from_user_id, state);
