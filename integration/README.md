# Synthetic transport composition and production gates

The binaries dock the completed domain/query, server/store/auth, client/CLI and
transfer packages. The legacy qualification startup paths require explicit synthetic fixtures.
The explicit stage1 profile described below binds the frozen static policy
adapter; no production policy is installed.

Build with Go 1.25.5, `GOMAXPROCS=2`, `go build -mod=readonly -p 2`, and stamp
`-ldflags "-X main.commit=EXACT_COMMIT"` for qualification. Unstamped builds say
`dev`. Test with `go test -mod=readonly -p 2 ./...` under the same environment.

For local qualification, create an empty private directory (0700) and start the
server explicitly. It refuses any existing socket path, including stale sockets:

```sh
swarma-board-server --synthetic-fixture internal/testadapter/testdata/server.json \
  --data-dir /absolute/private/synthetic-data \
  --unix-socket /absolute/private/board.sock \
  --synthetic-peer-account synthetic-admin --synthetic-peer-project synthetic-board
```

Supply projection JSON with exactly `endpoint`, `project_id`, `account_id`, using
`unix:///absolute/private/board.sock`, `synthetic-board`, `synthetic-admin`.
Invoke `swarma-board --synthetic-local-projection FILE q 'count()'` or the usual
q/m/plan/import/export/version commands. This path never resolves a credential
source or sends Bearer authorization. TCP fallback, discovery and startup are
absent. Mixed local/bearer/CA flags refuse.

`app.NewUnixServer` injects one `auth.NewPeerAuthorizer` into BOTH authentication
and role checks, and installs `httpapi.UnixConnContext`. The listener returns
actual accepted `*net.UnixConn` values for Linux SO_PEERCRED. Each protected
request rechecks mapping and current grants; missing kernel context, unknown or
revoked mapping and spoofed identity headers refuse. Readiness includes mapping.
`SyntheticPeerResolver` is an explicit fake granting exactly one account/project
admin for the current kernel UID. It does not use bearer fixture grants for
local role checks. Every agent sharing that UID has the same identity. These
witnesses prove protocol integration, never per-agent isolation or Host policy.

For separate verified TLS qualification, provide an ephemeral synthetic server
certificate/key and explicit literal-loopback address:

```sh
swarma-board-server --synthetic-fixture internal/testadapter/testdata/server.json \
  --data-dir /absolute/private/tls-data --listen 127.0.0.1:17994 \
  --tls-cert /absolute/private/test.crt --tls-key /absolute/private/test.key
```

Use an HTTPS projection and the existing explicit `--synthetic-projection FILE
--synthetic-credential-fd N` flags. `--synthetic-tls-ca FILE` supplies bounded
fixture CA roots. TLS chain and endpoint hostname verification stay enabled;
untrusted certificates and insecure verification options refuse. The server
uses a separate Bearer handler without the Unix kernel hook. Tests can compose
both handlers over one service. Remote production trust and credentials remain
owner decisions. Test keys have no relation to Git or export signing custody.

The original explicitly synthetic literal-loopback HTTP harness remains:
`--synthetic-fixture FILE --listen 127.0.0.1:17994 --data-dir DIR`. Direct client
or CLI option injection must now set `SyntheticHTTP: true` for that harness.
The preexisting paired synthetic projection/credential-fd flags opt in explicitly.
HTTP is refused by default. Public synthetic bearer values are in the fixture
README; no ambient credential discovery exists.

SIGINT/SIGTERM stops admission, drains requests, closes listeners and releases
registry ownership. Socket cleanup removes only the created inode. A live
replacement path is preserved. Missing/dead endpoints fail with exit 1 and safe
Host/launcher recovery guidance. The frozen client reports dial failure as
`unavailable`, other dropped requests as `transport_error`; unknown write
outcome requires read/reconcile and is never retried automatically. CLI never
spawns a service or edits a local board. Ordinary native export remains separate
from offline signed audit export.

The full synthetic suite retains graph/import/limits/CAS/fault/kill/restart and
backup/restore witnesses. New integration tests cover concrete Unix/TLS facts,
plans and CLI parity, shared-UID extraction, no credential calls, cross-transport
CAS, coherent current-v1 journal reads, keep-alive mapping/grant revocation,
readiness, spoof/missing-hook refusal, TLS trust refusal and shutdown/restart.

Production gates remain: distinct approved OS identities and protected mapping
lifecycle/socket policy; U1 repository pin and adapter; remote TLS/auth custody;
explicit persistent journal schema disposition; external signer identity/trust,
algorithm and custody; authorized sanitized data, source notification disposition
and cutover/reconciliation authority. Schema-v1 bytes are preserved; version,
project and digest-bearing persistence remain HELD. `internal/maintenance/**`
belongs to the independent maintenance producer and is consumed unchanged.
Independent review/checks and authorized signed landing remain required.
The authorship hold was removed; the task-specific source-custody gate and
external key-custody hold remain in effect.

### Offline maintenance command (unsigned synthetic qualification)

The explicit server-only entry dispatches before registry, fixture, auth,
listener or service initialization:

```sh
swarma-board-server maintenance-export \
  --database /absolute/private/source.db --project synthetic-board \
  --output /absolute/private-evidence/new-checkpoint --writer-stopped
```

The binary must be built with `-ldflags '-X main.commit=<full-40-hex-commit>'`.
There are no ambient database, project, signer or credential defaults. The Host
must already stop the writer and retain maintenance ownership excluding service
restart and source/output path replacement through publication. The flag is an
assertion of that ownership, not proof or an operation that stops the service.
The inherited read-only bbolt lock additionally refuses an active writer after
about two seconds; cancellation cannot shorten lock acquisition. No board CLI
fallback, HTTP route, stop, spawn, restore or resume operation is added.

`internal/maintenance` is consumed byte-for-byte from the maintenance producer's completed signed
`37d189daecdeb98c862925c44c02f4acdf82bc20`. It reads a single coherent schema-v1
ExportView and closes its read-only handle after encoding. App composition then
validates canonical manifest/journal bytes, native-v1 content and exact sizes and
SHA256 hashes. The source database is never created, initialized or migrated.
The native snapshot and full v1 audit journal are separate artifacts; v1 events
are not replayable and no historical change digest or v2 fields are invented.

Output requires an absolute new directory beneath an existing canonical private
0700 parent protected by the Host, including its ancestors. Publication stages
the exact three streams privately (0700 directory, 0600 exclusive regular files),
syncs files and directory, then uses Linux amd64 `renameat2(RENAME_NOREPLACE)`
relative to the retained parent descriptor and syncs the parent. Existing files,
directories and symlinks refuse; unsupported no-replace operations fail closed.
There is no overwrite-capable rename fallback. Other platforms are unqualified.
This filesystem code is a producer candidate pending independent review;
fault/power-loss recovery has not been qualified. Process death can leave private
`.maintenance-incomplete-*` staging; the Host owns inspection/retention. A parent
sync failure after rename returns failure even though a complete output may be
visible. Inspect it before retrying; no completion is reported for partial writes.

The manifest and completion message say **unsigned**. No signature is written,
signer selected, key read or approval inferred from a candidate's Git signature.
The CLI does not expose the package's external-signing seams until the owners
approve signer identity, trust, algorithm/format and custody policy. Artifact
hashes establish self-consistency only. The maintenance manifest binds snapshot
and journal; its own exact-byte SHA256 can be recorded separately in the review
packet. The source writer stays stopped after every exit.

`TestMaintenanceExecutableUnsignedReadOnlyReimportAndRefusal` builds and runs the
actual command over synthetic temporary state containing a post-checkpoint task
mutation. It verifies source bytes/inode/mtime, complete revision-2 journal and
manifest hashes, checks native reimport into a fresh synthetic target, and checks
busy-writer, missing assertion/source, uninitialized source, symlink, mixed flags,
wrong project and occupied output refusals. Native reimport starts a new local
revision-1 audit history; it never replays the exported journal or resumes the
source. Existing actual Unix/TLS integration tests continue unchanged. These
witnesses do not qualify a production source, signature, UID policy or cutover.
Artifact limits are inherited from the maintenance producer and frozen P2/P4; they do not provide a
total process-memory bound or a raw-state preallocation bound.


### Stage1 explicit single-trust-unit candidate

Both binaries accept `--deployment-profile=single-trust-unit --deployment-status`
and emit the same safe static JSON descriptor on stdout. Status opens no policy,
storage or listener and is not live readiness. `production` always refuses with
both O/S/C principals and UID-helper, and protected per-request policy publisher diagnostics. No admission override exists.

Server startup requires all of the following explicit inputs (absolute paths):

```sh
swarma-board-server --deployment-profile=single-trust-unit \
  --unix-socket=/absolute/run/board.sock --data-dir=/absolute/data \
  --policy-file=/absolute/policy/release.json --policy-version=release-pin \
  --audit-export-dir=/absolute/exports
```

The policy-version is a separately reviewed release pin, never inferred from
policy bytes. The exact schema and custody checks are documented in
`internal/auth/STATIC_POLICY.md`. Config validation precedes policy loading;
policy readiness/catalog checks precede registry creation. Data, runtime,
policy and export trees must be disjoint. The runtime directory must exist and
be private; the service refuses occupied sockets. No export directory is created
by startup, and no export operation or signer is selected by that flag.

The existing bounded Unix server uses the same static resolver for kernel UID
authentication, authorization and readiness. Its actual `/ready` and
`/v1/version` responses carry `Swarma-Board-Deployment` JSON, including readiness
503 failures. The shared UID has one account/grant set and no per-agent isolation.
Policy drift latches closed until an orchestrator-controlled stop and new
release startup. No policy refresh or client-controlled service recovery exists.
The catalog includes granted projects only; disabled/no-project startup refuses.

Client operations require `--deployment-profile=single-trust-unit
--deployment-projection=/absolute/projection.json`, followed by usual product
arguments. The bounded three-field projection uses the existing public parser;
its endpoint must be Unix and is resolved once. No credential source is consumed.
The projected account is a preflight expectation, not peer identity evidence.
Duplicate/empty/mixed synthetic flags and all dogfood remote activation inputs
refuse. Separate synthetic TLS qualification remains available and tested.

Maintenance remains a separate offline command with the Host stopped-writer
assertion and protected paths documented above. The deployment export directory
is a packaging separation declaration, not an automatic maintenance target.
The operator must explicitly choose the new checkpoint beneath a protected
external export parent; no path is inferred from service config. The binary
publishes only unsigned v1 artifacts. `maintenance-import` unconditionally
refuses before any storage or service setup. Native product import still uses
the existing explicit project authorization/transaction path; this task performs
no real import. The dedicated verifier/external signer seam is in
`internal/maintenance/sshsig.go`; no Keymaker/keyvault API or real key adapter
is supplied. Synthetic SSHSIG tests do not establish custody or production trust.

`integration/stage1_binary_acceptance.py` derives from the frozen deployment
handoff acceptance without modifying that original document. It adds the
required independent policy-version flag, dispatch negatives, an actual CLI
mutation, live-writer export refusal, latched policy drift/restoration, clean
drain, unsigned offline export of the same synthetic service database, exact
hash/source preservation, occupied-output/import refusal and dead-service
no-resurrection. Run it against both exact stamped final binaries with a
synthetic current-UID policy and a NEW evidence directory. Full Go/race/vet,
legacy Unix/TLS, native-v1 restore and dedicated SSHSIG tests remain required.
Packaging, installation and lifecycle belong to board service orchestrator.
Independent review/checks, Source landing and production custody/adoption
remain separate. O/S/C principals and UID-helper, and the protected per-request policy publisher do not block this synthetic integration packet.
