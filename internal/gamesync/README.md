# internal/gamesync

Implements `nn.npln.gamesync.v1.Gamesync`, the Firestore-shaped document/
watch service Splatoon 3 uses for lobby and match state. See
[Kinnay's NPLN Servers wiki page](https://github.com/kinnay/NintendoClients/wiki/NPLN-Servers)
for where Gamesync sits among NPLN's other services; there is no
Gamesync-specific documentation there or anywhere else this project found --
every RPC here is implemented straight against the generated proto
(`proto/gamesync/v1`), which pins the wire shapes down exactly, plus
`internal/gamesyncstore` for the document semantics a wire shape alone
doesn't answer.

## Where it's reached

`gamesync.npln.nintendo.net` is classified `ServiceNPLN` in
`internal/nintendo/hosts.go`, alongside the tenant gRPC host --
**not** a separate port. TODO.md originally described Gamesync as reachable
"on its own port (7575)", and `config.NPLN` briefly carried an unused
`gamesync_listen` setting reflecting that plan; neither the nginx generator
(`cmd/switchnetctl/main.go`) nor `docker-compose.yml` ever wired it to
anything; the actual routing that shipped groups it with the tenant channel
by SNI. That is the evidence this package's registration follows: `Service`
is registered on `internal/npln`'s own `*grpc.Server`
(`internal/npln/services.go`), the same one `internal/toyohr`'s services
are, and for the identical reason -- `internal/npln` registers this package,
so this package cannot import `internal/npln` back without a cycle, and reads
the caller's identity from `internal/rpcauth` instead. See
`docs/QUESTIONS.md` for what would change if a capture ever shows the
"own port" reading was right after all.

## Sessions

`IssueToken`/`RefreshToken` mint an opaque access/refresh token pair for the
already-authenticated NPLN caller (`internal/rpcauth`), the same way BAAS and
`nn.npln.auth.v1.Auth` mint their own tokens rather than verifying anything
Nintendo issued -- see CLAUDE.md's second ground rule. `IssueTokenRequest`'s
own `user_session`/`matchmaking_id_token` fields are not used for identity:
naming a user is not proving one, the same distinction
`nn.npln.auth.v1.Auth.IssueToken`'s own doc comment makes about its request's
`user` field.

The minted access token is not checked anywhere: every Gamesync RPC,
`IssueToken` included, is already behind `internal/npln`'s own NPLN access
token requirement for the whole channel, so nothing downstream depends on it.
It exists because `Token.access_token` is not optional in the wire shape.
If a capture ever shows the console threading this token back through gRPC
metadata on later calls, that is the point at which this package would start
verifying it.

## What's simplified, not guessed at

See `internal/gamesyncstore`'s README for transactions (atomic, not
isolated) and `FieldTransform`'s undocumented global-value operations
(answered as a no-op). Two more choices at this layer:

- `DefermentOperation` (on `WriteDocuments` and `CommitTransaction`) is
  accepted and logged, never executed -- nothing found documents what a
  "deferment" defers, or until when.
- `ListDocuments`'s `show_missing` is not honoured: it asks for placeholder
  entries for a document that has children but was never itself written, and
  this store has no notion of a document existing independently of having
  been written.

`CreateRound` is left entirely `Unimplemented`. Its own request carries a
`Round` message whose fields are named `Unk1` through `Unk4` in this
project's own proto reversal (`proto/gamesync/v1/resources.pb.go`) -- there
is nothing here to implement a defensible reading of.
