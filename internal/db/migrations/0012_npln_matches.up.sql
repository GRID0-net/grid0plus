-- Formed Splatoon 3 matches, for the dashboard and for the record.
--
-- The QUEUE is not here and must not be: a ticket is worth nothing across a
-- restart, because the console that owned it has already given up and made a
-- new one. Persisting tickets would mean matching players against consoles that
-- stopped waiting minutes ago -- see internal/matchmaking. A formed match is a
-- different thing: it happened, and "what did we play and when" is the only
-- history this server keeps.
CREATE TABLE npln_matches (
    -- The session id the players were told, so a line in this table can be
    -- matched against a line in the log.
    id         TEXT        PRIMARY KEY,

    -- The mode, as the game named it in the ticket's matchmaking_config.
    -- Unconstrained: nothing public documents those strings, so what is valid
    -- depends on the game rather than on anything this schema can know.
    mode       TEXT        NOT NULL,

    -- The players, in the order they were matched -- which is queue order, so
    -- the first is also the host.
    --
    -- An array rather than a join table. There is no query that asks "which
    -- matches was this player in" often enough to earn one, the list is at most
    -- eight entries, and a join table would need a foreign key to users that
    -- would delete history when an account is removed. History that disappears
    -- when somebody leaves is not history.
    players    TEXT[]      NOT NULL,

    -- Whose console the others connect to. Gameplay is peer-to-peer, so this is
    -- all the server ever knew about the match itself.
    host       TEXT        NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The only read is "the most recent matches", newest first.
CREATE INDEX npln_matches_created_at_idx ON npln_matches (created_at DESC);
