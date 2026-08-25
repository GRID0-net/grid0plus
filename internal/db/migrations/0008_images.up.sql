-- Uploaded profile pictures.
--
-- The bytes live on disk under baas.image_dir, not in Postgres: they are served
-- straight to consoles by a file handler, and a 256x256 JPEG in a row is a row
-- that has to be read into memory to answer a request for a URL.
CREATE TABLE images (
    id           CHAR(32) PRIMARY KEY,
    owner_id     CHAR(16)    NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    format       TEXT        NOT NULL DEFAULT 'jpg',
    width        INTEGER     NOT NULL,
    height       INTEGER     NOT NULL,
    size_bytes   INTEGER     NOT NULL,

    -- Path relative to baas.image_dir. Never an absolute path and never taken
    -- from the request: it is built from the image id.
    rel_path     TEXT        NOT NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX images_owner_idx ON images (owner_id);
