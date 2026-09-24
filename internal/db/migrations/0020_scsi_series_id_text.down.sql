ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id DROP DEFAULT;
ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id TYPE BIGINT USING series_id::BIGINT;
ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id SET DEFAULT 0;
