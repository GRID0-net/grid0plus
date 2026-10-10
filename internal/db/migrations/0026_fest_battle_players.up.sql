-- One row per player per Splatfest battle, from that player's own referee
-- report. Each console reports only its own result, so a battle's ink is the
-- sum of these rows; the battle row alone held only the first winner's.
CREATE TABLE npln_fest_battle_players (
    fest_id TEXT NOT NULL REFERENCES npln_fests (id) ON DELETE CASCADE,
    battle_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    team TEXT NOT NULL,
    paint BIGINT NOT NULL,
    PRIMARY KEY (fest_id, battle_id, user_id)
);
