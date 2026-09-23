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

## What the hardware attempts establish

At 18:00:11 UTC the server logged `IssuePrearrangedUserToken` as OK, followed
by `ListLatencyMeasurementServers` as OK with one server. It logged
`ValidateToken` as OK at 18:00:27 and 18:00:31. The client made no subsequent
gRPC call visible to the NPLN backend during that attempt. The console still
showed the hall's communication error.

The Azure packet capture for that attempt shows five 20-byte STUN Binding
requests to UDP 3478 from the console's public address between 18:00:11.471 and
18:00:11.884. The VM sent a 32-byte Binding Success response with the matching
transaction ID and an XOR-MAPPED-ADDRESS for each request. This proves the VM
answered those probes on the wire, but not that the console accepted the replies
or considered latency measurement successful. It ruled out the earlier
ICMP-port-unreachable result as the complete explanation.

A controlled follow-up enabled seven regional latency resources, using the
same region labels and endpoint as the local Nextendo reference. SwitchNet's
resource IDs in that test were still its own `switchnet-NN-udp` values, so this
was not yet an exact copy of the reference's resource names. At 19:17:53 UTC,
SwitchNet logged successful
`IssuePrearrangedUserToken` and `ListLatencyMeasurementServers` calls; it then
logged successful `ValidateToken` calls at 19:18:08 and 19:18:13. The capture
contains 35 STUN Binding Requests to UDP 3478, each answered with a Binding
Success response. The console still entered the hall reload/communication-error
loop, and no matchmaking RPC reached the backend. Therefore, changing the
count and region labels did not resolve the failure. This test did not settle
whether the resource IDs matter; SwitchNet now uses the reference IDs for the
next hardware attempt. The capture still cannot prove the client accepted the
STUN results.

The local Nextendo implementation uses the same seven region labels and
addresses all entries to its configured UDP endpoint. Its `ValidateToken`
returns `google.protobuf.Empty` on success. SwitchNet also returns that empty
message after the authenticated caller reaches the handler. Nextendo additionally
checks its own account-verification state, which is not a useful distinction for
this local SwitchNet account flow; both observed calls succeeded at the RPC
level.

A further hardware attempt used the exact seven resource IDs, region labels,
and single UDP endpoint from that reference. At 19:45:07 UTC, both
`IssuePrearrangedUserToken` and `ListLatencyMeasurementServers` returned OK;
`ValidateToken` returned OK at 19:45:25 and 19:45:31. The console again showed
the hall reload loop, and no matchmaking RPC appeared. The Frontline stream was
closed by the console at 19:46:08, followed immediately by a successful refresh
and a new stream. The STUN responder does not log individual packets, and no
packet capture was running for this attempt, so STUN receipt/reply is unknown
for this test. The exact resource-name difference is now ruled out as the sole
cause; the next capture needs to include UDP 3478 again.

## Remaining question

The generated Auth stub and protobuf descriptor both stop at generic SDK
dispatch. Static references from the method-name strings do not identify the
game's completion callback, and the observed `ValidateToken` interval alone
does not prove a retry loop waiting on auth. A useful next experiment is a
targeted hardware run that records the plaintext NPLN HTTP/2 request/response
frames, UDP 3478 traffic, and a single controlled change to the latency-server
list. That can distinguish a rejected latency result from a failure in a later
client-only step. Keep any credentials and packet payloads out of this repo.
