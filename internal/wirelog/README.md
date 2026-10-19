# wirelog

Records the HTTP requests a console makes, hexdumped and in order, so an exchange nobody
has documented becomes a transcript instead of a guess.

It stands in for nothing of Nintendo's. It is used by `internal/nplnrest`
(`npln.rest_record_dir`) and by the BCAT probe in `internal/bcat` (`stubs.bcat_record_dir`).

Point a `Recorder` at a directory and put a console through one boot. GRID0+ never
decrypts a console's traffic with Nintendo, so these are the only full transcripts of what
a console asks for.

- **It is off unless a directory is configured**, because it writes whatever the console
  sends.
- **Credential headers are redacted** before anything touches disk. The header stays,
  since which credential the console presented is part of the record, but its value is
  replaced by its length. Bodies are written as sent, so read a transcript before sharing
  it.
- **One numbered file per exchange**, so the order on disk is the order the console asked
  in.
