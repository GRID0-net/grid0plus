-- Presence: what a user is doing right now.
--
-- Two timestamps matter, and conflating them is the bug that makes a friends
-- list look permanently offline:
--
--   reported_at  when the console last said something about its state. A
--                console reports PLAYING when a game starts and ONLINE when it
--                goes back to the Home Menu, but it does not re-report on a
--                timer, so this can be minutes or hours old while the console
--                is still perfectly awake.
--
--                NULL means the console has never reported anything. That is
--                not the same as "reported OFFLINE": a fresh OFFLINE report is
--                a console signing out and is honoured as-is, while a row that
--                was never reported falls through to the liveness rule below.
--                Defaulting this to now() conflates the two, and the visible
--                effect is that a console which is plainly online reads as
--                offline to all its friends.
--
--   last_seen_at when the console last made ANY authenticated request. Polling
--                a friend list is proof of life. Without this column a console
--                idling in the Home Menu reads as offline to all its friends,
--                which is exactly where players look at each other's status.
--
-- The read path takes the strongest fresh signal: a fresh reported state wins,
-- otherwise a fresh last_seen_at means ONLINE, otherwise OFFLINE. A weak signal
-- never downgrades a strong one.
CREATE TABLE presence (
    user_id      CHAR(16) PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,

    -- OFFLINE, INACTIVE, ONLINE or PLAYING.
    state        TEXT        NOT NULL DEFAULT 'OFFLINE',

    -- Denormalised out of extras so the dashboard and the "who is playing what"
    -- query do not have to walk JSON.
    app_id             CHAR(16),
    presence_group_id  CHAR(16),

    -- appField is a JSON-encoded string chosen by the game. It is what carries
    -- in-game detail such as the mode a player is in, so it must be stored and
    -- returned verbatim rather than parsed.
    app_field    TEXT        NOT NULL DEFAULT '',
    acd_index    INTEGER     NOT NULL DEFAULT 0,

    -- The five visibility scopes, stored as the console sent them.
    extras       JSONB       NOT NULL DEFAULT '{}'::jsonb,

    reported_at  TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    logout_at    TIMESTAMPTZ,

    CONSTRAINT presence_state CHECK (state IN ('OFFLINE', 'INACTIVE', 'ONLINE', 'PLAYING'))
);

CREATE INDEX presence_app_idx ON presence (app_id) WHERE app_id IS NOT NULL;
CREATE INDEX presence_last_seen_idx ON presence (last_seen_at DESC);
