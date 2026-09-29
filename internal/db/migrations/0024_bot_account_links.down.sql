ALTER TABLE users ADD COLUMN discord_user_id TEXT;

-- Only a Discord account's main account can go back in the column, since that
-- is all it held. An extra account loses its owner here; re-linking it is a
-- /link away.
UPDATE users u SET discord_user_id = l.discord_user_id
  FROM bot_account_links l
 WHERE l.user_id = u.id AND l.primary_account;

CREATE UNIQUE INDEX users_discord_user_id_idx ON users (discord_user_id)
    WHERE discord_user_id IS NOT NULL;

DROP TABLE bot_account_links;
