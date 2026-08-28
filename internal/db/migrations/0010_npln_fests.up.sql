-- Splatfests, as the operator schedules them.
--
-- Only the MECHANICS of a fest live here: when it runs, where it is published,
-- and which BCAT pack carries its content. The team names, banners and idol
-- dialogue are inside that pack, not in this table -- see internal/bcat.
CREATE TABLE npln_fests (
    -- Uppercase, "NXTD-00042"-shaped. The console matches this against the id
    -- in its own BCAT cache metadata, which is uppercase; a lowercase id is a
    -- different fest to the game and it answers BcatInvalid.
    id          TEXT PRIMARY KEY,

    -- The BCAT pack identifier. LOWERCASE: the console derives the pack's AES
    -- decryption IV from this string, so a stray capital produces a pack it
    -- cannot open, and the failure says nothing about the name.
    resource    TEXT        NOT NULL,

    -- The pack's 40-hex-character revision. Must equal the revision the pack's
    -- own metadata carries, or the console looks for content it cannot find.
    revision    TEXT        NOT NULL,

    -- When play opens. Every other phase is derived from it, and it must land
    -- on a two-hour boundary: Splatoon 3 files schedules into two-hour slots,
    -- and a fest in no slot makes the game demand its verdict instead of
    -- opening. Enforced in internal/toyohr, where the reason can be explained.
    start_at    TIMESTAMPTZ NOT NULL,

    regions     TEXT[]      NOT NULL,

    -- The AES-128 keys that decrypt this fest's BCAT packs, hex-encoded, in the
    -- order GetFestDecryptionKey returns them: notice, start, result, then the
    -- three per-team keys. They are OUR keys, not Nintendo's -- the pack was
    -- encrypted with them -- so a fest whose keys change without its pack being
    -- rebuilt stops decrypting on the console.
    pack_keys   TEXT[]      NOT NULL DEFAULT '{}',

    -- Whether the operator has turned this fest on. A scheduled-but-disabled
    -- fest is how the dashboard holds one ready without announcing it.
    enabled     BOOLEAN     NOT NULL DEFAULT FALSE,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The common read is "which fest is live now", which scans by time.
CREATE INDEX npln_fests_start_at_idx ON npln_fests (start_at);
