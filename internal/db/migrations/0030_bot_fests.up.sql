-- Splatfests as the Discord bot runs them: the vote post, what the bot has
-- already announced, and LAN games players submit for a mod to review.
--
-- Every bot fest is a row in npln_fests too, so entries, battles and scoring
-- are the same tables and the same code whether a game was played online or
-- over LAN. A Discord-only fest (no BCAT pack, no in-game side) is an
-- npln_fests row that is never enabled: every console-facing path skips a
-- disabled fest, so it cannot leak to a console.
CREATE TABLE bot_fests (
    fest_id      TEXT        PRIMARY KEY REFERENCES npln_fests (id) ON DELETE CASCADE,
    discord_only BOOLEAN     NOT NULL,
    title        TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    -- [{team, name, emoji, roleId}], team being Alpha/Bravo/Charlie: the
    -- protocol's names, which the vote and the battles are stored under.
    teams        JSONB       NOT NULL,
    channel_id   TEXT        NOT NULL DEFAULT '',
    message_id   TEXT        NOT NULL DEFAULT '',
    default_win_paint  BIGINT NOT NULL DEFAULT 0,
    default_loss_paint BIGINT NOT NULL DEFAULT 0,
    -- {kind: time} for each announcement the bot has made, so a restart does
    -- not post one twice.
    posted       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_by   TEXT        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bot_fests_message_idx ON bot_fests (message_id) WHERE message_id <> '';

CREATE TABLE bot_fest_submissions (
    id                TEXT        PRIMARY KEY,
    fest_id           TEXT        NOT NULL REFERENCES npln_fests (id) ON DELETE CASCADE,
    submitted_by      TEXT        NOT NULL,
    winning_team      TEXT        NOT NULL,
    losing_team       TEXT        NOT NULL,
    -- [{userId, discordUserId, team, paint}], paint null when not given.
    players           JSONB       NOT NULL,
    screenshot_url    TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'pending',
    reason            TEXT        NOT NULL DEFAULT '',
    reviewed_by       TEXT        NOT NULL DEFAULT '',
    reviewed_at       TIMESTAMPTZ,
    -- [{discordUserId, reason, at}] from players who dispute it.
    flags             JSONB       NOT NULL DEFAULT '[]'::jsonb,
    review_channel_id TEXT        NOT NULL DEFAULT '',
    review_message_id TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX bot_fest_submissions_fest_idx ON bot_fest_submissions (fest_id, created_at);
