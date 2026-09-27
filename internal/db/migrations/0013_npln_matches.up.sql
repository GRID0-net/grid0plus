-- A record of every game session NPLN's matchmaking queue has formed, for the
-- dashboard's match history.
--
-- The queue itself is in memory (see internal/matchmaking's package doc): a
-- ticket is worth nothing across a restart, so the live queue does not
-- persist. A FORMED match is different -- it already happened, and this table
-- is the only place that survives the process that formed it.
--
-- GRID0+ only ever knows a match was formed. Gameplay is peer-to-peer, so
-- nothing tells this server when -- or whether -- a match actually started or
-- ended; there is no end_at column because there is no signal to put in one.
CREATE TABLE npln_matches (
    -- The session id the queue assigned. Same id GetGameSession answers to,
    -- while the match is still live in the queue's own memory.
    id            TEXT        PRIMARY KEY,
    tenant        TEXT        NOT NULL,
    mode          TEXT        NOT NULL,
    host_user_id  TEXT        NOT NULL,
    player_ids    TEXT[]      NOT NULL,

    -- Whether an operator force-started this match with fewer players than
    -- the mode currently wants, via the dashboard's matchmaking controls,
    -- rather than it filling up on its own.
    forced        BOOLEAN     NOT NULL DEFAULT FALSE,

    created_at    TIMESTAMPTZ NOT NULL
);

-- The dashboard's only read is "the most recent matches".
CREATE INDEX npln_matches_created_at_idx ON npln_matches (created_at DESC);
