-- NPLN Ugcstore documents (internal/ugcstore). Splatoon 3 reads and writes
-- its own per-player records here right after signing in -- the GameRecord
-- VsUserAttribute/CoopUserAttribute documents among them -- and reads back
-- what it wrote, so writes have to outlive a restart: a document the game
-- believes it created and then cannot find crashes it.
--
-- name is the canonical resource name ("tenants/<tenant>/documents/<rest>").
-- fields is the document's nn.npln.common.MapValue, protobuf-encoded, so
-- value types (int vs double, timestamps, nested maps) survive exactly.
-- owner is the NPLN user who created it; only they may change it.
CREATE TABLE IF NOT EXISTS ugc_documents (
    name        text        PRIMARY KEY,
    owner       text        NOT NULL,
    fields      bytea       NOT NULL,
    create_time timestamptz NOT NULL,
    update_time timestamptz NOT NULL
);
