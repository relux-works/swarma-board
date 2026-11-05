# Synthetic composition and production gates

The two binaries dock domain/query, server/store/auth, client/CLI and transfer.
Only explicit synthetic server fixture/listen/data-dir inputs are runnable in
this candidate. No production default adapter is installed. D6 in contract.md
specifies the approved production semantics and their remaining gates.

Build with Go 1.25.5, GOMAXPROCS=2 and package parallelism 2. Stamp an exact source
commit for qualification; unstamped builds identify themselves as dev:

```sh
GOTOOLCHAIN=go1.25.5 GOMAXPROCS=2 go build -mod=readonly -p 2 -ldflags "-X main.commit=$(git rev-parse HEAD)" -o /tmp/swarma-board ./cmd/swarma-board
GOTOOLCHAIN=go1.25.5 GOMAXPROCS=2 go build -mod=readonly -p 2 -ldflags "-X main.commit=$(git rev-parse HEAD)" -o /tmp/swarma-board-server ./cmd/swarma-board-server
```

Use a private empty test directory; synthetic fixtures provision missing empty
projects and preserve existing facts on restart. Start the server separately:

```sh
/tmp/swarma-board-server --synthetic-fixture internal/testadapter/testdata/server.json --listen 127.0.0.1:17994 --data-dir /absolute/private/synthetic-data
```

Supply a projection JSON with exactly endpoint, project_id and account_id. The
shared fixture uses synthetic-board and synthetic-admin/viewer/editor accounts.
The corresponding PUBLIC test bearer values are documented in the fixture
README. Open an explicit fd containing the chosen test value and pass it with
`--synthetic-projection FILE --synthetic-credential-fd N` on every CLI invocation;
the CLI consumes/closes that fd once. No env/config credential discovery exists.
Use `import FILE --dry-run`, inspect the digest/exclusions, then explicitly import
with `--sha256 DIGEST` into the empty target. Production snapshot custody and
cutover authorization are not supplied by these examples.

Server handles SIGINT/SIGTERM by stopping admission, draining HTTP and then
closing the project registry. CLI has q/m/plan/import/export/version and standard
help; it has no lifecycle or local-store command. If service is unavailable,
requests fail with nonzero transport_error. Host/launcher must recover service;
the CLI cannot launch it. A disconnected write requires read/reconcile.

The integration tests exercise concrete component imports, revision/grant
checks, DAG/plan results over synthetic HTTP and verified test TLS, native fact
roundtrip, restart, backup/restore and CLI preflight/refusal. Producer suites
retain their parser/graph/limits/fault/kill witnesses. These are producer checks,
not independent Muse review or production UDS/signer qualification. No live
board, native payload, owner credential, provider or external service is needed.

Remaining D6 work: server peer identity and coherent bounded journal export;
client Unix credential bypass and Host recovery wording; approved schema-v2
journal disposition; Host runtime/protected mapping/distinct UID evidence; U1
adapter pin; remote TLS/auth custody; external signer trust/custody/format;
source notification and sanitized snapshot/cutover/reconciliation authority.
Rollback exports a fresh checkpoint read-only and keeps the stale source writer
disabled. Bot landing signatures are unrelated to audit-export signing custody.
