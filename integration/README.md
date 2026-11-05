# Synthetic transport composition and production gates

The binaries dock the completed domain/query, server/store/auth, client/CLI and
transfer packages. All executable startup paths require explicit synthetic
fixtures. No production adapter or protected mapping policy is installed.

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
belongs to the independent maintenance producer and is not implemented here.
Independent Muse review/checks and authorized signed landing remain required;
Meta A/B main-author and task-specific Curator key holds remain unchanged.
