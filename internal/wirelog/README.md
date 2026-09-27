# internal/wirelog

Records the HTTP requests a console makes, hexdumped and in order, so an
undocumented exchange becomes a transcript instead of a guess.

| | |
| --- | --- |
| Reimplements | nothing of Nintendo's |
| Used by | `internal/nplnrest` (`npln.rest_record_dir`), `internal/bcat`'s probe (`stubs.bcat_record_dir`) |
| Needs a database | no |

## Why it matters here

This deployment never decrypts the console's traffic with Nintendo (see
`CLAUDE.md`, "Where to ask the operator"), so the only full transcripts of a
console's requests are the ones GRID0+ writes while answering them. Point
a `Recorder` at a directory, put the console through one boot, and write the
result up under `docs/CAPTURES/`.

## What it guarantees

- **Off unless a directory is configured**, because it writes whatever the
  console sends.
- **Credential headers are redacted** before anything touches disk: the
  header is kept (which credential the console presented is part of the
  record) but its value is replaced by its length. Bodies are written as
  sent, so read a transcript before committing any of it.
- **One file per exchange, numbered**, so the order the console asked in is
  the order on disk.
