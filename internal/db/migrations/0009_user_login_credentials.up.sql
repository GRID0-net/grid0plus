-- Login credentials let a person authenticate directly, independently of any
-- device account: the self-registration website, and the Nintendo Account
-- link stub that checks a person's username/password before attaching a
-- device account to their existing user rather than minting a new one.
--
-- Both nullable: a user created by a console (registerUser) never has these
-- set, and a user registered on the website starts with no device account
-- at all.
ALTER TABLE users
    ADD COLUMN login_username      TEXT,
    ADD COLUMN login_password_hash TEXT;

-- Case-insensitive uniqueness, matching how the login handler looks users up.
CREATE UNIQUE INDEX users_login_username_idx ON users (lower(login_username))
    WHERE login_username IS NOT NULL;
