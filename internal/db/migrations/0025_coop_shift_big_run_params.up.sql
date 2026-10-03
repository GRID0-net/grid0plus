-- Big Run reward borders (gold, silver, bronze golden-egg high scores) and
-- the IsSpecial flag. Splatoon 3's CoopSchedule parser invalidates a Big Run
-- shift unless all three borders are positive (main+0x32a28d0), so a Big
-- Run cannot be offered without them. Empty for every other kind.
ALTER TABLE npln_coop_shifts
    ADD COLUMN reward_borders BIGINT[] NOT NULL DEFAULT '{}',
    ADD COLUMN is_special     BOOLEAN  NOT NULL DEFAULT FALSE;
