-- Which team each player picked for a Splatfest, and where they picked it.
--
-- The vote is the ONE piece of fest data SwitchNet genuinely measures. The
-- battle ratios a real fest result also carries need battle records, which
-- nothing here writes yet -- so internal/toyohr reports those as not valid
-- (the protocol's own way of saying "not available") and decides the fest on
-- the vote. See internal/toyohr/README.md for why that is an editorial choice
-- rather than a stand-in.
CREATE TABLE npln_fest_entries (
    -- The fest, uppercase. Cascading: deleting a fest from the dashboard should
    -- not leave votes behind for a fest that no longer exists, which would then
    -- reappear if the operator recreated the same id.
    fest_id    TEXT        NOT NULL REFERENCES npln_fests (id) ON DELETE CASCADE,

    -- The player. From their authenticated session, never from anything the
    -- client said about itself.
    user_id    TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- The team, as the game names it. Not constrained to a list: the team names
    -- live inside the BCAT pack, so what is valid depends on the pack rather
    -- than on anything this schema can know.
    team       TEXT        NOT NULL,

    -- The region the player entered from, uppercase.
    region     TEXT        NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One entry per player per fest. Changing team is an update, not a second
    -- row: two rows for one player would count them twice in the vote.
    PRIMARY KEY (fest_id, user_id)
);

-- The common read is "every entry for this fest", to tally the vote.
CREATE INDEX npln_fest_entries_fest_idx ON npln_fest_entries (fest_id);
