-- Ugcstore attachments: an upload's bytes, and the one document it is
-- attached to once a commit links it. Unlinked uploads are purged.
CREATE TABLE ugc_blobs (
    id          text PRIMARY KEY,
    owner       text NOT NULL,
    document    text UNIQUE,
    mime_type   text NOT NULL DEFAULT '',
    total       bigint NOT NULL DEFAULT -1,
    complete    boolean NOT NULL DEFAULT false,
    data        bytea NOT NULL DEFAULT '\x',
    create_time timestamptz NOT NULL
);
CREATE INDEX ugc_blobs_unlinked ON ugc_blobs (create_time) WHERE document IS NULL;
