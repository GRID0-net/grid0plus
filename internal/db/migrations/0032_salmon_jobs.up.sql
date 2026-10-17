-- Salmon Run LAN jobs the Discord bot set up and mods review. The job is one
-- JSON document: only its id and status are ever queried.
CREATE TABLE salmon_jobs (
    id         TEXT PRIMARY KEY,
    status     TEXT NOT NULL,
    job        JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
