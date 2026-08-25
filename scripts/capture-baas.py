#!/usr/bin/env python3
"""Capture the real BAAS friends API from a PC, without touching the console.

This is the recommended way to get the captures SwitchNet still needs. It talks
to Nintendo's real BAAS server from your computer, using your console's
credentials, and writes the responses to disk.

Compared to running mitmproxy against the console it has two large advantages:

  * The console is not involved at all. It does not need to be on, and it never
    runs with certificate verification disabled while talking to Nintendo --
    which is the main ban vector in the mitmproxy approach and is exactly what
    produces error 2137-7403.

  * You get the real responses, decoded, in one shot.

WHAT IT NEEDS

  1. prod.keys                Lockpick_RCM
  2. PRODINFO                 hekate  (decrypt it if your setup encrypts it)
  3. Your BAAS device account id and password.

     They live in su/baas/<guid>.dat inside save folder 8000000000000010.
     In that file, bytes 0x20-0x28 are the user id in REVERSED byte order, and
     bytes 0x28-0x50 are the password as plain text. Use --parse-save to read
     them out of the file rather than by hand.

  pip install NintendoClients

WHAT IT SENDS TO NINTENDO

  A device token request, a BAAS login, and the friends reads you asked for.
  That is the same traffic a console makes when you open the friends applet.
  Nothing is written: no request here creates, accepts or deletes anything.

  It does mean Nintendo sees your real console identity, exactly as it does
  when the console is online normally. If you would rather it did not, use the
  --against-switchnet mode instead, which captures only the REQUEST shapes by
  pointing the same code at your own server. Those are the harder half to guess.

USAGE

  # read the credentials out of the save file
  python3 scripts/capture-baas.py --parse-save /path/to/8000000000000010/su/baas/xxx.dat

  # capture, against real Nintendo
  python3 scripts/capture-baas.py \
      --keys /path/to/prod.keys \
      --prodinfo /path/to/PRODINFO \
      --device-account-id 0123456789abcdef \
      --password "<40 characters>" \
      --country NL \
      --system-version 2250 \
      --out docs/CAPTURES/raw

  # the same requests against your own server, no Nintendo involved
  python3 scripts/capture-baas.py ... --against-switchnet https://192.168.1.50

The output is one JSON file per endpoint, plus a summary. Credentials are
redacted by default: see --no-redact if you specifically want them kept.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from pathlib import Path

def require_nintendoclients():
    """Import NintendoClients on demand.

    Imported lazily so that --parse-save, which only reads a local file, works
    on a machine where the library will not build. anynet pulls in netifaces,
    which needs a C compiler and Python headers.
    """
    try:
        import anyio
        from anynet import http
        from nintendo import switch
        from nintendo.switch import baas, dauth
    except ImportError as exc:
        sys.exit(
            f"capture-baas: {exc}\n"
            "\n"
            "  pip install NintendoClients anyio\n"
            "\n"
            "If that fails building netifaces, you need a compiler and headers:\n"
            "  Debian/Ubuntu  sudo apt install build-essential python3-dev\n"
            "  macOS          xcode-select --install\n"
        )
    # NintendoClients 5.0.0 bug: BAASClient._request uses self._host directly
    # instead of calling get_host(module), so self._host=None crashes.
    # Patch the class to resolve the correct per-module host on every call.
    _orig = baas.BAASClient._request
    async def _fixed(self, req, token, module, *, use_power_state=False):
        saved = self._host
        self._host = self.get_host(module)
        try:
            return await _orig(self, req, token, module,
                               use_power_state=use_power_state)
        finally:
            self._host = saved
    baas.BAASClient._request = _fixed

    return anyio, http, switch, baas, dauth


# Fields that are credentials rather than shapes. The shape is what SwitchNet
# needs; the value is a live credential for your account until it expires.
REDACT_KEYS = {
    "password",
    "accessToken",
    "idToken",
    "assertion",
    "device_auth_token",
    "application_auth_token",
    "dtoken",
    "mac",
    "challenge",
}

# Values worth blanking wherever they appear, because they identify hardware.
REDACT_PATTERNS = [
    (re.compile(r"XA[WJKterm][A-Z0-9]{11}"), "<SERIAL>"),
    # A JWT is three base64url segments; the header always starts "eyJ".
    (re.compile(r"eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*"), "<JWT>"),
]


def redact(value):
    """Replace credentials with a description of their shape.

    "<40 chars>" is as useful to somebody reconciling an implementation as the
    real password, and carries none of the risk.
    """
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if k in REDACT_KEYS and isinstance(v, str):
                out[k] = f"<REDACTED {len(v)} chars>"
            else:
                out[k] = redact(v)
        return out
    if isinstance(value, list):
        return [redact(v) for v in value]
    if isinstance(value, str):
        for pattern, replacement in REDACT_PATTERNS:
            value = pattern.sub(replacement, value)
        return value
    return value


def parse_save(path: Path) -> None:
    """Print the device account id and password from a baas save file."""
    data = path.read_bytes()
    if len(data) < 0x50:
        sys.exit(f"capture-baas: {path} is only {len(data)} bytes; expected at least 0x50")

    # The user id is stored in reversed byte order.
    user_id = int.from_bytes(data[0x20:0x28], "little")
    password = data[0x28:0x50].decode("ascii", errors="replace")

    print(f"device account id : {user_id:016x}")
    print(f"password          : {password}")
    print()
    print("Pass these as --device-account-id and --password.")
    print("Treat the password like a private key: it is this console's permanent")
    print("credential for the account and it cannot be rotated without")
    print("re-registering the console.")


class Capture:
    def __init__(self, out_dir: Path, do_redact: bool):
        self.out_dir = out_dir
        self.do_redact = do_redact
        self.index: list[dict] = []
        out_dir.mkdir(parents=True, exist_ok=True)

    def record(self, name: str, method: str, path: str, status: int, body) -> None:
        payload = {
            "endpoint": f"{method} {path}",
            "status": status,
            "body": redact(body) if self.do_redact else body,
        }
        target = self.out_dir / f"{name}.json"
        target.write_text(json.dumps(payload, indent=2, ensure_ascii=False) + "\n")

        shape = describe(body)
        self.index.append({"name": name, "endpoint": payload["endpoint"],
                           "status": status, "shape": shape})
        print(f"  {status}  {method} {path}")
        print(f"       -> {target}")
        print(f"       shape: {shape}")

    def finish(self) -> None:
        (self.out_dir / "index.json").write_text(
            json.dumps(self.index, indent=2) + "\n")
        print()
        print(f"capture-baas: wrote {len(self.index)} responses to {self.out_dir}")
        print()
        print("The single most important line above is the shape of")
        print("GET /2.0.0/users/<id>/friends. SwitchNet assumes a bare array;")
        print("if it says 'object with keys [...]' then internal/baas/friends.go")
        print("listFriends needs to wrap its result in that envelope.")


def describe(body) -> str:
    """A one-line summary of a response's shape.

    This is the actual deliverable for several of the open questions: whether the
    friend list is an array or an envelope is answerable from this line alone.
    """
    if isinstance(body, list):
        if not body:
            return "empty array"
        first = body[0]
        if isinstance(first, dict):
            return f"array[{len(body)}] of object with keys {sorted(first.keys())}"
        return f"array[{len(body)}] of {type(first).__name__}"
    if isinstance(body, dict):
        return f"object with keys {sorted(body.keys())}"
    return type(body).__name__


async def run(args, http, switch, baas, dauth) -> None:
    out_dir = Path(args.out)
    capture = Capture(out_dir, not args.no_redact)

    print("capture-baas: loading keys and PRODINFO")
    keys = switch.load_keys(args.keys)
    info = switch.ProdInfo(keys, args.prodinfo)
    cert = info.get_tls_cert()
    pkey = info.get_tls_key()
    print(f"  device id: {info.get_device_id():016x}")

    dauth_client = dauth.DAuthClient(keys)
    dauth_client.set_certificate(cert, pkey)
    dauth_client.set_system_version(args.system_version)

    baas_client = baas.BAASClient()
    baas_client.set_certificate(cert, pkey)
    baas_client.set_system_version(args.system_version)

    if args.against_switchnet:
        # Point the client at SwitchNet instead of Nintendo. The requests are
        # byte-for-byte what the console would send, so the server's own log
        # gives you the request shapes with no Nintendo traffic at all.
        host = args.against_switchnet.replace("https://", "").replace("http://", "")
        baas_client.set_host(host)
        print(f"capture-baas: talking to SwitchNet at {host}, not Nintendo")

    print("capture-baas: requesting a device token")
    if args.system_version >= 2000:
        response = await dauth_client.device_tokens([dauth.CLIENT_ID_BAAS])
        device_token = response["results"][0]["device_auth_token"]
    else:
        response = await dauth_client.device_token(dauth.CLIENT_ID_BAAS)
        device_token = response["device_auth_token"]

    print("capture-baas: POST /1.0.0/application/token")
    response = await baas_client.authenticate(device_token)
    capture.record("01-application-token", "POST", "/1.0.0/application/token",
                   200, response)
    anon_token = response["accessToken"]

    print("capture-baas: POST /1.0.0/login")
    login = await baas_client.login(
        int(args.device_account_id, 16), args.password, anon_token,
        na_country=args.country,
    )
    capture.record("02-login", "POST", "/1.0.0/login", 200, login)

    user_id = int(login["user"]["id"], 16)
    access_token = login["accessToken"]
    print(f"  logged in as {user_id:016x}")

    # A raw request helper. _request is private, but it puts the headers in the
    # order the console uses, which is the point of capturing at all. This is a
    # capture tool, not production code.
    async def get(name: str, path: str, params: dict | None = None) -> None:
        req = http.HTTPRequest.get(path)
        if params:
            req.params = params
        try:
            response = await baas_client._request(req, access_token, baas.MODULE_FRIENDS)
            capture.record(name, "GET", path, response.status_code, response.json)
        except Exception as exc:  # noqa: BLE001 - a failed endpoint is still a result
            print(f"  !! GET {path} failed: {exc}")
            capture.index.append({"name": name, "endpoint": f"GET {path}",
                                  "status": "error", "shape": str(exc)})

    print()
    print("capture-baas: the friends endpoints")
    print("  (this is the part SwitchNet is guessing at)")

    # 1. THE most important one. Array or envelope?
    await get("03-friends", f"/2.0.0/users/{user_id:016x}/friends",
              {"count": 300})

    # 2 and 3. The inbox and outbox shapes.
    await get("04-friend-requests-inbox",
              f"/2.0.0/users/{user_id:016x}/friend_requests/inbox")
    await get("05-friend-requests-outbox",
              f"/2.0.0/users/{user_id:016x}/friend_requests/outbox")

    # 4. The self record, for comparison against what SwitchNet returns.
    await get("06-user-self", f"/1.0.0/users/{user_id:016x}")

    # 5. Blocks.
    await get("07-blocks", f"/1.0.0/users/{user_id:016x}/blocks")

    # 6. The relationship shape, using the first friend if there is one.
    friend_id = None
    friends_file = out_dir / "03-friends.json"
    if friends_file.exists():
        try:
            body = json.loads(friends_file.read_text())["body"]
            # The envelope shape is {count, etag, items, itemsPerPage}; fall
            # back to a bare list for older captures.
            entries = body.get("items", body) if isinstance(body, dict) else body
            if entries and isinstance(entries[0], dict):
                # Relationship objects use "friendId"; plain user objects use "id".
                friend_id = entries[0].get("friendId") or entries[0].get("id")
        except (KeyError, ValueError, AttributeError):
            pass

    if friend_id:
        await get("08-relationship",
                  f"/2.0.0/users/{user_id:016x}/relationships/{friend_id}")
    else:
        print("  -- skipping relationships: no friend found in the friend list.")
        print("     Add a friend on the console first; the relationship shape is")
        print("     only observable when there is a relationship.")

    capture.finish()

    print()
    print("Next:")
    print("  1. Check the files for anything the redaction missed. Serial")
    print("     numbers and JWTs are blanked; read them before committing.")
    print("  2. Write each up under docs/CAPTURES/ using the template in")
    print("     docs/CAPTURES/README.md.")
    print("  3. Reconcile internal/baas, and delete the matching")
    print("     TODO(capture-needed) comment in the same commit.")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Capture the real BAAS friends API from a PC.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("--parse-save", metavar="PATH",
                        help="read the device account id and password out of a "
                             "baas save file and exit")
    parser.add_argument("--keys", default=os.environ.get("SWITCH_PROD_KEYS"),
                        help="path to prod.keys (Lockpick_RCM)")
    parser.add_argument("--prodinfo", default=os.environ.get("SWITCH_PRODINFO"),
                        help="path to PRODINFO, decrypted (hekate)")
    parser.add_argument("--device-account-id",
                        help="BAAS device account id, 16 hex digits")
    parser.add_argument("--password", help="BAAS device account password, 40 chars")
    parser.add_argument("--country", default="NL",
                        help="country of your Nintendo account, e.g. NL (default: NL)")
    parser.add_argument("--system-version", type=int, default=2250,
                        help="system version as an integer, e.g. 2250 for 22.5.0 "
                             "(default: 2250)")
    parser.add_argument("--out", default="docs/CAPTURES/raw",
                        help="output directory (default: docs/CAPTURES/raw)")
    parser.add_argument("--against-switchnet", metavar="URL",
                        help="talk to your own SwitchNet instead of Nintendo. "
                             "Captures request shapes only, with no Nintendo traffic")
    parser.add_argument("--no-redact", action="store_true",
                        help="keep credentials in the output. Do not commit the result")

    args = parser.parse_args()

    if args.parse_save:
        parse_save(Path(args.parse_save))
        return

    missing = [name for name, value in [
        ("--keys", args.keys),
        ("--prodinfo", args.prodinfo),
        ("--device-account-id", args.device_account_id),
        ("--password", args.password),
    ] if not value]
    if missing:
        parser.error("missing required arguments: " + ", ".join(missing))

    if len(args.password) != 40:
        print(f"capture-baas: warning: the password is {len(args.password)} "
              "characters, expected 40", file=sys.stderr)

    anyio, http, switch, baas, dauth = require_nintendoclients()
    anyio.run(run, args, http, switch, baas, dauth)


if __name__ == "__main__":
    main()
