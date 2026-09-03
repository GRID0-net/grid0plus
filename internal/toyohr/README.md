# internal/toyohr

Splatoon 3's own NPLN services (`nn.npln.toyohr.v1.*`). Splatfests first,
because custom Splatfests are what phase 2 exists for.

| | |
| --- | --- |
| Reimplements | `nn.npln.toyohr.v1.FestService`, `Schedule`, `CloudSave.GetSaveRecord` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) `latest/proto/toyohr/v1/{fest,schedule,cloud_save}.proto` |
| Needs a database | yes, for fests -- `Schedule` and `GetSaveRecord` need none |

## A Splatfest lives in two places, and they must agree

This is the thing to understand before changing anything here.

```
NPLN schedule  (this package)   names a fest: its id, its pack, its revision
BCAT pack      (internal/bcat)  carries the CONTENT, and names the fest it is for
```

`SelectFestSchedule` carries **no content at all** — no team names, no banners,
no idol dialogue. Only mechanics: which pack, which revision of it, the five
phase times, and the publication window. Everything a player actually sees is
inside the BCAT pack the console fetches separately.

So the fest id, the resource name and the revision announced here must match
what the console's BCAT cache holds. When they disagree the game asks BCAT for a
resource digest it cannot find and reports **`BcatInvalid`**, which reaches the
player as a generic communication error.

## Three details that each cost somebody an evening

**Timestamps must land on two-hour boundaries.** Splatoon 3 files scheduled
things into 7200-second slots. A fest whose phases are not slot-aligned belongs
to no slot: the game stops treating it as open and starts demanding its
*verdict*, which a real console never does mid-fest. Every player's lobby fails
at once with nothing in any log.

Every phase offset is a multiple of the slot length, so aligning the start
aligns the whole table. `NewFest` aligns; `Validate` refuses — a caller asking
for "next Saturday" wants a fest, but an operator who typed a time should be
told it moved rather than have it happen quietly.

**The fest id is uppercase and the resource name is lowercase**, and they fail
differently. A lowercase id is simply a *different fest* to the game. A
capitalised resource name is worse: the console derives the pack's AES
decryption IV from that string, so one wrong capital means the pack never opens
and nothing in the failure points back at the name.

**A key in the wrong slot is silent.** `GetFestDecryptionKey` returns keys
positionally — notice, start, result, then the three per-team keys. A
misordered key is handed to the console for the wrong phase and the pack fails
to open, with nothing saying which phase was wrong.

## Seven key slots, not three

The message declares seven key fields and the request carries a `show_all_keys`
flag. A reference implementation this protocol knowledge was checked against
caps its response at three, on the grounds that the game never asks for more —
which is plausibly true of the phases it was tested against and is not a
property of the protocol. All seven are filled when supplied.

A key that was **not** supplied is left absent rather than empty. Nintendo omits
the key for a phase that has not happened yet, and an empty string is a key the
console would try to use.

## The keys are ours

Nothing here decrypts anything of Nintendo's. A fest SwitchNet serves is a fest
SwitchNet built: its pack was encrypted with keys stored alongside it, and
`GetFestDecryptionKey` hands back those same keys. That is what makes a genuinely
custom Splatfest possible rather than a rename of one of Nintendo's — both
inputs to the pack encryption, the key and the resource name, are the server's
to choose.

## Team choice is permanent, and results are honestly incomplete

`CreateFestEntry` records a caller's team for a fest -- self-scoped, the same
discipline `internal/baas`'s `requireSelf` applies to every user-scoped HTTP
endpoint: `GetFestEntry` refuses to read another account's choice
(`PermissionDenied`), matching real Splatfest rules that a pick cannot be
seen or changed by anyone else before results are public. A second
`CreateFestEntry` for the same fest and caller is `AlreadyExists`, not a
silent overwrite -- Splatfest team choice does not have an "undo" in the
real game, and letting a retried call quietly replace an earlier pick would
be inventing a feature Nintendo's own version does not have.

`GetFestResult` is **honestly incomplete, not `Unimplemented`**. Every
sub-result (`Yobisai`, `HonsaiMidterm`, `HonsaiFinal`, `Overall`) comes back
with `IsValid: false` rather than a computed ratio or point total, because
SwitchNet has nothing to compute one from: `npln_fest_entries` records who
picked which team, not who won a battle, and `GameRecord` -- the RPC that
would report battle outcomes -- is not implemented (see `TODO.md` §1.4).
Deriving a ratio from entry counts alone was considered and rejected: a
lopsided sign-up split is not the same thing as a lopsided battle result,
and showing one dressed as the other would be actively misleading rather
than merely incomplete. `IsValid` exists in the proto specifically to say
"not computed yet," so using it is the defensible reading rather than an
invented number -- the same principle `Schedule`'s honest emptiness follows,
just for a per-fest resource instead of a boot-time list. See the
`TODO(capture-needed)` on `GetFestResult` for what a capture would need to
settle before this can report anything else.

## Overlapping fests

Two fests live at once is a state the game has no reading for. The operator can
create it by mistake, so `currentFest` picks the most recently started and logs
a warning rather than picking arbitrarily.

## `Schedule`: honest emptiness, not an invented rotation

`schedule.go` answers all five `Schedule` RPCs with an empty schedule list
rather than `Unimplemented`, because the console calls these early in boot
and the alternative is a to-do-list log line where a working boot query
should be.

The content those RPCs would normally carry — which stages, which mode,
which weapons are in Salmon Run's rotation — is Nintendo's own game design
data. Unlike a fest, which the operator authors and `SaveFest` validates,
there is no equivalent authoring path for a normal Vs/Coop rotation and no
dump of one. CLAUDE.md is explicit that a genuinely undocumented shape must
not be filled with an invented-but-plausible Nintendo internal, so this
stays honestly empty (`TODO(capture-needed)`, in `schedule.go`) rather than
serving a fabricated rotation nothing here would ever catch as wrong.

## `CloudSave.GetSaveRecord`: `NotFound` is the correct answer, not a stub

A cloud save is real player save state. SwitchNet has no legitimate way to
have one — the same "no game dump, no save data" boundary CLAUDE.md draws
for phase 3 applies here too. `GetSaveRecord` answers `NotFound` for every
account, which is not a placeholder: a fresh account has no cloud save on a
real server either, and the game already has to handle that case on its
very first boot.

The other five `CloudSave` RPCs are left to
`UnimplementedCloudSaveServer`. Nothing calls them this early, and actually
implementing `Write`/`Create`/`Delete` convincingly would mean this server
keeping opaque save blobs it has no way to validate — a materially
different scope than answering the one boot query that would otherwise
hang.
