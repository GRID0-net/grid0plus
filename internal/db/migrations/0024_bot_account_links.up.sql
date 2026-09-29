-- One Discord account can own several GRID0+ accounts: a person may run more
-- than one login on the same consoles, and /reset-password has to be able to
-- reach all of them -- and only those. The link used to be a column on users,
-- which made that impossible, because "one account, one Discord user" was
-- enforced by a unique index. It moves to its own table, which can say both
-- things at once: this account belongs to that Discord account, and of
-- everything that Discord account owns, this one is the one /whoami and a
-- bare /reset-password act on.
CREATE TABLE bot_account_links (
    user_id         TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    discord_user_id TEXT NOT NULL,
    -- The account a person reaches without naming one. At most one per
    -- Discord account, which is what the partial index below holds; the rest
    -- are the extras they name by login.
    primary_account BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE UNIQUE INDEX bot_account_links_primary_idx
    ON bot_account_links (discord_user_id) WHERE primary_account;

-- The partial index only covers main accounts, so listing everything a
-- Discord account owns needs its own.
CREATE INDEX bot_account_links_discord_idx ON bot_account_links (discord_user_id);

-- Every link that exists, and all of them are main ones: until now there could
-- only ever be one.
INSERT INTO bot_account_links (user_id, discord_user_id, primary_account)
SELECT id, discord_user_id, TRUE FROM users WHERE discord_user_id IS NOT NULL;

ALTER TABLE users DROP COLUMN discord_user_id;
