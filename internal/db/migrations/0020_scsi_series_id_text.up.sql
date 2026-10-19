-- series_id became BIGINT in migration 0019 after INTEGER overflowed a
-- real console's value, but a second real value (12237957226414712540) exceeds int64's own maximum
-- (9223372036854775807) -- not signed 64-bit either, so BIGINT still
-- cannot hold it losslessly. Nothing in this project does arithmetic on
-- series_id; it is an opaque identifier the console sends and reads back,
-- the same treatment encoded_digest already gets. Stored as its exact
-- decimal digit string from here on, which can hold any value a console
-- sends regardless of width.
ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id TYPE TEXT USING series_id::TEXT;
ALTER TABLE scsi_save_data_archives ALTER COLUMN series_id SET DEFAULT '0';
