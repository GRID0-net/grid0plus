# Public lobby startup group (2026-10-14)

The operator reported three two-player Big Run attempts. Both accounts were
listed in every session, but only the first attempt launched both clients.
The next two launched Jade alone while doodlylizz returned to matchmaking.

## Production evidence

| Session | Host | Kickoff UTC | Profile submissions |
| --- | --- | --- | --- |
| `4b64a762-2328-4753-8199-b71451f59b03` | doodlylizz | 18:19:08.908 | both players, 18:19:09.005 / .061 |
| `b6b7cece-4b7f-4176-ad82-e72da9607d2f` | Jade | 18:21:57.399 | Jade only, 18:21:57.443 |
| `db00327c-2e49-496a-ad03-e6f945f0106f` | Jade | 18:24:54.389 | Jade only, 18:24:54.438 |

In the first failed attempt both clients authenticated, subscribed to the
same room, and allocated ICE servers before kickoff. Jade locked the lobby
at 18:21:55.646. The other client's stream closed at 18:22:13.532 and it
requested a new ticket at 18:22:14.669. Gamesync writes completed in roughly
46–1382 microseconds, without a server-side error in this interval. These logs
do not establish why that client's Pia connection failed to reach the profile
step; distance or NAT asymmetry remains unproven.

## Correction

The old startup projection released each client after its own profile. Public
matches now freeze the authenticated ticket roster at kickoff and keep status
`-1` until every member has submitted that battle's profile. The final profile
immediately republishes the verdict to all watchers. Reads, collection
snapshots and periodic refreshes use the same check. The existing per-client
early handshake and immutable shared verdict remain intact.

The barrier uses the actual group size, not `maxu=8`. It permits one-, two-,
four- and eight-player groups. A missing peer does not get removed to release
the host; the game's existing timeout remains the recovery mechanism.
Profiles must belong to the authenticated caller and game session.

For Again!, the next battle freezes the authenticated user sessions still
watching at kickoff. Players who left before that kickoff do not block it;
stream loss after kickoff cannot shrink it. Private rooms retain their prior
per-recipient behavior because their joinable roster is not a fixed ticket
roster.

The complete preconnected `__pus` roster still determines one host. Public
`__us` no longer supplies a synthetic connected entry for an unauthenticated
matched peer, extending the existing Tricolor behavior to Big Run and Versus.

TODO(capture-needed): the initial `__us` list's role in Big Run's early close
is inferred from the timeline and prior Tricolor evidence. Compare both host
directions on consoles to validate `matchedPlayerDocuments`. The group barrier
prevents the observed host-only release, but does not repair NAT/Pia failure.

## Validation

Local real-gRPC tests cover group sizes 1/2/4/8, a host-only request against a
larger authenticated roster, pending and ready watch/read/snapshot delivery,
immutable stored verdicts, roster retries, a missing/disconnected peer,
retained-lobby members, and atomic rejection of forged profile writes.
The Gamesync race suite, Gamesync/NPLN unit suites and package vet pass.
Real-console startup with this change is pending.

Logs now include `startup group frozen` and, after each profile,
`profiles_ready` / `players_expected`. In a two-player successful test both
profiles should reach 2/2 before either recipient receives status 0. A stalled
1/2 identifies an unresolved peer startup failure without calling it gameplay.
