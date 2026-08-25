-- A user's friend code.
--
-- The code is the twelve digits the console shows as SW-XXXX-XXXX-XXXX, stored
-- in the dashed form the BAAS user object uses ('7779-8151-4794').
--
-- Regenerating is rate-limited: after generating a code a user cannot generate
-- another until regenerable_at. Nintendo's window is 24 hours.
CREATE TABLE friend_codes (
    user_id        CHAR(16) PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    code           TEXT        NOT NULL UNIQUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    regenerable_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
