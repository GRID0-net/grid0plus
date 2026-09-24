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

## Follow-up pass: literal-immediate disassembly scan (2026-09-25)

This pass ran the disassembly-level scan flagged as the next step above,
using a new script, `ScanLiteralImm.java`, run against the same project
(`/home/nico/switchnet-re/ghidra_projects/splat3_main`, program `splat3-crash.elf`,
base `0x7100000000`; note the earlier `/tmp/splat3_main` scratch project from
the prior pass no longer existed and the real, persistent project path had to
be used instead). Also new context motivating this pass: live hardware
retesting (including a from-scratch account) confirmed the same symptom
deterministically — the console never calls `FestService/SelectFestSchedule`,
never reads any of the 8 `GameRecord`/`Ugcstore.GetDocument` documents, never
calls `GameRecord/InitializeAttributes`, and never calls
`CloudSave/CreateSaveRecord`, then the NPLN session silently restarts every
70-90 seconds — with every server-observed RPC returning OK and the working
reference captures ruled out as a configuration difference. This raised the
priority of resolving who installs/calls the `0x7105d91670` table.

**Method.** The script iterates every instruction in `.text`
(`0x7100000000`-`0x7104e8afcf`, 15,130,988 instructions total) and, per
function, tracks register state across three AArch64 addressing idioms that
could materialize the table's address as a literal 64-bit value:

1. `ADRP`+`ADD` pairs (the idiom Ghidra's own compiler output already showed
   is used for the sibling `0x7105d91bf0` table, confirmed by direct
   disassembly at `0x710328b2b4`/`0x710328b2b8`: `adrp x8,0x7105d91000`
   then `add x8,x8,#0xbf0`);
2. `MOVZ`+`MOVK` register-accumulation sequences (parsed from operand text,
   honoring any `LSL #n` shift);
3. any single instruction operand that is directly an `Address` or `Scalar`
   equal to a target (covers literal-pool loads and any idiom that resolves
   to one operand).

**Calibration.** Before trusting a negative result on the harder target, the
scan was run against both `0x7105d91670` (the VS/Coop table) and
`0x7105d91bf0` (the `GameRecord` manager's own table, already known from
ordinary xref search to have exactly two references) in the same pass. The
scan correctly found both known `0x7105d91bf0` references
(`710328b2b8` in `FUN_710328b2a0`, `710328e8f4` in `FUN_710328e8e0`, both
`ADRP`+`ADD`) — confirming the scan technique itself works as intended, per
this task's explicit calibration requirement.

**Result on the target table: still zero, across every idiom tried.**

- `ADRP`+`ADD`, exact match on `0x7105d91670`, `0x7105d916b0` (VS slot), and
  `0x7105d916b8` (Coop slot): 0 hits.
- `MOVZ`+`MOVK` accumulation, same three targets: 0 hits (in fact `MOVZ`/
  `MOVK` do not appear to be used anywhere in `.text` for materializing
  data addresses at all in this build — 0 matches of any kind from that
  idiom across the whole scan).
- Direct single-operand scalar/address match, same three targets: 0 hits.
- A follow-up windowed scan (same `ADRP`+`ADD` tracking, but logging any
  computed target within `0x7105d91400`-`0x7105d91900`, i.e. roughly ±0x400
  bytes around the table) found 60 references — but every one lands outside
  the table's own span (`0x7105d91670`-`0x7105d917f0`, per the previously
  confirmed ~30-entry/0x780-byte extent). These nearby hits are almost all
  from the `FUN_710327xxxx` family (the same GameRecord async/readiness
  functions already documented above), which is consistent with the
  compiler having placed several sibling vtables/RTTI/string data for
  neighboring classes in the same translation unit contiguously in `.data` —
  it does not point at a hidden anchor for the table itself.
- Also checked, as a separate hypothesis specific to this being a
  statically-allocated singleton: the ELF's dynamic relocation table
  (535,826 entries, `.rela.dyn`) for any `R_AARCH64_RELATIVE`-style
  relocation whose resolved value equals `0x7105d91670`,
  `0x7105d916b0`, `0x7105d916b8`, or (for calibration) `0x7105d91bf0`.
  Checked two ways — the relocation's own cached value fields, and by
  reading the already-relocated bytes at each relocation's target address
  directly — both came back with 0 matches for all four addresses,
  including the calibration target. This is actually expected, not a
  calibration failure: the known `0x7105d91bf0` install is itself done in
  code (`ADRP`+`ADD` inside `FUN_710328b2a0`), not by a load-time
  relocation, so a relocation-table scan was never going to see it either;
  it was checked anyway to rule out the table being installed by a pure
  data-relocation with no owning function, which is now also excluded for
  the `0x7105d91670` target.

**Interpretation.** This upgrades the previous finding from "the ordinary
reference-manager search finds nothing" to "no standard AArch64 PIC address
construction idiom present anywhere in the binary's code section resolves to
an address inside this table, and no load-time relocation writes one there
either." The earlier hypothesis that this was simply an artifact of Ghidra's
reference manager not folding `ADRP`/`ADD` into a recognized xref is now
excluded — a byte-level scan that both idioms would have to pass through was
run directly and calibrated against a known-working case in the same binary.

What remains as an explanation is now narrower and points toward something
that cannot be resolved with static disassembly of this binary alone:

- a multi-level pointer indirection where the table's address is computed
  at runtime from values not present in `.text` or `.rela.dyn` as a fixed
  literal — e.g. assembled from a runtime-relative offset added to another
  already-installed vtable pointer, or produced by a generic/templated
  install helper that receives the table pointer as a parameter from a
  caller whose own reference this scan should still have caught (this is
  the least likely surviving explanation, since the scan covers all of
  `.text` and any such helper's caller would itself need one of the three
  idioms checked);
  or
- the table is not reached by computing its base address as a discrete
  value at all — for instance, if it is walked purely by index arithmetic
  from a *different*, already-referenced anchor at a fixed byte offset
  (multiple-inheritance-style secondary vtable layout, where only the
  primary vtable's address is ever materialized in code and this table is
  reached as `primary_vtable_ptr + constant_offset` without the constant
  `0x7105d91670` ever appearing as a literal). This was not directly ruled
  out by this pass and is the most concrete remaining static-analysis
  lead: it would mean searching for a *different*, already-referenced
  vtable base (there are several nearby, per the windowed scan above) and
  checking whether `base + offset` lands on `0x7105d91670` for some
  offset consistent with how the two known fetch-callback slots
  (`+0x40`/`+0x48` from `0x7105d91670`) are actually invoked at their call
  sites (i.e., trace `FUN_71032869c0`'s `+0x40` virtual call, mentioned in
  the prior pass, back to whatever pointer it dereferences through, rather
  than to this table's base specifically).

Neither of these can be resolved further by literal-address scanning; the
next static step, if pursued, is the offset-relative-vtable trace described
above (follow the *pointer register* used at `FUN_71032869c0`'s `+0x40`
call site back through its defining instructions, rather than searching for
the table's own base address). Beyond that, this specific question — what
object installs/holds this table — most likely needs a runtime trace, which
remains out of scope for static analysis.

This does not change the standing conclusion that the missing early
`GameRecord` batch is a client-side call gate rather than a wire-shape
issue; it only narrows *how* that gate is reached without resolving it. See
the updated interpretation below for what this means given the new
hardware-testing context.

### Evidence files, this pass

- `/tmp/ScanLiteralImm.java` — the calibrated `.text` literal-immediate
  scanner (`ADRP`+`ADD`, `MOVZ`+`MOVK`, and direct-operand idioms), plus a
  windowed near-miss scan.
- `/tmp/ghidra-s3-scan-literal.log` — its output: 2/2 calibration hits on
  `0x7105d91bf0`, 0 hits on `0x7105d91670`/`0x7105d916b0`/`0x7105d916b8`
  across all idioms, 60 non-overlapping nearby hits.
- `/tmp/ScanRelocs.java`, `/tmp/ScanRelocs2.java` — `.rela.dyn` scans (cached
  relocation values, and resolved-memory read) for the same four addresses;
  `/tmp/ghidra-s3-relocs.log`, `/tmp/ghidra-s3-relocs2.log` — both 0 hits,
  535,826 relocations scanned each way.
- `/tmp/DumpInsns.java`, `/tmp/ghidra-s3-dumpinsns.log` — disassembly used to
  confirm the exact `ADRP`+`ADD` idiom and instruction encoding at the known
  `0x7105d91bf0` install site, used to design and validate the scanner.
- `/tmp/DebugOps.java`, `/tmp/ghidra-s3-debugops.log` — operand-object
  introspection that caught and fixed an early scanner bug (Ghidra represents
  an `ADRP` page operand as a `Scalar`, not an `Address`; the first scan
  attempt silently found nothing, including on the calibration target,
  until this was fixed).
- `/tmp/ListBlocks.java`, `/tmp/ghidra-s3-blocks.log` — confirmed `.text` is
  the only executable memory block in the binary, so no code region was
  missed.

## Current interpretation and open work (updated 2026-09-25)

The strongest supported conclusion is unchanged and now more strongly
supported: the missing early `GameRecord` batch — and, per the new hardware
context, also the missing `SelectFestSchedule` call and the missing
`CreateSaveRecord` call — is consistent with a client-side call gate the
console fails to pass, not a server-side wire-shape problem. Nothing found in
this pass or the previous one identifies a SwitchNet response field as a
producer of that gate.

Updated status of the original open items:

1. **Still unresolved, but the search space has narrowed.** An exhaustive,
   calibrated scan of every `ADRP`+`ADD`, `MOVZ`+`MOVK`, and direct-operand
   idiom in the whole `.text` section, plus the full `.rela.dyn` relocation
   table, found no reference anywhere to the `0x7105d91670` table's base
   address or to its two known fetch-callback slots. This rules out both
   "Ghidra just isn't showing the xref" (the original hypothesis) and "it's
   installed by a load-time relocation with no owning function." What is
   left is that the table is reached through offset arithmetic from a
   different, already-referenced vtable/pointer (multiple-inheritance-style
   layout) rather than through its own literal base address, or through an
   indirection this scan cannot represent as a fixed value. The concrete
   next static step is tracing the pointer register at `FUN_71032869c0`'s
   `+0x40` virtual call site back to its defining instructions, rather than
   searching for this table's base address.
2. Unchanged: still open. What ticks `FUN_710328bcc0` (the registered
   `GameRecord` component's update method) and how it schedules child
   acquisitions is not yet traced.
3. Unchanged: still open. The producer of `+0x110 >= 8` before the VS/Coop
   marker is written is not yet identified.
4. Unchanged: still open, but now with a stronger reason to care about the
   answer — this is no longer just about the early `GameRecord` document
   batch. The 2026-09-25 hardware retest shows the *same* client-side skip
   pattern recurs across three separate RPC groups (`SelectFestSchedule`,
   the `GameRecord`/`Ugcstore` batch, and `CreateSaveRecord`), all of which
   sit downstream of whatever readiness/gate mechanism this table and its
   sibling `GameRecord` component participate in, and the client then
   restarts its whole NPLN session every 70-90 seconds instead of
   proceeding or erroring visibly. That repeating full-session restart, on
   a freshly created account with no prior local state, is itself new
   evidence against a local-save-state explanation and for some other
   condition the client is waiting on and never receiving — findings 2 and
   3 above (what ticks the update, what sets the readiness fields) are now
   the more consequential open questions than finding 1, since they are
   closer to whatever the client is actually blocked on.
5. Unchanged: still open. SwitchNet logs show no early reads and no calls
   to the three newly-confirmed-missing RPCs, but a skipped local callback
   still cannot be observed server-side by construction.

**Net assessment for SwitchNet's purposes, updated with the new hardware
context**: this pass makes it *more*, not less, likely that the root cause is
a client-local state machine SwitchNet has not yet found the right trigger
for, rather than something fixable by changing a response shape. The
strongest new evidence is the hardware retest itself, not this RE pass: the
symptom is now confirmed deterministic across a completely fresh account
(ruling out local save corruption), consistent across every server RPC
returning OK (ruling out a server-side error response), and consistent with
the two known-working reference captures only up to the exact point where
those captures still proceed into schedule/fest/GameRecord calls that this
console's client never makes — i.e., a real, working NPLN server received the
*same class* of responses and the client proceeded, while SwitchNet's client
does not, for a still-unidentified reason. Combined with this pass's finding
that the client-side gate is not even reached through an ordinary vtable
install this binary's disassembly exposes, the picture is of a client-side
readiness condition — plausibly timing, an earlier response's exact field
value, or a completion signal from a different subsystem entirely — that has
not yet been identified, rather than a shape SwitchNet is getting wrong on
these three RPCs specifically. Nothing in this pass points at which field.
This should not be treated as ruling out a fixable SwitchNet-side cause
entirely — three RPC groups sharing one symptom is also consistent with one
upstream response (schedules, or an earlier `GetSaveRecord`) silently failing
a check the client makes before continuing — but the specific field or
condition remains unidentified, and resolving it further likely needs either
the offset-relative-vtable trace above, or a runtime trace/debugger session
against the live console, which is out of scope for this static pass.

## Follow-up: FUN_71032869c0 ruled out as the table's owner (2026-09-25)

`FUN_71032869c0` was flagged in the section above as the one manager method
seen making a virtual call at `+0x40` before resetting parent state, with its
exact vtable owner still unmatched to `0x7105d91670`. Traced by hand this
pass, reading its existing decompilation and its callee `FUN_710328b2a0` (the
`GameRecord` manager's own constructor, already known from earlier passes).

`FUN_71032869c0(long *param_1)` calls `(**(code **)(*param_1 + 0x40))()`
(the generic "already reset?" pattern seen elsewhere in this codebase), then
resets a **pool-allocated** sub-object via `FUN_710327dd74`/`FUN_7103279e6c`
on `param_1[0x27]` (offset `+0x138`). That sub-object's vtable is
`&PTR_LAB_7105d91e08` -- a THIRD vtable, distinct from both the VS/Coop fetch
table (`0x7105d91670`) and the `GameRecord` manager's own table
(`0x7105d91bf0`). `FUN_710328b2a0` builds this as a small freelist of
fixed-size (`0x44`-byte) worker objects, each tagged by a type-id field
(values 1/2/3/4 seen assigned across the constructor's repeated blocks) --
consistent with it being a generic acquisition-slot pool for `GameRecord`'s
several children (`PointCardBigRun`, `BankaraChallenges`, etc.), not the
specific VS/Coop fetch object.

**Conclusion: this candidate is ruled out.** `FUN_71032869c0` does not
install or call through `0x7105d91670`; it manages an unrelated pool. Given
the earlier calibrated disassembly-immediate scan already searched the whole
`.text` section for literal references to `0x7105d91670` and found zero
(while correctly finding the two known references to `0x7105d91bf0`,
confirming the technique itself works), there is no remaining static lead in
hand to chase for who installs the VS/Coop table. The two honest options
left are: a computed-offset-from-a-different-vtable-pointer layout (would
need matching pointer arithmetic across many more candidate call sites, a
much larger search than any pass so far has attempted), or a live

## Follow-up: a second, independent vtable hits the same wall (2026-09-25)

A separate investigative path -- starting from `FestService`'s RPC method-name
string references rather than the `GameRecord` fetch table -- was followed by
hand, independently of the disassembly-scan tooling above, to see whether it
would surface a different, more tractable lead.

Traced upward from the `SelectFestSchedule` method-name string through the
generated `ServiceClient<FestServiceServiceTraits>` machinery
(`FUN_71008a4a70` the RPC-stub-table constructor, `FUN_71008a4a20` its
allocator wrapper, `FUN_7100735b90` the templated `nn::npln::ServiceClient`
constructor itself -- confirmed by a demangled string literal embedded in its
own `__cxa_guard`-protected static-init block,
`"static const char *nn::npln::ServiceClient<...FestServiceServiceTraits>::GetNameForExecutor()"`
-- then `FUN_7100714780`, another layer of generic future/promise-style
object wiring) up to a real, non-generic, application-level function:

`FUN_7103270050(param_1, param_2)`: a lazy accessor with the shape
`if (!already_attempted_flag) { reset_large_state_block(param_1); if
(*param_2 != 0) { construct_and_cache_FestService_client(); attempted_flag =
1; } }`. The `already_attempted_flag` lives at `param_1+0x74`; the cached
client pointer at `param_1+8`. `reset_large_state_block`
(`FUN_7103270140`) clears a large span of fields on `param_1` (roughly
`0x74` through `0x1618`), including offset `+0x138` -- the same offset
identified in the section above as holding the pool-object pointer
`FUN_71032869c0` resets. This is consistent with `param_1` being a shared
top-level session/lobby state object that owns both the `GameRecord`
acquisition pool AND the lazily-constructed `FestService` client as sibling
fields, not two unrelated objects.

The actual call gate turned out to be a TINY (2-instruction) wrapper at
`0x7103270044`, not analyzed as its own function until manually created in
this pass (`ghidraScript.createFunction`): `if (*(param_1+8) == 0) call
FUN_7103270050(param_1, param_2); else return (already cached).` This
resolved cleanly to a caller: **a vtable slot**, `0x7105d91038` -- i.e. this
Fest-client accessor is itself a virtual method. The owning vtable's base is
`0x7105d91000`, which is only `0x670` bytes before the still-unresolved
VS/Coop fetch table (`0x7105d91670`) -- almost certainly the same contiguous
per-class-vtable data region the earlier passes were already working in,
not a coincidence of address proximity.

The same reference search that found the two real constructor references to
`0x7105d91bf0` (the `GameRecord` manager's own table) was run against this
new vtable's base (`0x7105d91000`, and neighboring slot addresses
`0x7105d91028`/`0x7105d91030`/`0x7105d91038`) and again found **zero**
ordinary code references, on a *second, independently-discovered* vtable in
this same cluster. Two unrelated investigative paths landing on the
identical "no reference to this vtable's own address anywhere" result is a
real pattern, not coincidence, and it sharpens the leading theory: the
compiler most likely materializes **one shared `ADRP`+`ADD` to this data
section's base** (around `0x7105d91000`, possibly earlier/page-aligned) into
a register a single time, then reaches every individual vtable in the
cluster via a small immediate offset add from that cached register --
meaning no single vtable's full 64-bit address is ever constructed as a
literal anywhere in the binary, which is exactly what would make every
by-target literal-immediate scan come back empty regardless of which vtable
in this region is the target.

**This is now the concrete next static step, sharper than before**: instead
of scanning for references to each *target* vtable's own address, scan for
`ADRP`+`ADD` pairs whose *materialized value lands anywhere in this data
region* (e.g. `0x7105d91000` through at least `0x7105d91670`, likely a
larger page-aligned span), then, for each such site, trace the small
follow-on offset adds applied to that cached base register forward through
the function to see which specific vtable (by exact offset from the
section base) each use site actually reaches. This is a substantially
larger search than any single-target scan attempted so far -- realistically
another dedicated pass, not a quick follow-up -- but it is now a
well-reasoned technique with two independent data points supporting it,
not a guess.

## RESOLVED: the VS/Coop fetch table's real base, and why every prior scan missed it (2026-09-25)

The page-wide scan proposed just above was run (`ScanSectionBase.java`,
range `[0x7105d91000, 0x7105d92000)`, all 15,130,988 `.text` instructions):
**228 hits**, at 94 distinct addresses, scattered across the whole page --
confirming the "one region, many vtables, each reached from its own
`ADRP`+`ADD`" picture. Critically, `0x7105d91670` (the address every prior
pass treated as the VS/Coop fetch table's base) is **not** among them. The
hits cluster tightly except for one clean gap: the closest hit below
`0x7105d91670` is `0x7105d91608` (delta `0x68`), and the closest above is
`0x7105d917a0` (delta `0x130`) -- a 408-byte span with a single address
materialized at its very start and nothing in between.

`0x7105d91608` was traced to its one reference, `FUN_7103287fa0`, which
opens with `*param_1 = &PTR_FUN_7105d91608;` immediately followed by a long
chain of reference-counted child-object teardown (`ExclusiveMonitorPass`
refcount decrements, conditional virtual-dtor calls through `+0x10`) --
the standard C++ pattern of resetting an object's own vtable pointer before
tearing down its owned children, i.e. **this is the class's destructor**,
and `0x7105d91608` is unambiguously this class's real, complete vtable base
(a constructor for a class with no polymorphic base installs the identical
pointer; the destructor is sufficient proof on its own). Its own caller,
`FUN_7103288390`, is a generic "run destructor then free through the
custom allocator" wrapper -- ordinary delete-this boilerplate, not
app-specific, and not needed to establish the base address.

**This means every address this whole investigation has been calling "the
VS/Coop fetch table" (`0x7105d91670`) was never the table's base at all --
it is member offset `+0x68` inside the real table, whose base is
`0x7105d91608`.** The VS fetch callback (`FUN_7103283740`, previously
described as vtable slot `+0x40` relative to the assumed base `0x7105d91670`)
is really at slot `0x68 + 0x40 = 0xa8` from the true base; the Coop fetch
callback (`FUN_7103283d20`, previously `+0x48`) is really at `0x68 + 0x48 =
0xb0`. This is exactly why the disassembly-immediate scan (an earlier pass)
and the two targeted reference searches against `0x7105d91670` all
correctly found zero hits: nothing in the binary ever needed to construct
`0x7105d91670` as a value, because no code addresses the VS/Coop callbacks
by that address -- they are always reached as `vtable_base_reg + 0xa8` /
`+ 0xb0` from whatever already holds `0x7105d91608`, a fixed-immediate-offset
`LDR`, not a fresh address computation. Every previous pass's "no reference
found" result was a true negative on a wrong target, not a tooling failure.

**Original open question 1 ("which function's object uses the method table
... to invoke slots ... and under what lifecycle event") is now answered at
the vtable-identity level**: the table is a real, single, ~0x200+-byte
class vtable at `0x7105d91608`, confirmed via its own destructor. What
still hasn't been chased down in this pass -- the next concrete step, and a
much more tractable one now that the correct base is known -- is finding
where an object of *this* class gets its `vptr` loaded into a register and
then dispatched through offset `0xa8`/`0xb0` specifically (an ordinary
virtual-call site, not an address-hunting problem anymore): search for
`LDR`-based virtual calls of the form `(**(code**)(*obj + 0xa8))(...)` /
`+ 0xb0` against the correct base, the same way `FUN_71032869c0`'s `+0x40`
call was already found and read in the section above -- that search was
never run because every prior pass anchored on the wrong base offset
(`+0x40`/`+0x48` from `0x7105d91670`) rather than the real one (`+0xa8`/
`+0xb0` from `0x7105d91608`).
