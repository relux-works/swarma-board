# Integrator handoff: single-trust-unit

This candidate adds only `internal/deployment/**` and this documentation. **The
binaries at transport base 352aedf5 are NOT wired for this profile.** No binary
behavior, static-policy adapter, production admission, signed landing, real
adoption or live activation is claimed here. Apply the constructor patch proposal
only after merging dev3's maintenance dispatch; bind dev8's independently reviewed
static policy adapter at the indicated seam. Do not replace either producer's work.

Authority: the saved `board-stage1-meta.jsonl` decision at 2026-11-05T16:12:33Z
and `board-stage1-ratification.jsonl` at 16:18:19Z ratify first dogfood under the
shared agent UID, no per-agent isolation, UDS only, static owner-private 0600
release policy, and agents2 board orchestrator lifecycle ownership. A2-035
(O/S/C principals + UID-helper) and A2-036 (protected per-request publisher)
block production. This implementation has no mechanism to mark them satisfied.
A later independently reviewed implementation and owner admission are required;
adding a production enum or setting booleans does not discharge those gates.

## Exact command contract proposed for dev3/integrator

Add flags to the app entry dispatch, leaving product query syntax and the
existing explicit synthetic TLS qualification path intact:

| Input | Mapping and validation |
| --- | --- |
| `--deployment-profile=single-trust-unit` | Required for dogfood server/client operations; exact string, no default/environment lookup. Call `deployment.Describe` before adapter/storage I/O. |
| `--deployment-status` | Both binaries: require explicit profile, no other operation/config arguments, write `deployment.WriteStatus(profile, stdout)`, exit 0. Static capabilities only; not a live readiness result. The existing server app output is stderr, so route this special status output explicitly to stdout in the entry dispatcher. |
| server `--unix-socket=/absolute/run/board.sock` | `Config.Endpoint = "unix://" + value`; use `Config` validation then `app.ListenUnix`. |
| server `--data-dir=/absolute/data` | `Config.DataDir`; only `registry.New` opens/creates it. |
| server `--policy-file=/absolute/policy/release.json` | `Config.PolicyFile`; pass to dev8 loader after `Validate`; never the synthetic bearer fixture loader. |
| server `--audit-export-dir=/absolute/exports` | `Config.ExportDir`; separate offline evidence destination. This flag creates nothing and does not invoke export/signing. Bind dev3's explicit offline output path under the same disjointness policy. |
| client `--deployment-projection=/absolute/projection.json` | Explicit bounded three-field projection loader, no credential fd; freeze the resolved value once. `NewClientTransport(profile, projection)` binds its Unix endpoint. This profile supplies no endpoint from home/cwd/environment. |

Reject duplicates (including empty-first-then-value), empty explicit values,
unknown flags, mixed deployment/synthetic flags, and positional server args.
Reject deployed invocation lacking a profile before writes. `--deployment-status`
with no profile exits 2; `production` exits 2 with both A2-035/A2-036 diagnostics.
No `--force`, `--production-ready`, admission override or gate-success bits.
The legacy synthetic qualification entry remains reachable only through its
explicit synthetic inputs; it is not a default deployed profile.

Any `--listen`, `--tls-cert`, `--tls-key`, `--remote-tls` activation input with the
dogfood profile must refuse before adapter loading or registry creation, including
status requests mixed with these arguments. Map merged transport options to
`Config.TCPListen/TLSCertFile/TLSKeyFile/RemoteTLS`, rather than silently dropping
them. `NewVerifiedTLSServer` and existing synthetic TLS tests remain buildable
and independently qualified; none are used in this profile.

## Small constructor patch proposal

`app-wiring.patch` proposes one NEW app helper file. It is a review artifact,
not an applied change to dev3's active app files. It is based on the exact
transport352aedf5 constructors, with no dependency/module/shared-interface delta.
The entry dispatcher remains dev3/integrator-owned: parse the flags above and
call the helpers in the following order:

1. Handle the status-only form through `deployment.WriteStatus`. Status emits no
   config, account, UID, credential or path. Keep server lifecycle notices on stderr.
2. For server startup call `deployment.Validate(c)` **before** reading policy or
   writing anything. On error exit 2 (configuration), printing only the bounded
   error. `Validate` is read-only and does not load or bless a policy.
3. Bind dev8's loader to an adapter with `auth.ProtectedPeerResolver` and the
   explicit authorized provisioning project list. Verify the static release
   policy is owned by the expected UID, regular, nonsymlink, exactly 0600, valid,
   complete and ready. Those checks and schema belong to dev8, not this module.
   Pass the validated `Plan`, adapter, projects and exact build commit into
   `serveSingleTrustUnit`. A missing/unready adapter must refuse before storage;
   no fake resolver, bearer fixture or inferred current-UID grant in this branch.
4. The helper maps `Plan.Config().DataDir` to `registry.New`, the authorized
   projects to `Provision`, then `domain.New`, `NewUnixServer`,
   `Plan.WithStatusHeaders(server.Handler)`, `ListenUnix`, and
   `ServeUntilCanceled`. `Synthetic` is only the existing constructor's carrier
   type; no synthetic fixture/authorizer is constructed or used by the helper.
5. `Swarma-Board-Deployment` response header contains the complete safe JSON
   descriptor on actual `/ready` and `/v1/version` responses. `/ready` body and
   status retain existing storage/current-policy checks (including 503 failures).
   Do not interpret descriptor presence, status 200, or static local status as
   production admission. Reserve this header; downstream middleware must not
   overwrite it. A body-field extension would be a separate HTTP-owner patch.
6. For the client resolve the explicit projection once and pass the value and
   remaining product args to `runSingleTrustClient`. That helper supplies a
   matching Unix transport and nil credentials to `cli.Run`. The projected
   AccountID is only a preflight expectation; SO_PEERCRED and policy establish
   the one shared-UID identity. Never infer per-agent identities.
7. Dispatch dev3 offline maintenance before normal serve parsing, while retaining
   its exclusive read-only locks, explicit paths and signer custody requirements.
   This profile does not start maintenance or permit signing with the bot key.

This patch is not complete until those entry calls and the dev8 binding are
present in the actual `cmd`-reachable functions. A nil binding deliberately
refuses. Host/board orchestrator owns start/stop/packaging; after draining, close
storage. Never unlink an occupied socket, spawn/restart from the client, fall
back to local files/TCP, retry writes, or automatically recover. The helper's
error paths return; an external orchestrator decides recovery.

## Path and validation contract

`Validate(Config)` accepts only canonical absolute UTF-8 paths without whitespace,
controls, backslashes or a root resource path. Data tree, socket parent tree,
policy file and offline export tree must be pairwise disjoint (equal/ancestor/
descendant all refuse; sibling prefixes are allowed). Existing ancestors must
be nonsymlink directories; an existing policy file must be regular. Missing
policy/data/export paths are allowed at config preflight, so packaging/adapter
readiness remains a distinct mandatory step. Socket parent must already exist;
a socket path may be absent or an existing socket (client/status validation),
never a symlink/regular file. `ListenUnix` still refuses **every** occupied path,
including a live or stale socket, and independently enforces parent ownership,
private mode and inode-preserving cleanup. Mode/owner/readiness checks remain in
the existing constructors and dev8 adapter. Validation cannot reserve paths or
prevent a same-UID actor changing them afterward; it claims no isolation.

Do not put policy/socket/export artifacts under the registry: it enumerates
`.db` files plus its lock only. Keep policy, audit signer/trust custody and exports
outside the data/runtime trees. Audit signer/trust custody remains dev3's
separate decision/implementation; this descriptor does not imply a signed export.

## Exact acceptance evidence

Run `GOMAXPROCS=2 go test -p 2 ./internal/deployment` and its race variant once for
the package. Tests exercise negative profile/TLS/path cases without writes,
immutable config snapshots, no-dial construction, endpoint binding and safe JSON.
A constructor test uses the REAL app Unix listener/kernel peer hook, registry,
HTTP readiness and client preflight with a clearly FAKE shared-UID resolver. It
proves both authority and storage failure stay 503 with the descriptor present,
and Unix client preflight never consumes a credential. It does not qualify dev8.

After wiring, build both actual final entry points with the exact 40-hex candidate
commit stamp and run `binary_acceptance.py` as documented in its help. Supply a
**synthetic** dev8 policy fixture for the current nonprivileged UID, one project
and one account, never a live release policy. The script uses only private temp
paths and exits 1 against the current unwired base. It verifies both actual
binaries' descriptor/status, production and missing-profile refusal, invalid
activation before registry creation, live UDS readiness/version headers, client
preflight, auth-readiness refusal on policy mode loss, dead-service refusal and
no resurrection. It must be run by the integrator against the final binary pins;
Python syntax checks or constructor tests do not satisfy this gate.

Additional integrator gates: retain existing remote TLS tests/builds; dev8's
mode/owner/malformed/unknown-UID/revocation tests (including keep-alive); dev3's
read-only export tests and signer trust tests. Inspect final diff for no spawn,
write retry, local board fallback or alternate live listener. Distinct-UID/UID
helper/publisher, independent Muse review, Source landing, custody/adoption and
owner cutover approval remain external gates. No privileged UID/mode experiment
or real deployment is required or authorized by this handoff.

Implementation attribution: agents2_swarma_dev10, model `gpt-6.1-sol`. Local
candidate author/signing identity: Relux Zion. Independent Muse review and
Source-authorized landing remain required. No push or activation is authorized.
