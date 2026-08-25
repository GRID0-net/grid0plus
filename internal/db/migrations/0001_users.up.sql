-- Users are BAAS accounts: one per Switch profile, not one per console.
--
-- The id is what the console calls the NSA id and what appears as 'sub' in a
-- user access token and an id token. It is 16 lowercase hex digits, because
-- that is the shape the console and every game server expect.
CREATE TABLE users (
    id                    CHAR(16) PRIMARY KEY,

    nickname              TEXT        NOT NULL DEFAULT '',
    country               TEXT        NOT NULL DEFAULT '',
    -- Nintendo uses the literal string '0000-00-00' for "not set", which is not
    -- a valid date, so this is text rather than a date column.
    birthday              TEXT        NOT NULL DEFAULT '0000-00-00',

    thumbnail_url         TEXT        NOT NULL DEFAULT '',
    thumbnail2_url        TEXT        NOT NULL DEFAULT '',
    thumbnail_uploaded_at TIMESTAMPTZ,

    -- The privacy settings block, stored as sent so a field we do not yet
    -- understand survives a round trip instead of being dropped.
    permissions           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    -- The five visibility scopes of user extras: self, favoriteFriends,
    -- friends, foaf, everyone.
    extras                JSONB       NOT NULL DEFAULT '{}'::jsonb,

    deleted               BOOLEAN     NOT NULL DEFAULT FALSE,

    -- The console uses these to decide whether its cached friend and block
    -- lists are stale, so they must move whenever the corresponding list does.
    blocks_updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    friends_updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Nicknames are not unique on Nintendo's servers and are not unique here, but
-- the dashboard and the "find a friend" lookup both search on them.
CREATE INDEX users_nickname_idx ON users (lower(nickname));
