# splatdiag

A temporary, opt-in diagnostic that answers `CloudSave/GetSaveRecord` and two `Ugcstore/GetDocument` calls for one
chosen account from private files, to compare how the game behaves against a known-good record. It is for debugging
and is not part of normal operation.

It is off unless all four of these are set: `SWITCHNET_NPLN_DIAGNOSTIC_USER`,
`SWITCHNET_NPLN_DIAGNOSTIC_SAVE_PATH`, `SWITCHNET_NPLN_DIAGNOSTIC_VS_PATH` and
`SWITCHNET_NPLN_DIAGNOSTIC_COOP_PATH`. The paths point at raw, unframed protobuf messages held privately outside this
repository, and start-up fails if a source is missing or malformed. No Nintendo capture, key or certificate belongs in
Git.

Only the selected authenticated user receives these records. The save's name and nickname are retargeted to that user,
and so are the account references inside the save and the two attribute documents. The save still holds the source
account's progression, so this must never be used to initialise a real player. Other users and other documents get the
normal implementation, and the imported progression is never written to GRID0+'s database. Remove the four variables
and the private files when you are done.

Protocol definitions come from [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols).
