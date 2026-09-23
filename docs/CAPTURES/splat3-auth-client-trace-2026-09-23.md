# Splatoon 3 auth client trace (2026-09-23)

This is static analysis of the operator's Splatoon 3 v11.3.0 `main` dump,
build ID `28C4287AEE36F7499DA60F3E68B54C70DA382D75`. It is not a Nintendo
packet capture. Addresses below use the Ghidra project's `0x7100000000` base.
No game binary, credentials, or raw captures are included here.

## What the binary establishes

The existing `splat3_main` Ghidra project had already completed full automatic
analysis (`ghidra_import.log`: “Analysis succeeded”, 4223 seconds). The
`-noanalysis` option on later headless invocations reused that database; it did
not mean the project was unanalyzed.

`0x7100058140` constructs the generated Auth client stub. It places
`/nn.npln.auth.v1.Auth/IssuePrearrangedUserToken` at object offset `0x70`
(`0x71000581f4`–`0x71000581fc`), next to the method's call object at `0x68`.
It places `/nn.npln.auth.v1.Auth/ValidateToken` at offset `0xb8`
(`0x7100058254`–`0x710005825c`), next to its call object at `0xb0`.
`0x71000580f8` allocates this stub and calls that constructor. Its two direct
callers, `0x7100026320` and `0x710001bc20`, create two Auth service clients;
`0x710001a900` owns the first, and `0x71000266a0` owns the second.

The other occurrences of these method names are at `0x7105f66070` and
`0x7105f660a0` in a protobuf descriptor. Each entry points to name/type
strings, not a handler address. The Auth client vtable reachable from
`0x7105f3ce10` likewise contains generic lifecycle/executor methods, rather
than one response callback per RPC. Thus, the proposed `{method name,
handler pointer}` search does **not** reveal the game's response-handling code.
It would be incorrect to label any neighboring pointer as the callback.

`ValidateToken` returns `google.protobuf.Empty` according to the protocol
definition. Its response body therefore cannot itself carry a readiness flag;
any game state change would have to follow from RPC completion/status or from
another concurrent operation. This pass has not found the completion callback
or a client-side retry condition. It has not established a check on
`User.name`, `Token.access_token`, or any other
`IssuePrearrangedUserTokenResponse` field.

## What the latest hardware attempt establishes

At 18:00:11 UTC the server logged `IssuePrearrangedUserToken` as OK, followed
by `ListLatencyMeasurementServers` as OK with one server. It logged
`ValidateToken` as OK at 18:00:27 and 18:00:31. The client made no subsequent
gRPC call visible to the NPLN backend during that attempt. The console still
showed the hall's communication error.

The Azure packet capture shows five 20-byte STUN Binding requests to UDP 3478
from the console's public address between 18:00:11.471 and 18:00:11.884.
The VM sent a 32-byte Binding Success response with the matching transaction
ID and an XOR-MAPPED-ADDRESS for each request. This proves the VM answered the
probes on the wire. It does not prove the console accepted the replies, nor that
latency measurement succeeded. It rules out the previous observation of
requests receiving only ICMP port-unreachable as the entire explanation for
the hall error.

Nextendo's server, used here only as a behavioral reference, advertises seven
latency measurement server resources. SwitchNet currently advertises one.
Neither this binary pass nor the Azure capture establishes a minimum count, so
that difference is a question for a controlled hardware test, not a confirmed
fix.

## Remaining question

The generated Auth stub and protobuf descriptor both stop at generic SDK
dispatch. Static references from the method-name strings do not identify the
game's completion callback, and the observed `ValidateToken` interval alone
does not prove a retry loop waiting on auth. A useful next experiment is a
targeted hardware run that records the plaintext NPLN HTTP/2 request/response
frames, UDP 3478 traffic, and a single controlled change to the latency-server
list. That can distinguish a rejected latency result from a failure in a later
client-only step. Keep any credentials and packet payloads out of this repo.
