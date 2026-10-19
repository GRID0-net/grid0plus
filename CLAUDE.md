# Notes for working on this repository

Start with [CONTRIBUTING.md](CONTRIBUTING.md) for how to build, test and commit,
and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how the pieces fit. This file
is the short list of things that are easy to get wrong.

## The rules

1. Never commit a Nintendo key, certificate, ticket or dumped file, in any form.
   If a task seems to need one, say so instead of finding a way.
2. Never verify a Nintendo-issued credential and never forge one. Device
   certificates, tickets and gamecard challenges are accepted unchecked on purpose.
3. Identity fails closed. If a lookup can't resolve the caller, return an error.
   Don't add a fallback.
4. Run it, don't just test it: `make up`, `make run`, `make seed`.
5. If a Nintendo request or response shape is undocumented, implement the most
   defensible reading and mark it `UNCONFIRMED:` with what would settle it. Don't
   invent something that merely looks official.
6. One logical change per commit, with a conventional prefix and a body that says
   why.

## Two bugs the design is built around

Both were seen on real hardware against the server GRID0+ replaces.

**Every console acted as one person.** That server fell back to "the last account
that authenticated" whenever a device account lookup missed, and a console
presents a cached device account forever, so misses are normal. The defences are
`authenticate` in `internal/baas/router.go`, `completeLogin` in
`internal/baas/accounts.go`, `requireSelf` on every user-scoped endpoint and the
self-request guard in `createFriendRequest`. The tests that hold them are
`TestLoginRefusesAnUnknownDeviceAccount`,
`TestATokenWhoseSubjectDoesNotOwnItsDeviceAccountIsRefused`,
`TestOneUserCannotReadOrChangeAnother` and
`TestAnExistingTokenStopsWorkingWhenItsDeviceAccountIsDeleted`. If you are
weakening one, you are reintroducing the bug.

**Friends were never online.** Nothing reported a console that was simply on, and a
reported state never expired. `resolvePresence` in `internal/baas/presence.go`
takes a fresh report if there is one, otherwise treats a recently seen console as
online with the app fields cleared, otherwise offline. Never reported is not the
same as reported offline (`reported_at` is NULL until the console reports), and the
app fields must be cleared whenever the state isn't playing.

## Things that waste time

- **All services must share one `keys.dir`.** If they don't, nothing errors; every
  request is just rejected as expired. Key generation writes a temp file and links
  it into place so several services starting at once converge on one key. Don't
  replace that with a plain `os.WriteFile` or an `O_EXCL` create.
- **`nncs1` and `nncs2` must be different IP addresses.** Otherwise the console
  de-duplicates the NAT probe, detection never finishes, and matches won't start.
  The config validator refuses to let them match.
- **Leave the system update hosts (`sun`, `atumn`, `aqua`) null-routed** as
  `internal/nintendo/hosts.go` has them, unless you reuse the host list for a
  console that moves back and forth between GRID0+ and Nintendo. In that case make
  them pass through again first.
- **Redirection and certificate trust are separate problems.** DNS redirection
  happens off the console. Trust has to happen on it. "Nothing in any log" is
  almost always trust failing, because the connection never becomes a request.
- **Never forward AAAA records for a redirected name.** The generated resolver
  configuration answers NODATA so a console with IPv6 can't slip past to the real
  address.
- **Hostnames live only in `internal/nintendo/hosts.go`.**
- **Errors the console reads are keyed on `errorCode`, not the HTTP status.** Only
  define errors in `internal/baas/errors.go` that GRID0+ can actually produce.
- **Assert an absence only when a real capture shows it absent.** A wrong asserted
  absence is a test actively holding a bug in place.
- **Never let Splatoon 3's fest schedule go empty, and keep a fest's BCAT pack served while it is
  announced.** The game has no "no fest" answer: an empty `SelectFestSchedule` leaves its fest with an
  all-zero timetable, which it treats as Invalid, and every big screen in the lobby (each sits on a fest
  layer) goes blank for every player. A fest whose results are out is "no fest" to the game, so
  `lastClosedFest` keeps announcing the most recent closed fest until a newer one exists. When fests are
  disabled or deleted, a closed one must remain, with its seed in `data/bcat-native/0100c2500fc20000`;
  without the pack the console reports BcatInvalid. Deleting a fest also strands saves on it:
  `dropDeletedFest` cleans those up. Don't tidy the fallback away, and don't empty `bcat-native`.
