CREATE TABLE npln_fest_battles (
    fest_id TEXT NOT NULL REFERENCES npln_fests (id) ON DELETE CASCADE,
    battle_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    winning_team TEXT NOT NULL,
    ink JSONB NOT NULL DEFAULT '{}'::jsonb,
    finished_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (fest_id, battle_id)
);

CREATE INDEX npln_fest_battles_finished_idx ON npln_fest_battles (fest_id, finished_at);
