DROP INDEX IF EXISTS users_discord_user_id_idx;
ALTER TABLE users
    DROP COLUMN IF EXISTS discord_user_id,
    DROP COLUMN IF EXISTS provisional;
