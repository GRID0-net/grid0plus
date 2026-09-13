-- Salmon Run rotation shifts (Normal and Big Run), as the operator schedules
-- them. See internal/toyohr's coop_schedule.go and coop_ids.go: the wire
-- shape (nn.npln.toyohr.v1.CoopSchedule, with parallel Normal/BigRun/
-- TeamContest sub-messages) is already known from that proto's own decompiled
-- descriptor, unlike a Vs rotation -- what was missing was an authoring path
-- and a real stage/weapon/boss id table, both added alongside this table.
CREATE TABLE npln_coop_shifts (
    -- Derived, not typed: "<kind>-<start unix seconds>" (toyohr.CoopShift.ID),
    -- the same "never typed twice" reasoning as npln_fests.id vs. NewFestID.
    id              TEXT PRIMARY KEY,

    -- "normal" or "big_run". Determines which of stage_id/boss's real-table
    -- membership is checked (toyohr.CoopShift.Validate) and which sub-message
    -- the shift is served as.
    kind            TEXT        NOT NULL,

    -- Real Nintendo stage id, from internal/toyohr's CoopStages table --
    -- extracted from the game's own CoopSceneInfo parameter table, not
    -- invented. Must be a Normal-flagged stage for a "normal" shift and a
    -- BigRun-flagged one for a "big_run" shift.
    stage_id        INTEGER     NOT NULL,

    -- The king salmonid's codename for this shift's Xtrawave (e.g.
    -- "SakeJaw"), or empty if none is scheduled. From CoopEnemyInfo's
    -- Category=Boss rows.
    boss            TEXT        NOT NULL DEFAULT '',

    -- Exactly four main weapon ids, from CoopWeapons -- every real rotation
    -- shows exactly four, enforced in Validate rather than here so the reason
    -- can be explained.
    weapons         BIGINT[]    NOT NULL,

    -- The Grizzco weapon this shift's boss wave hands out, from
    -- CoopKumaWeapons.
    kuma_weapon_id  BIGINT      NOT NULL,

    -- 0 means no gear reward this shift. Not validated against a real gear
    -- id table -- none was pulled into this pass, see coop_ids.go's comment.
    reward_gear_id  INTEGER     NOT NULL DEFAULT 0,

    -- Real-valued int64 reward tiers for a Big Run's scale-based rewards
    -- (CoopSchedule_BigRun.Rewards); empty for a "normal" shift. Exact
    -- meaning of the values is unconfirmed -- see docs/QUESTIONS.md.
    rewards         BIGINT[]    NOT NULL DEFAULT '{}',

    -- Must land on a two-hour slot boundary, the same rule fest.go's
    -- comment says Splatoon 3 applies to every schedule, not just fests.
    start_at        TIMESTAMPTZ NOT NULL,
    end_at          TIMESTAMPTZ NOT NULL,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The common read is "which shifts are live or upcoming", which scans by time.
CREATE INDEX npln_coop_shifts_start_at_idx ON npln_coop_shifts (start_at);
