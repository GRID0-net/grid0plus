-- Refresh tokens from the Nintendo Account link stub (internal/baas/nalink.go).
-- They used to live in the baas process's memory, so every restart or deploy
-- invalidated them and logged every console out of its Nintendo Account (the
-- next refresh answered 400 invalid_grant). Only a SHA-256 of the token is
-- stored: the token is a bearer credential and a database dump should not be
-- one.
CREATE TABLE IF NOT EXISTS na_refresh_tokens (
    token_sha256 text        PRIMARY KEY,
    user_id      char(16)    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope        jsonb       NOT NULL DEFAULT '[]'::jsonb,
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS na_refresh_tokens_expires_idx ON na_refresh_tokens (expires_at);
