# internal/toyohr

Splatoon 3's own NPLN services (`nn.npln.toyohr.v1.*`). Splatfests first,
because custom Splatfests are what phase 2 exists for.

| | |
| --- | --- |
| Reimplements | `nn.npln.toyohr.v1.FestService` |
| Reference | [NPLN Servers](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers) |
| Definitions | [kinnay/NPLN-Protocols](https://github.com/kinnay/NPLN-Protocols) `latest/proto/toyohr/v1/fest.proto` |
| Needs a database | yes |

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

## The vote is measured; the battle ratios are not

`GetFestResult` fills four sections, and they are not equally real.

**Yobisai — the pre-fest vote — is measured.** Every `CreateFestEntry` is a
player who picked a side, and the ratio of entries per team is a number this
server actually has.

**The Honsai midterm and final ratios are not.** A real fest result carries
per-category ratios derived from battles played, and nothing in SwitchNet
records a battle yet — `GameRecord` is unimplemented. Those are returned with
`is_valid` false, which is the protocol's own way of saying a section is
unavailable.

They are **not** filled with a plausible number, and that is the point: a
fabricated ratio is indistinguishable from a measured one to whoever reads it
next, including a later version of this code.

**So the verdict is decided on the vote.** One point per entry, most points
wins. That is an *editorial choice*, not a stand-in — this is one operator's
server, a fest here is a fest SwitchNet built, and "the side more people picked
wins" is a rule that can be stated to the players. `overallResult` is the single
function that changes when battles start being recorded.

The verdict is withheld until play is over. Publishing one mid-fest would have
the game announce a winner while the fest is still open.

## Team choice locks when play opens

Before a fest starts, calling `CreateFestEntry` again changes the team — that is
a player changing their mind in the lobby. Once play opens:

- **a player who already picked gets their existing entry back**, not an error.
  A console re-sending its choice on reconnect is normal traffic, and an error
  there reads to the player as the fest being broken.
- **a player who never picked is refused.** Splatoon 3 makes players choose
  before a fest opens, and a vote arriving after a ratio was published changes a
  number the players have already seen.

A fest that is disabled, or not yet announced, does not exist to a console at
all — `NotFound`, not a more specific refusal.

## The player comes from the token, never from the request

`FestEntry` carries fields the client fills in. The player is taken from the
authenticated session instead, through the `CallerID` function
`internal/npln` hands in — it cannot be an import, because `npln` registers this
service and the cycle would not build.

`CallerID` nil is legitimate and **fail-closed**: the dashboard builds a
`Service` to schedule fests and never serves an RPC through it, so a
player-scoped handler reached that way refuses rather than guessing. Guessing is
the bug this repository is shaped around.

## Overlapping fests

Two fests live at once is a state the game has no reading for. The operator can
create it by mistake, so `currentFest` picks the most recently started and logs
a warning rather than picking arbitrarily.
