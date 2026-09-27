# Splatoon 3 private response comparison

This package is a temporary, opt-in diagnostic for the `CloudSave/GetSaveRecord`
response and two `Ugcstore/GetDocument` responses used during hall bootstrap.
It is disabled unless `SWITCHNET_NPLN_DIAGNOSTIC_USER`,
`SWITCHNET_NPLN_DIAGNOSTIC_SAVE_PATH`, `SWITCHNET_NPLN_DIAGNOSTIC_VS_PATH`, and
`SWITCHNET_NPLN_DIAGNOSTIC_COOP_PATH` are all set. The paths point to raw,
unframed protobuf messages held privately outside this repository. Startup
fails if a source is missing or malformed. No Nintendo capture, key, or
certificate belongs in Git.

Only the selected authenticated user receives these records. The save's name
and displayed nickname are retargeted, and account references within the save
and two attribute documents are retargeted. The preserved working captures
pair a save and attributes whose original owners differ; the bundle handles
them independently. The save still contains the source account's progression,
so this is strictly a temporary diagnostic, not a player initialization path. Other users and other documents retain the
normal implementation. The imported game progression is intentionally not
written to GRID0+'s database: this tests bootstrap behavior only. Remove
the four environment variables and private files after the diagnostic.

The behavior under comparison is documented in
[`docs/CAPTURES/splat3-hall-boundary-2026-09-24.md`](../../docs/CAPTURES/splat3-hall-boundary-2026-09-24.md).
Protocol definitions come from
[kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols).
