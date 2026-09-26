-- State the NEX game servers (cmd/nex: Splatoon 2) keep between sessions:
-- a player's NEX unique ids, Splatfest votes and scores, league and X Power
-- results, uploaded match results, plaza posts.
--
-- One table rather than one per kind, on purpose. Every kind is small, is
-- read back by the title that wrote it and nothing else, and is only ever
-- looked up by (title, kind, key) or listed by (title, kind[, owner]).
-- Rankings are computed from the list in Go: this is a server for a
-- private group of consoles, not Nintendo's leaderboard.
--
-- owner is the SwitchNet user id the record belongs to ('' for none). Not a
-- foreign key: a record outliving its user (a deleted account's old fest
-- vote) is harmless, and cascading would make deleting a user rewrite
-- someone else's ranking.
-- value is the title's own encoding (JSON); the store does not look inside.
CREATE TABLE IF NOT EXISTS nex_records (
    title      text        NOT NULL,
    kind       text        NOT NULL,
    key        text        NOT NULL,
    owner      text        NOT NULL DEFAULT '',
    value      bytea       NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (title, kind, key)
);
CREATE INDEX IF NOT EXISTS nex_records_by_owner ON nex_records (title, kind, owner);
