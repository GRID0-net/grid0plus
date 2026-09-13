-- Lets an operator invalidate a device account's live sessions without
-- rotating the whole server's signing key.
--
-- A BAAS access/user/id token stays cryptographically valid until it expires,
-- with no per-token record to consult -- there was previously no way to
-- react to a leaked-but-unexpired token short of rotating the key that signs
-- every token for every console at once. Comparing a presented token's 'iat'
-- against this single per-device-account cutoff, instead, revokes exactly
-- the sessions minted before the operator noticed something was wrong,
-- without touching anyone else's.
ALTER TABLE device_accounts
    ADD COLUMN tokens_revoked_before TIMESTAMPTZ;
