-- An operator-chosen end of play. NULL keeps the standard 48 h fest.
ALTER TABLE npln_fests ADD COLUMN end_at timestamptz;
