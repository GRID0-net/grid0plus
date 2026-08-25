#!/usr/bin/env python3
"""Add SwitchNet's CA to the Switch's built-in certificate store.

This is the alternative to the `disable_ca_verification` exefs patch, and it is
better in three ways:

  * It is keyed on TITLE ID, not on a firmware build id. An exefs IPS patch only
    applies to the exact firmware build it was made for, and when it does not
    match it silently does nothing -- every HTTPS call fails at the handshake and
    nothing appears in any log, because the connection never becomes a request.
    A LayeredFS data file has no such gate.

  * It ADDS a trusted CA instead of switching verification off. Certificate
    validation stays on for everything else, so the console is not left
    interceptable by anyone on the network while it talks to real Nintendo.

  * It is a data file, not a code patch, so it cannot crash the sysmodule. To
    undo it, delete one file.

HOW IT WORKS

The Switch's trusted CA list is a SystemData archive, title 0100000000000800,
containing /ssl_TrustedCerts.bdf in its RomFS. The ssl sysmodule's SSL context
uses that store for every connection. Atmosphere's LayeredFS can replace a file
in a data archive's RomFS, so appending an entry to that file adds a CA.

  https://switchbrew.org/wiki/SSL_services#CertStore

File format, little-endian:

  0x0   4    magic "sslT"
  0x4   4    total entries
  0x8   0x10 * N   entry array
               0x0  4  id          (CaCertificateId)
               0x4  4  status      (TrustedCertStatus; 1 = EnabledTrusted)
               0x8  4  data size
               0xC  4  data offset, RELATIVE TO ABSOLUTE OFFSET 0x8
  then the certificate data

IMPORTANT: THIS IS NOT VERIFIED ON HARDWARE

Two things about this approach are unconfirmed, and you should read
switch/README.md before relying on it:

  1. Whether Atmosphere's fs.mitm actually intercepts the ssl sysmodule's read of
     this archive. fs.mitm MITMs applications plus ns, settings and sdb/glue -- it
     does NOT MITM arbitrary system modules. If ssl reads the archive directly
     rather than through sdb/glue, LayeredFS will not see it.

  2. Whether the certificate data in the blob is DER or PEM. This script detects
     it from the file you supply and matches it, so it should be right -- but it
     is inferred, not documented.

switch/README.md has a five-minute test that settles (1) before you spend any
time on it.

USAGE

  # 1. Dump the original from your console (nxdumptool can dump system titles).
  #    You need the RomFS of title 0100000000000800.
  #
  # 2. Inspect it, to check this script understands your firmware's file:
  python3 scripts/build-certstore.py --inspect ssl_TrustedCerts.bdf

  # 3. Append your CA:
  python3 scripts/build-certstore.py \\
      --input  ssl_TrustedCerts.bdf \\
      --ca     certs/rootCA.pem \\
      --output out/ssl_TrustedCerts.bdf

  # 4. Copy to the SD card:
  #    /atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf

NEVER build this file from scratch. It must be the console's own store with one
entry appended -- a file containing only SwitchNet's CA would break every other
TLS connection on the console: the browser, the eShop, Nintendo Account.
"""

from __future__ import annotations

import argparse
import base64
import struct
import sys
from pathlib import Path

MAGIC = b"sslT"
HEADER_LEN = 8
ENTRY_LEN = 0x10

# Data offsets in the entry array are relative to this absolute offset.
DATA_OFFSET_BASE = 0x8

# nn::ssl::TrustedCertStatus
STATUS_REMOVED = 0
STATUS_ENABLED_TRUSTED = 1
STATUS_ENABLED_NOT_TRUSTED = 2
STATUS_REVOKED = 3

STATUS_NAMES = {
    -1: "Invalid",
    STATUS_REMOVED: "Removed",
    STATUS_ENABLED_TRUSTED: "EnabledTrusted",
    STATUS_ENABLED_NOT_TRUSTED: "EnabledNotTrusted",
    STATUS_REVOKED: "Revoked",
}

# nn::ssl::CaCertificateId, the ones worth naming in the inspect output.
KNOWN_IDS = {
    1: "NintendoCAG3",
    2: "NintendoClass2CAG3",
    3: "Nintendo Root CA G4",
    1000: "AmazonRootCA1",
    1011: "DigiCertGlobalRootCA",
    1012: "DigiCertGlobalRootG2",
}

# The id given to SwitchNet's CA.
#
# Nintendo's own ids are 1-3, and public roots start at 1000. 9000 is clear of
# both, and clear of any plausible future addition.
SWITCHNET_CA_ID = 9000


class Entry:
    def __init__(self, cert_id: int, status: int, data: bytes):
        self.id = cert_id
        self.status = status
        self.data = data

    @property
    def format(self) -> str:
        if self.data.startswith(b"-----BEGIN"):
            return "pem"
        if self.data[:1] == b"\x30":
            return "der"
        return "unknown"

    def subject_hint(self) -> str:
        """A readable hint, without needing a certificate library."""
        blob = self.data
        if self.format == "pem":
            try:
                body = b"".join(
                    line for line in blob.splitlines()
                    if not line.startswith(b"-----")
                )
                blob = base64.b64decode(body)
            except Exception:  # noqa: BLE001 - a hint, not a parser
                return "?"
        # Pull printable runs out of the DER and pick a plausible CN.
        runs, current = [], bytearray()
        for byte in blob:
            if 0x20 <= byte < 0x7F:
                current.append(byte)
            else:
                if len(current) >= 6:
                    runs.append(current.decode("ascii"))
                current = bytearray()
        if len(current) >= 6:
            runs.append(current.decode("ascii"))
        for run in runs:
            if any(k in run for k in ("CA", "Root", "Nintendo", "Authority", "SwitchNet")):
                return run
        return runs[0] if runs else "?"


def parse(blob: bytes) -> list[Entry]:
    if len(blob) < HEADER_LEN:
        raise ValueError(f"file is {len(blob)} bytes, too short for a header")
    if blob[:4] != MAGIC:
        raise ValueError(
            f"bad magic {blob[:4]!r}, expected {MAGIC!r}. "
            "Is this really ssl_TrustedCerts.bdf, and is it the RomFS file rather "
            "than the NCA that contains it?"
        )

    count = struct.unpack_from("<I", blob, 4)[0]
    if count == 0 or count > 10000:
        raise ValueError(f"implausible entry count {count}")

    array_end = HEADER_LEN + count * ENTRY_LEN
    if array_end > len(blob):
        raise ValueError(f"entry array of {count} runs past the end of the file")

    entries = []
    for i in range(count):
        offset = HEADER_LEN + i * ENTRY_LEN
        cert_id, status, size, data_off = struct.unpack_from("<iiII", blob, offset)
        start = DATA_OFFSET_BASE + data_off
        end = start + size
        if end > len(blob):
            raise ValueError(
                f"entry {i} (id {cert_id}) points at {start}..{end}, "
                f"past the end of the {len(blob)}-byte file"
            )
        entries.append(Entry(cert_id, status, blob[start:end]))
    return entries


def build(entries: list[Entry]) -> bytes:
    """Serialise entries back into the file format."""
    count = len(entries)
    array_len = count * ENTRY_LEN
    # Data begins right after the entry array.
    data_start = HEADER_LEN + array_len

    array = bytearray()
    data = bytearray()
    for entry in entries:
        # The stored offset is relative to absolute 0x8.
        data_off = (data_start - DATA_OFFSET_BASE) + len(data)
        array += struct.pack("<iiII", entry.id, entry.status, len(entry.data), data_off)
        data += entry.data

    return MAGIC + struct.pack("<I", count) + bytes(array) + bytes(data)


def load_ca(path: Path, want_format: str) -> bytes:
    """Read a CA certificate and convert it to the format the store uses."""
    raw = path.read_bytes()
    is_pem = raw.lstrip().startswith(b"-----BEGIN")

    if want_format == "pem":
        if is_pem:
            # Normalise the trailing newline: entries are concatenated, and a
            # missing one would run two PEM blocks together.
            return raw if raw.endswith(b"\n") else raw + b"\n"
        body = base64.encodebytes(raw).replace(b"\n", b"")
        lines = [body[i:i + 64] for i in range(0, len(body), 64)]
        return (b"-----BEGIN CERTIFICATE-----\n" + b"\n".join(lines)
                + b"\n-----END CERTIFICATE-----\n")

    # DER
    if not is_pem:
        return raw
    body = b"".join(l for l in raw.splitlines() if not l.startswith(b"-----"))
    return base64.b64decode(body)


def inspect(path: Path) -> None:
    entries = parse(path.read_bytes())
    size = path.stat().st_size

    print(f"{path}  ({size} bytes, {len(entries)} entries)")
    print()
    print(f"  {'id':<26} {'status':<18} {'fmt':<4} {'bytes':>6}  subject")
    print(f"  {'-' * 26} {'-' * 18} {'-' * 4} {'-' * 6}  {'-' * 40}")

    formats = set()
    for entry in entries:
        formats.add(entry.format)
        name = KNOWN_IDS.get(entry.id, "")
        label = f"{entry.id}" + (f" ({name})" if name else "")
        print(f"  {label:<26} {STATUS_NAMES.get(entry.status, entry.status):<18} "
              f"{entry.format:<4} {len(entry.data):>6}  {entry.subject_hint()[:44]}")

    print()
    if len(formats) == 1:
        print(f"All entries are {formats.pop().upper()}. That is the format a new "
              "entry will be written in.")
    else:
        print(f"WARNING: mixed formats {sorted(formats)}. That is unexpected; "
              "please report it with this output.")

    if any(e.id == SWITCHNET_CA_ID for e in entries):
        print(f"Entry {SWITCHNET_CA_ID} is already present: this file already has "
              "SwitchNet's CA in it.")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Add SwitchNet's CA to the Switch's built-in certificate store.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("--inspect", metavar="PATH",
                        help="print the contents of a certificate store and exit")
    parser.add_argument("--input", metavar="PATH",
                        help="the ssl_TrustedCerts.bdf dumped from your console")
    parser.add_argument("--ca", metavar="PATH",
                        help="the CA to add, PEM or DER (certs/rootCA.pem)")
    parser.add_argument("--output", metavar="PATH",
                        help="where to write the new store")
    parser.add_argument("--id", type=int, default=SWITCHNET_CA_ID,
                        help=f"the CaCertificateId to use (default: {SWITCHNET_CA_ID})")

    args = parser.parse_args()

    if args.inspect:
        try:
            inspect(Path(args.inspect))
        except (OSError, ValueError) as exc:
            sys.exit(f"build-certstore: {exc}")
        return

    missing = [n for n, v in [("--input", args.input), ("--ca", args.ca),
                              ("--output", args.output)] if not v]
    if missing:
        parser.error("missing required arguments: " + ", ".join(missing))

    try:
        entries = parse(Path(args.input).read_bytes())
    except (OSError, ValueError) as exc:
        sys.exit(f"build-certstore: reading {args.input}: {exc}")

    print(f"build-certstore: read {len(entries)} entries from {args.input}")

    formats = {e.format for e in entries}
    if len(formats) != 1 or "unknown" in formats:
        sys.exit(f"build-certstore: could not determine the certificate format "
                 f"(saw {sorted(formats)}). Run --inspect and report the output.")
    store_format = formats.pop()
    print(f"build-certstore: the store holds {store_format.upper()} certificates")

    # Replace rather than duplicate, so running this twice is safe.
    entries = [e for e in entries if e.id != args.id]

    try:
        ca = load_ca(Path(args.ca), store_format)
    except (OSError, ValueError) as exc:
        sys.exit(f"build-certstore: reading {args.ca}: {exc}")

    entries.append(Entry(args.id, STATUS_ENABLED_TRUSTED, ca))
    print(f"build-certstore: appended id {args.id} ({len(ca)} bytes) as EnabledTrusted")

    blob = build(entries)

    # Round-trip before writing. A store the console cannot parse is a console
    # that trusts no CA at all, which breaks far more than it fixes.
    try:
        reparsed = parse(blob)
    except ValueError as exc:
        sys.exit(f"build-certstore: the file just built does not parse: {exc}")
    if len(reparsed) != len(entries):
        sys.exit("build-certstore: round trip lost entries")
    for original, parsed in zip(entries, reparsed):
        if original.id != parsed.id or original.data != parsed.data:
            sys.exit(f"build-certstore: round trip corrupted entry {original.id}")
    print(f"build-certstore: verified the round trip ({len(reparsed)} entries)")

    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(blob)

    print()
    print(f"build-certstore: wrote {out} ({len(blob)} bytes)")
    print()
    print("Copy it to the SD card at:")
    print("  /atmosphere/contents/0100000000000800/romfs/ssl_TrustedCerts.bdf")
    print()
    print("Then read switch/README.md: there is a test to run FIRST that tells")
    print("you whether Atmosphere is serving this file to the ssl sysmodule at")
    print("all. That is the part of this approach which is not confirmed.")


if __name__ == "__main__":
    main()
