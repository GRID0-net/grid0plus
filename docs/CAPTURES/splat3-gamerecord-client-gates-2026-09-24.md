# Splatoon 3 GameRecord client-side call gates

This note records the current static-analysis findings for why the console may
skip the early per-user GameRecord document reads that appear in the working
Nextendo hall captures. It is a partial RE result, not a confirmed root cause.

## Binary and address convention

- Splatoon 3 v11.3.0, build ID
  `28C4287AEE36F7499DA60F3E68B54C70DA382D75`.
- ELF: `/tmp/splat3-crash.elf` (analysis input; not a repository artifact).
- Ghidra program base: `0x7100000000`; addresses below are runtime addresses.
- Decompiled in a temporary copy of the existing Ghidra project under `/tmp`;
  the original project and all raw captures were left untouched.

## What the working captures show

Both preserved Nextendo hall captures request these `GameRecord` documents
after the second schedule batch and before `Canola`/`Locker`:

- `CoopUserAttribute/Data`
- `VsUserAttribute/Data`
- `WeaponPowerMeasurement` (mode-specific document)
- `BankaraChallenges`
- `PointCardTotal`
- `PointCardRegular`

They also call `GameRecord/InitializeAttributes` on the fresh-profile path. The
SwitchNet hall runs discussed in the investigation do not issue this early
batch; they proceed to later calls. The absent call is therefore currently a
client-side boundary, not an RPC that SwitchNet logged and rejected.

## Client code recovered

`FUN_7103283740` (`0x7103283740`) constructs
`services/GameRecord/users/<current-user>/VsUserAttribute/Data` and queues the
document read through the client document machinery. It optionally queues
`WeaponPowerMeasurement/<id>` when its second argument is positive. It returns
without queuing unless the per-user resource lookup succeeds, the current user
string is nonempty, and both 32-bit status fields at object offsets `+0x10c`
and `+0x110` are in the accepted completed range (unsigned value at least 8).
On success it marks the mode as VS (`+0x110 = 1`). Its dispatch slot is the
function pointer at `0x7105d916b0`.

`FUN_7103283d20` (`0x7103283d20`) is the parallel Coop routine. It constructs
`services/GameRecord/users/<current-user>/CoopUserAttribute/Data`, applies the
same status and identity gates, then marks the mode as Coop (`+0x110 = 2`). Its
dispatch slot is `0x7105d916b8`.

The neighboring table at `0x7105d91670` starts with eight small accessors that
read object fields `+0x110` through `+0x12c`; the VS and Coop routines are the
next two entries (`+0x40` and `+0x48`). This is strong evidence that the slots
belong to one acquisition-object method table, rather than being independent
free functions. The next larger handler, `FUN_7103284090` (`0x7103284090`),
queues `PointCardRegular/<key>`, `PointCardLimited/<key>`, and
`PointCardTotal/Data` when its own `+0x118` readiness value is at least 8.
`FUN_7103286310` separately queues
`BankaraChallenges/<challenge>` after its readiness checks. These are client
paths for more requests in the captured early batch.

The `GameRecord` component is registered by `FUN_7101523eb0` under the
`"GameRecord"` label; it allocates a `0x418`-byte object and initializes it
through `FUN_710328b2a0`. That constructor installs the separate method table
at `0x7105d91bf0`. Its update method is `FUN_710328bcc0` at table slot
`0x7105d91c20`. Crucially, the manager's own virtual slots `+0x40/+0x48` are
not the VS/Coop document callbacks: in this table they resolve to short
accessors at `0x710328ece8` and `0x710328ecf8`. The update method processes
component state and polls its jobs under a mutex, but it is not yet proven to
invoke the VS/Coop fetch table directly.

The constructor/call path identifies the `GameRecord` subsystem that owns the
work, but not yet the exact indirect call sites to the VS/Coop callbacks. A
separate manager method, `FUN_71032869c0`, does make a virtual call at `+0x40`
before resetting a parent state; its exact vtable owner still needs to be
matched to `0x7105d91670` before treating it as the fetch caller. This remains
the next concrete dispatch-resolution task.

A follow-up pass confirmed the table layout but not the owning object.
`TraceFetchTable.java` dumped the full contiguous table starting at
`0x7105d91670` (entry `+0x00` = `0x7103288700`, ... `+0x40` =
`0x7103283740` (VS fetch), `+0x48` = `0x7103283d20` (Coop fetch), continuing
through `+0x110`/`0x7105d91780`), which matches and extends the eight-accessor
description above — this is a single ~30-entry method table, and the VS/Coop
fetch pair sits inside it rather than at its start. Separately,
`TraceVtableBaseRefs.java` searched Ghidra's reference manager for any code
reference *to the table's own base address* `0x7105d91670` (i.e., a
constructor writing that address into an object's vtable-pointer slot, the
usual way to find "what type is this") and found **zero references** — in
contrast to the same query against the `GameRecord` component's own table
base `0x7105d91bf0`, which correctly returned the two known constructor
references (`FUN_710328b2a0`, called from `FUN_7101523eb0`). This is a real
result, not a script bug: it means whatever loads `0x7105d91670` into a
register does so in a way Ghidra's static reference analysis does not resolve
into a data reference — most likely a PC-relative `ADRP`+`ADD` pair the
decompiler did not fold into a recognized address-of, or a level of
indirection (a table of vtable pointers, or a pointer stored in another
object and copied at runtime) that a pure address-reference search cannot
follow. Grepping the decompilations already on hand
(`/tmp/ghidra-s3-acq-methods.log`, the per-entry decompiles of this same
table's other slots) did not surface a sibling constructor either. Resolving
this now needs either a disassembly-level scan for the literal 64-bit
immediate `0x7105d91670` split across an `ADRP`/`ADD` (or `MOVZ`/`MOVK`) pair
— which the reference manager does not do for you — or a runtime/debugger
trace, which is out of scope for this pass.

These routines make the absent reads dependent on an earlier producer calling
them after readiness has been reached. The `+0x110` mode marker is overwritten
by the VS/Coop routine itself; it is not by itself proof of a server response
or a GameRecord document's existence.

## What advances a related status field

`FUN_710327f70c` is a large asynchronous state-machine/update function. In the
`+0x10c` state branch it polls an async operation, interprets its result, and
sets the field to `8` on its success path or `9` on its error path. Depending
on the mode selector at `+0x114`, its follow-up calls include a `VsParse` or
`CoopParse` callback. A distinct `+0x114` path also constructs
`services/GameRecord/users/<current-user>/VsResults` or `CoopResults` and hands
that resource to another helper. The observed `+0x10c` value is therefore a
local async state, not a wire field.

Related methods in this region confirm the same style of gating:

- `FUN_7103286310` queues
  `services/GameRecord/users/<current-user>/BankaraChallenges/<challenge>`
  only after its own readiness checks; it then resets its status field.
- `FUN_7103287460` queues
  `services/GameRecord/users/<current-user>/PointCardBigRun/<timestamp>`
  only after `+0x10c` and `+0x110` have completed; it marks its own state as
  `7` after queueing.

This shows several GameRecord reads share local acquisition/readiness gates,
but the exact meaning and producer of the second `+0x110 >= 8` prerequisite
are not yet established. In particular, it is not yet proven that schedule
responses advance either field.

A related object, decompiled at `FUN_710328d908` (table slot `0x7105d91c48`,
part of the `GameRecord` manager's own method table, not the VS/Coop table),
is the acquisition routine for `PointCardBigRun`/`BigRun_<timestamp>`. It
early-outs (returns `0`, doing nothing) when a sibling field at `param_1[1]`
is zero *or* when a virtual call through `*param_1 + 0x40` already reports the
low bit set — i.e., this handler has its own "already have it" completion
check, structurally identical to the pattern used by the VS/Coop routines, but
it belongs to a different vtable (the manager's own, confirmed by
`TraceOwnerTable.java`/`TraceOwnerFuncs.java` matching `0x7105d91c48` to
`FUN_710328d908`, distinct from `0x7105d91670`). This confirms the readiness/
already-done gating pattern generalizes across several `GameRecord` child
acquisitions and is not unique to the VS/Coop pair, but it does not by itself
resolve who calls the VS/Coop pair.

## Current interpretation and open work

The strongest supported conclusion is that the missing early batch is skipped
because a client-side caller does not reach (or does not successfully pass) the
per-user VS/Coop fetch callbacks. The server cannot cause these reads merely by
returning a different `GetDocument` answer, because the reads are never sent.

Still unresolved:

1. Which function's object uses the method table at `0x7105d91670` to invoke
   slots `0x7105d916b0` and `0x7105d916b8`, and under what lifecycle event.
   **Now further narrowed but still blocked**: the table itself is confirmed
   contiguous and ~30 entries long (see above), but Ghidra's static reference
   manager finds zero code references to the table's base address at all,
   meaning the constructor that installs this vtable on an object cannot be
   found through ordinary data-xref search. It is likely reached through an
   `ADRP`/`ADD` pair not folded into a recognized reference, or through a
   level of pointer indirection. A literal-immediate disassembly scan, or a
   runtime trace, is needed next — neither was performed in this pass.
2. What event/tick invokes `FUN_710328bcc0` on the registered GameRecord
   component, and how that component schedules its child acquisitions.
3. What exact operations own the two prerequisite status values, especially
   the producer for `+0x110 >= 8` before the VS/Coop marker is written.
4. Whether one of those operations is a response to a SwitchNet endpoint,
   local save initialization, or another client subsystem.
5. Which capture/log event corresponds to those client transitions. Existing
   SwitchNet logs show no early GameRecord reads, but cannot reveal a skipped
   local callback by themselves.

Next RE step: match the receiver vtable used by `FUN_71032869c0` and sibling
methods against the table at `0x7105d91670`; then trace the fetch-method caller
and each readiness-field writer back to its entry callback and any network or
save operation. Only after that should a server-side response mismatch be
inferred or changed.

**Net assessment for SwitchNet's purposes, given everything gathered so far**:
nothing found in this pass points at a specific NPLN response field as the
producer of the `+0x110 >= 8` gate. The gate machinery (readiness fields,
"already done" bit checks, per-child acquisition objects) looks purely local
to the client's `GameRecord` component, and the one concretely-identified
async state producer (`FUN_710327f70c`) sets its field from polling an
internal async operation's *result*, not from parsing a specific wire value.
This is circumstantial, not conclusive — the async operation being polled
could still originate from a network call — but there is no evidence in hand
that ties the gate to a specific SwitchNet response shape. Until the
`0x7105d91670` constructor/caller is found, this should not be treated as
either confirmed-local or confirmed-network; it remains open.

## Evidence files

Temporary Ghidra script outputs used for this pass:

- `/tmp/ghidra-s3-funcs3.log` — VS/Coop fetch callback decompilation.
- `/tmp/ghidra-s3-fields.log` — related status-machine and document-read
  helpers.
- `/tmp/ghidra-s3-manager.log` — the manager that polls both fetch callbacks
  and its state helpers.
- `/tmp/ghidra-s3-fetch-table.log` (script `TraceFetchTable.java`) — full
  dump of the `0x7105d91670` method table's entries.
- `/tmp/ghidra-s3-vtable-base-refs.log` (script `TraceVtableBaseRefs.java`) —
  reference-manager search for code references to `0x7105d91670` and
  `0x7105d91bf0`; the former returned no results, the latter matched the
  known `GameRecord` component constructor.
- `/tmp/ghidra-s3-owner-table.log`, `/tmp/ghidra-s3-owner-funcs.log` — the
  `GameRecord` manager's own method table (`0x7105d91bf0`+) and its member
  functions, including `FUN_710328d908` (`PointCardBigRun` acquisition,
  slot `0x7105d91c48`), used above to confirm the same completion-gate
  pattern recurs outside the VS/Coop table.

These scratch outputs are not required to interpret the conclusions above and
contain only static-analysis output, not preserved packet captures.
