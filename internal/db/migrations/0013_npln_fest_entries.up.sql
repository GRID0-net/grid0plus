-- One account's declared team for a fest -- what CreateFestEntry records.
--
-- A team choice is permanent once made: Splatoon 3's own Splatfest rules do
-- not let a player switch teams after picking, which is why this has no
-- update path and CreateFestEntry conflicts on a second attempt rather than
-- overwriting the first. No foreign keys to npln_fests or users, matching
-- npln_matches' host_user_id/player_ids -- a user row is soft-deleted
-- (users.deleted), never removed, so there is no dangling-reference case a
-- hard constraint would catch that the application does not already handle.
CREATE TABLE npln_fest_entries (
    fest_id     TEXT        NOT NULL,
    user_id     TEXT        NOT NULL,
    fest_team   TEXT        NOT NULL,
    fest_region TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (fest_id, user_id)
);

-- GetFestResult's tally reads every entry for one fest.
CREATE INDEX npln_fest_entries_fest_id_idx ON npln_fest_entries (fest_id);
