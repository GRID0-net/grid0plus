# Splatoon 3 server comparison, 2026-09-27

This compares GRID0+'s Splatoon 3 services with the locally cloned Nextendo
server and the two preserved, decrypted Nextendo hall captures under
`docs/CAPTURES/raw/`. Nextendo's source is used for observed behavior, not
copied implementation. The two hall captures end before a completed public
match; neither supplies a successful `TrackMatchmakingTicket/SUCCEEDED` frame.
Findings below distinguish a measured wire mismatch from a missing feature or
an untested hypothesis.

| Surface | Evidence and current GRID0+ behavior | Status |
| --- | --- | --- |
| NPLN auth and gateway | Both players obtain prearranged tokens, reach the hall, and create matchmaking tickets. The current failure is downstream. | No active blocker found in these calls. |
| Vermillion, Penne, BCAT | The game reaches the hall and receives schedules. The earlier routing and BCAT failures do not explain the current missing friends or two-player `SUCCEEDED` handoff. | Functional for the tested boot path; event delivery remains untested. |
| NPLN friends | The database contains the same reciprocal friendships read by BAAS and NPLN. Both working captures send complete `FriendUser` entries for their two friend accounts. GRID0+ sends complete entries for its two friends too. The captured entries omit `relationship`, whereas GRID0+ adds it; whether that extra field affects this client is not measured. | Friend identity/count verified; relationship field is a wire difference needing a targeted test, not a proven bug. |
| NPLN presence | Both working captures request `SubscribePresences` **without** an explicit `presences` selector. Previously GRID0+ interpreted that as an empty watch set, sending an empty page. Commit `f635059` enumerates visible friends, omits an empty page, and puts the captured-style complete-set resume token on presence pages. The capture's read mask is `name,state,unsubscribed,attributes`; GRID0+ also sends `last_online_time` when known, an untested extra field. | Confirmed empty-enumeration mismatch fixed and deployed; extra-field impact needs a client retest. |
| BAAS friend-applet presence | The live database showed a current `last_seen_at` but an hours-old `reported_at` for one player. GRID0+ returned liveness-derived `ONLINE` with the stale report timestamp. Commit `463e4c5` uses the last authenticated request as the effective `updatedAt` for that response. | Confirmed timestamp inconsistency fixed and deployed; needs a client retest. |
| Friend avatars | The live friend URLs point to valid JPEGs; an HTTPS request through the production edge returned `200 image/jpeg`. After the console's BAAS friend-list GET, the BAAS log recorded no image GET for those friends. The working BAAS capture uses extensionless image names while current uploaded images have `.jpg`; whether the friends applet rejects that URL shape or serves a cached picture is not established. | Request-path issue, not an image-storage 404; capture the applet's URL handling before changing filenames. |
| Versus/Coop schedules and no-fest response | The live client proceeds through schedule calls. GRID0+ now generates advancing rotations and answers no active fest with the empty response shown in both working hall captures. Active-fest BCAT/key behavior has not been tested in this audit. | No current hall blocker found; active-fest path remains unverified. |
| CloudSave | `GetSaveRecord` uses the current wire-specific stream behavior; `CreateSaveRecord` and `WriteSaveRecord` now persist account-owned data. The earlier "acknowledge and discard" mismatch is gone. | Covered for the observed boot flow; not a claim of complete Nintendo compatibility. |
| Ugcstore | Missing `GetDocument` uses the measured `NotFound` framing, and `CommitDocuments` stores named documents. `RunQuery` still returns one `read_time`-only message **even when matching documents are stored**. Nextendo enumerates stored documents under the requested parent, restricted to the requested collection, and uses the single `read_time` message only when that filtered result is empty. | Confirmed feature gap. Implement collection-filtered reads before matches write queryable results. Do not return unrelated attribute documents for a `VsResults` query; Nextendo measured a client crash from that. |
| GameRecord/results | GRID0+ handles `InitializeAttributes` and selected methods, but the post-match result/write path remains incomplete. Nextendo has a `VsResults` history path. | Known missing feature, not the reason two clients remain in matchmaking before a match. |
| Public matchmaking | At 20:20:57 UTC GRID0+ formed a two-player Salmon Run session and sent `SUCCEEDED` on both open Track streams. Neither client then called `GetGameSession` or Gamesync. Nextendo's source notes that advertising a partial roster as 4/4 did not produce a playable public match; it currently waits for the full nominal roster. | Current blocker. A server-only two-player match is not demonstrated. Compare a successful completed-match capture or trace the client's `SUCCEEDED` branch before inventing a roster patch. |
| GameSession/Gamesync | The configured public session address is `89.168.58.206:7575`, matching the working server's direct gamesync-port pattern. The clients did not reach this connection after the two-player ticket, so its document/watch behavior was not exercised by this test. Nextendo retires a player's previous Gamesync user session when a new `IssueToken` arrives; GRID0+ currently keeps old session handles until their streams close or expire. That can retain ghost members after a reconnect. | No conclusion on match startup from this attempt; stale-session cleanup is a separate correctness gap. |
| Canola, Locker, Replay, CoopScenario | GRID0+ answers the boot-critical subset and leaves several content operations unimplemented or empty. Nextendo also relies on captured content for parts of these services. Empty collections can be valid on a new server; no current request/log links one to the friend or matchmaking symptom. | Incomplete content surface; test when a specific feature reaches it. |

Two additional consistency points remain after the deployed fixes:

1. GRID0+'s open `SubscribePresences` stream pushes database/attribute bus changes,
   but its heartbeat tick does not re-evaluate a friend's effective TTL. Nextendo
   re-evaluates watched presence and sends changed states. A friend whose report
   expires without a bus event may therefore remain visibly online until the
   subscription is reopened.
2. Several package READMEs predate the measured match stream and Ugcstore
   behavior. In particular `internal/npln/README.md` still says Track closes
   at `SUCCEEDED` and names both players, while the code now keeps the stream
   open and gives each recipient only its own matched session.

The next high-value test for the current user-visible friend problems is to
restart Splatoon after `f635059`, open its friends view, then open the system
friends applet once. The NPLN and BAAS logs can distinguish a missing call from
a rejected response; an Ethernet capture is only needed if those logs show a
request but not how the client uses the returned fields.
