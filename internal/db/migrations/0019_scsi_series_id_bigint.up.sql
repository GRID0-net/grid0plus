-- series_id was created as INTEGER (0015), but a real Splatoon 3 console
-- sends a genuine 64-bit value in start_upload's series_id field (e.g.
-- 1463435653829973917, 4502276665849307917), both far outside INTEGER's
-- range. Every start_upload from a real console failed with a Postgres
-- encoding error before this widening.
ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id TYPE BIGINT;
