-- Challenges (Splatoon 3's Event Matches) an operator scheduled. The event
-- type names one of the game's own built-in events; its rules and text live
-- in the game's data, not here.
CREATE TABLE npln_league_events (
    id            TEXT PRIMARY KEY,
    event_type_id TEXT NOT NULL,
    rule          INTEGER NOT NULL,
    stages        INTEGER[] NOT NULL,
    slots         JSONB NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
