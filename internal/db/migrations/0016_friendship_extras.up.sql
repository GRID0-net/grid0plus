-- Per-friendship extras: what the console PATCHes onto one direction of a
-- friendship, e.g. /extras/self/isConfirmed once the "new friend" notice has
-- been seen. Stored per direction, like the favourite flag.
ALTER TABLE friendships ADD COLUMN IF NOT EXISTS extras jsonb NOT NULL DEFAULT '{}'::jsonb;
