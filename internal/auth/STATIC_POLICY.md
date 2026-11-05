# First-deployment static peer policy candidate

Implementation responsibility: static-policy adapter owner.
Base: `352aedf5acac1ee058a36c26befe7916aa48186f` (completed board transport).
Authority: approved first-deployment requirements described below.

This is a candidate operator format and package-local implementation of the
existing `ProtectedPeerResolver`, pending independent review and Source
landing. It introduces no platform API, publisher, production policy, real data
import, command flags, listener activation or shared contract change. Existing
binaries still expose their synthetic startup paths. The integrator owns the
explicit production composition and release packaging.

The first mode is Linux, UDS-only, `sharedUID`, `single-trust-unit`. Service and
clients use the same effective OS UID. Every process with that UID obtains the
same board account and grants. Mode 0600 provides no per-agent isolation and
cannot keep policy from other processes sharing the owner UID. Separate O/S/C
principals remain a tracked production-admission gate under these requirements.
The protected per-request policy publisher remains a later production dependency. This adapter never
refreshes grants from disk during a service lifetime.

## Exact operator format

The following is documentation with synthetic values, not an installed policy.
Replace UID 12345 with the actual effective service/client UID and replace all
example release/account/project values through the reviewed release process.
The expected policy version is separately pinned in release configuration.

```json
{
  "schema_version": 1,
  "policy_version": "release-20261105-example",
  "mode": "sharedUID",
  "trust_profile": "single-trust-unit",
  "remote_tls": false,
  "projects": [
    {"id": "example-board", "name": "Example board", "description": "Synthetic example"}
  ],
  "accounts": [
    {"id": "example-shared-unit", "disabled": false}
  ],
  "uid_mappings": [
    {"uid": 12345, "account_id": "example-shared-unit"}
  ],
  "grants": [
    {"account_id": "example-shared-unit", "project_id": "example-board", "role": "editor"}
  ]
}
```

All shown keys are required, exact-case and non-null, including the display
fields and `disabled`. Arrays may be empty except `accounts` and `uid_mappings`,
which must each have exactly one entry. No additional keys are accepted at any
level. UTF-8 JSON has an 8 MiB bound; duplicate keys (including escaped aliases),
trailing values, wrong types, unknown schema and any other mode/profile refuse
construction. No token, Bearer hash, agent name or publisher field exists.
`remote_tls` must be the JSON boolean `false`; omission/null/true refuse.

`policy_version` must equal the separately supplied release pin. It follows the
existing board ID grammar: 1–64 ASCII lowercase letters/digits, with `_` and `-`
allowed after the first character. Give every approved policy change a new
release version. This equality check is not signature verification: the
reviewed release and its packaging/lifecycle owner retain source custody.

The sole mapping must explicitly name the sole account, and its numeric UID must
fit uint32 and equal the effective UID that owns the file. Account IDs follow
existing board account validation (nonempty UTF-8, at most 4096 bytes, no space or
control characters). Project IDs use the board ID grammar above. Duplicate
projects, duplicate grants, dangling account/project references and unknown roles
refuse the whole policy. Roles are `viewer`, `editor`, `admin`; higher roles
include lower ones, scoped to that explicitly granted project. Admin grants do
not authorize other projects. Unknown UIDs/accounts and disabled accounts refuse
identity/authorization. A known enabled account with no grant authenticates but
cannot access a project; its project list is empty. An all-disabled or empty-grant
policy is valid and ready because it safely denies access.

The absolute, clean policy path must have no symlink component. The file must be
regular, owned by the effective UID, exactly 0600 without special mode bits, and
have one hard link. The operator installs it as part of an approved release;
the adapter never creates, chmods, chowns or repairs it. Keep its release directory
under approved owner custody; ancestor permissions are not certified by this
adapter. Linux component-relative no-follow opens prevent symlink traversal;
nonblocking opens let FIFOs be refused without hanging. Unsupported platforms
refuse startup until their file custody and peer credentials are qualified.

## Snapshot, drift and lifecycle

Construct `NewStaticPeerResolver(policyPath, expectedPolicyVersion)` once at
startup. Missing/unsafe/invalid policy yields `unavailable`, with no policy bytes
in the error. A nil/zero resolver cannot be ready. Every `Ready`, `ResolvePeer`,
`Require` and `Projects` call checks the path again against the accepted inode,
owner/group, full mode, link count, size, modification/change times and SHA-256
of its bounded bytes. Access time is excluded because reads may update it.
Each grant check consults the accepted immutable account/grant snapshot, never
a role stored in a connection or supplied by a client.

Any observed read failure or difference permanently latches the resolver closed
with `ErrStaticPolicyRestartRequired`. Even an identical replacement inode,
chmod/touch, malformed edit or valid new policy version trips it. Restoring the
old bytes/permissions does not reset it. File checks are serialized for concurrent
requests; they are point-in-time checks, not a continuous monitor. A change
occurring after a check can be detected only by a subsequent check. A completed
change wholly between checks may go unobserved; no changed bytes become grants.
There is no immediate-revocation guarantee, including for already admitted work.
Every policy method reads/hashes up to 8 MiB; there is no background watcher or
mtime-only fast path. Account for this disk/CPU cost when qualifying load.

On drift, readiness and subsequent protected requests refuse. Through
`PeerAuthorizer`, readiness reports the safe `unavailable` contract error; HTTP
`/ready` and protected endpoints return 503 when their readiness check detects
drift. If drift is first observed inside a subsequent resolver call, the existing
peer adapter can sanitize that failure to 401/403; it still refuses the request.
`/health` remains liveness-only.
Integrators must stop admission, drain/stop the old service, inspect the drift,
install the approved versioned release with owner-private file custody, and
start a new service instance. Do not recreate the resolver in a running process
or automatically clear the latch. Rollback likewise needs a reviewed release
pin and full restart. No file edit is an authorized live policy publication.

## Constructive integrator wiring

No app/cmd wiring is modified by this candidate. In the integrator's explicit
first-deployment startup, fail startup on every constructor/readiness error:

```go
resolver, err := auth.NewStaticPeerResolver(policyPath, expectedPolicyVersion)
if err != nil { return err }
peer, err := auth.NewPeerAuthorizer(resolver)
if err != nil { return err }
handler, err := httpapi.New(httpapi.Options{
    Service: service, Parser: parser, Transfer: transferAdapter,
    Authorizer: peer, PeerAuthenticator: peer,
    StorageReady: registry.Ready, Commit: releaseCommit,
})
if err != nil { return err }
// Configure the normal bounded server and lifecycle around this handler.
server.Handler = handler
server.ConnContext = httpapi.UnixConnContext
```

Use the **same** peer adapter for both authorizer fields. Retain the existing
`httpapi.UnixConnContext` hook on the actual accepted `*net.UnixConn`; it obtains
SO_PEERCRED from the kernel. Preserve its returned context when composing hooks.
Do not wrap the connection into an unrecognized type or replace the UID with a
header, socket address, configured account, bearer value or claimed agent name.
No kernel UID means refusal. A `Principal` is server-internal; do not construct
one from request data. `PeerAuthorizer.Authenticate` always refuses Bearer, and
the existing Unix HTTP adapter rejects Authorization and spoofed identity headers.

The existing `app.NewUnixServer` demonstrates these authorizer/hook settings;
its `Synthetic` container and executable flags are qualification infrastructure,
not permission to use a synthetic fixture as production authority. Provision the
approved new empty target through the integrator's explicit registry/lifecycle
path. If a startup catalog of granted projects is needed, `resolver.ResolvePeer`
with the effective UID followed by `resolver.Projects` returns the accepted
account/catalog (or refuses for a disabled account); never use this startup
bootstrap in place of per-request kernel authentication. Ungranted declared
projects are deliberately absent from `Projects`. No existing board is imported.

Use the existing private owned-directory `app.ListenUnix` policy (or equivalent
reviewed Host lifecycle), normal HTTP timeouts and drain-before-storage-close.
Do not start TCP/HTTP/TLS listeners or compose a remote Bearer authorizer in this
first deployment. `remote_tls=false` rejects a policy requesting remote service;
the resolver cannot prevent a caller from separately opening an unrelated TLS
server. The integrator must enforce the listener selection, with remoteTLS off.
TLS qualification code in the transport base remains available for future work.

Run Go checks with `GOTOOLCHAIN=go1.25.5 GOMAXPROCS=2` and `-mod=readonly -p 2`.
New tests include strict format/custody/grants/latching, concurrent checks under
the race detector, and a real shared-UID Unix HTTP keep-alive drift witness with
no board storage/import. This does not qualify foreign-UID provisioning,
per-agent isolation, non-Linux activation, a protected publisher, production
load, release custody, signer custody or live deployment. Independent
review, Source landing and remaining production-admission gates still apply.
