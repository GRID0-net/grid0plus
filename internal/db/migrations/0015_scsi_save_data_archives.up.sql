-- OLSC cloud save backups (phase 4). One row per per-title, per-device backup
-- attempt, mirroring storage.lp1.scsi.srv.nintendo.net's "save data archive"
-- object.
--
-- Reference: https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers
-- See internal/scsi/README.md for which fields are confirmed from that page
-- versus guessed, and what a capture would settle.
CREATE TABLE scsi_save_data_archives (
    id                              TEXT PRIMARY KEY,

    -- BAAS user id. Not a foreign key to the users table on purpose: this
    -- service reads identity off the same access token baas.go issues, the
    -- same way npln does, without importing baas's own package.
    nsa_id                          TEXT        NOT NULL,
    application_id                  TEXT        NOT NULL,
    device_id                       TEXT        NOT NULL,
    device_serial                   TEXT        NOT NULL DEFAULT '',

    data_size                       BIGINT      NOT NULL DEFAULT 0,
    -- May be empty, which Go and JSON both treat as "no description" --
    -- Nintendo's own field is nullable, but nothing observed anywhere in this
    -- project distinguishes "null" from "empty string" for a display string,
    -- so this stays a plain NOT NULL column rather than adding a second
    -- has-description flag nothing would ever read.
    description                     TEXT        NOT NULL DEFAULT '',
    series_id                       INTEGER     NOT NULL DEFAULT 0,
    datatype                        INTEGER     NOT NULL DEFAULT 0,

    -- "uploading" or "fixed" -- see internal/scsi/models.go.
    status                          TEXT        NOT NULL DEFAULT 'uploading',
    auto_backup                     BOOLEAN     NOT NULL DEFAULT FALSE,
    num_of_partitions               INTEGER     NOT NULL DEFAULT 0,
    launch_required_version         INTEGER     NOT NULL DEFAULT 0,
    previous_save_data_archive_id   TEXT        NOT NULL DEFAULT '',
    -- Base64url, whatever the console sent. SwitchNet stores this opaquely --
    -- it never has the console's own encryption key, so it cannot recompute
    -- or verify it. See internal/scsi/README.md's key-seed-package note.
    encoded_digest                  TEXT        NOT NULL DEFAULT '',

    saved_at                        TIMESTAMPTZ,
    -- NULL until finish_upload -- distinguishing "never finished" from "finished
    -- at the epoch" matters for GetSaveDataArchive filtering out incomplete
    -- uploads a restore should never see.
    finished_at                     TIMESTAMPTZ,

    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The common reads are "this device's backups for this title" and "this
-- device's backups" (device_id may be the literal string "me", resolved by
-- the caller before it reaches the store).
CREATE INDEX scsi_save_data_archives_lookup_idx
    ON scsi_save_data_archives (nsa_id, application_id, device_id);

-- One uploaded part of an archive -- Nintendo splits an archive into a
-- "meta" component and one or more "save" components so a large save can be
-- uploaded/resumed in pieces. SwitchNet does not need to resume mid-file
-- (see internal/scsi/README.md), but keeps the same one-row-per-component
-- shape so GetSaveDataArchive's component_files list matches the documented
-- response shape without a second mapping layer.
CREATE TABLE scsi_component_files (
    id                       TEXT PRIMARY KEY,
    archive_id               TEXT        NOT NULL REFERENCES scsi_save_data_archives(id) ON DELETE CASCADE,
    index                    INTEGER     NOT NULL,
    -- "meta" or "save".
    datatype                 TEXT        NOT NULL,
    status                   TEXT        NOT NULL DEFAULT 'uploading',
    archive_size             BIGINT      NOT NULL DEFAULT 0,
    encoded_archive_digest   TEXT        NOT NULL DEFAULT '',
    -- The key this component's raw bytes are stored under in the configured
    -- blob store (internal/scsi/storage.go) -- never exposed to the console.
    blob_key                 TEXT        NOT NULL,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX scsi_component_files_archive_idx ON scsi_component_files (archive_id, index);
