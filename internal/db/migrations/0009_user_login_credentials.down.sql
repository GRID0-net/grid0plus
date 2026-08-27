DROP INDEX users_login_username_idx;
ALTER TABLE users
    DROP COLUMN login_username,
    DROP COLUMN login_password_hash;
