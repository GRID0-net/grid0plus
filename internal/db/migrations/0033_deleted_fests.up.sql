-- Fests an operator deleted. A Splatoon 3 save keeps the id of the last fest
-- it joined, and a save left pointing at a fest that no longer exists sits
-- waiting for that fest's results forever; toyohr resets those saves.
CREATE TABLE npln_deleted_fests (
    id         TEXT PRIMARY KEY,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
