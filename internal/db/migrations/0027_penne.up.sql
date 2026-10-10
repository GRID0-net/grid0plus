-- Push notifications to consoles, the way BAAS and Penne link them: npns asks
-- Penne for a notification token per local account (penne_tokens), the
-- account sysmodule registers that token with BAAS as a push channel for the
-- signed-in user (push_channels), and the console's npns holds a frontline
-- connection authenticated by a login ticket (penne_tickets). A push to a
-- user walks user -> push channel -> token -> penne id -> frontline.
CREATE TABLE penne_tokens (
    token          TEXT PRIMARY KEY,
    penne_id       TEXT NOT NULL,
    -- The console's local account Uid, 32 hex digits, as npns names it in
    -- /v1/accounts/<account>/notification_tokens.
    account        TEXT NOT NULL,
    application_id TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE push_channels (
    user_id           CHAR(16) NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_account_id TEXT NOT NULL,
    device_token      TEXT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_account_id)
);

CREATE TABLE penne_tickets (
    ticket     TEXT PRIMARY KEY,
    penne_id   TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX penne_tickets_expires_idx ON penne_tickets (expires_at);
