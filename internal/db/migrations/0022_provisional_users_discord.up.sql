-- A console's first account request (POST /1.0.0/users) has to create a user
-- before anyone has said who the person is. That user is provisional: once
-- the console links to a real person's account, its device account moves
-- there and the provisional user is deleted. Until then it is a real,
-- working account (tokens, profile, friends all use its id), just not a
-- person, so it is counted apart.
ALTER TABLE users
    ADD COLUMN provisional     BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN discord_user_id TEXT;

CREATE UNIQUE INDEX users_discord_user_id_idx ON users (discord_user_id)
    WHERE discord_user_id IS NOT NULL;

-- Every console-created user so far: no login credentials of its own.
UPDATE users SET provisional = TRUE WHERE login_username IS NULL;

-- The throwaway users linking already left behind: provisional, and no
-- device account (live or deleted) points at them any more.
DELETE FROM users u
WHERE u.provisional
  AND NOT EXISTS (SELECT 1 FROM device_accounts d WHERE d.user_id = u.id);
