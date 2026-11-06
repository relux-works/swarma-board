# Proposed server data-provider protocol v1

This is a reviewable proposal under the authority and exact source bindings in
[README](README.md). MUST below defines the proposed common contract; it does not
claim owner ratification. No implementation, deployment or migration is authorized
by this draft alone. Existing product behavior outside the listed deltas stays in
the root contract and its D6 successor sections.

## P1. Boundaries and module ownership

```text
CLI / thin Curator command wrapper
  -> remote board API client -> authenticated board server boundary
  -> shared domain/query/transfer services -> data-provider module protocol
  -> filesystem adapter OR database adapter selected once at server startup
```

Remote means a separate service boundary, including a Unix-domain HTTP transport
on one host; it never means opening local board files from a client. Verified
remote TLS remains a separate transport/admission gate. Clients never select a
backend, receive storage paths, import either implementation, start a board
process or recover a dead service. A thin Curator wrapper is a command client,
not the board service or its lifecycle owner.

Proposed module layout in the **existing repository**, without creating another
repository:

| Location / module | Owner and allowed dependencies |
| --- | --- |
| `modules/dataprovider`, module `github.com/relux-works/swarma-board/dataprovider` | Contract owner; exported records, errors, interfaces, canonical encoding, conformance vectors. Standard library only. Independently versioned. Does not import root `internal/*`. |
| `internal/providerfs` | FS producer; imports protocol plus narrowly extracted, attributed FS/markdown/recovery mechanisms. No CLI, remoteconfig, sessions, runtime, credentials or execution authority. |
| `internal/providerdb` | DB producer; imports protocol plus approved exact engine pins. Existing `internal/store` and registry are the reuse baseline. |
| `internal/domain`, `internal/query`, `internal/transfer`, `internal/contract` | Shared integrator; adapt existing record ownership to the protocol, keeping task rules in domain. Transitional type aliases may avoid two divergent Element definitions. |
| `internal/app`, `internal/deployment`, `internal/httpapi`, `internal/auth` | Shared integrator; config, factory, scope/auth/admission, transport and wire compatibility. One writer of shared files. |

Module packaging is proposed, not present today. The root module will need an
explicit reviewed dependency on the protocol module. For same-repository
development, a checked-in relative replacement may be proposed and reviewed with
the module extraction; it is not permission for arbitrary replacements or a
dependency upgrade. Release/version/tag policy remains with the source owner.
The shared conformance runner lives with the protocol and accepts both adapter
factories; adapters cannot fork its expectations. Root tests must explicitly run
the separate module tests too; `go test ./...` at root alone will not cover it.

The module owns provider records, not the network API or an upstream identity API.
Domain services own validation, derived inverse edges, plans and semantic errors.
The provider owns scoped facts, CAS, atomic persistence, journal continuity,
recovery, coherent checkpoints and lifecycle. Auth remains the server boundary.
No provider has a listener or parses DSL, bearer tokens, capability documents,
project file paths or caller-selected database queries.

## P2. Identity, product scope and data model

Proposed value declarations (notation only; no Go file is added):

```go
type Scope struct { ProductID, BoardID string }
type Revision struct { Scope Scope; Counter uint64 }
type Board struct { Scope Scope; Name, Description string }
type Snapshot struct {
    Board Board
    Revision Revision
    Elements []Element
    Provenance []ProvenanceFile
}
type ChangeSet struct {
    Upserts []Element
    Deletes []string
    Provenance []ProvenanceFile
}
type Actor struct { AccountID, RequestID, Operation string; At time.Time }
type Commit struct { Revision Revision; ChangedIDs []string }
type Event struct {
    FormatVersion uint32
    Scope Scope
    Revision Revision
    Actor Actor
    ChangedIDs []string
}
type Checkpoint struct { Snapshot Snapshot; Events []Event }
```

`ProductID` and `BoardID` each use the existing ASCII project-ID grammar
`[a-z0-9][a-z0-9_-]{0,63}`; empty, uppercase, traversal, encoded separators,
backslashes, NUL and Unicode lookalikes refuse before storage lookup. This is
identifier validation, not path cleaning or an authorization decision. BoardID is
the existing project ID within one product. Scope is always the pair; neither a
board ID nor a globally unique-looking task ID may substitute for it. Element
references (parent, blockers, manual edges and resource references to elements)
resolve only in that same board. Cross-board/product links refuse; no implicit
join, fallback product, wildcard or caller-provided path exists.

The exact stored Element fields, nullability, checklist order, resources, UTC
times and provenance semantics start from
[`internal/contract/element.go:7–49`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/contract/element.go#L7-L49).
Move these types with attribution or alias them centrally; do not regenerate a
reduced FS or DB record. Scoped ownership is explicit in Board, Snapshot,
Revision, Event and provider arguments; Element storage keys are
`(ProductID, BoardID, canonical Element.ID)`. An Element cannot relocate by
changing an embedded field. Same IDs in different products/boards are legal.
Within one board, case-insensitive element lookup preserves the stored stable
ID, and case-fold collisions refuse as today. A task assignee is a label, never
an authenticated account or a grant.

Metadata enumeration is the authorizer's scoped catalog, not a directory scan
returned to a client. Providers expose no public unfiltered ListProducts or
ListBoards operation. Control-plane provisioning is an explicit server operation
after admission. Product identity, catalog ownership, and grant authority are
configured server inputs; no new principal registry is invented here.

## P3. Operations and outcomes

Proposed standard-library protocol:

```go
type Provider interface {
    View(context.Context, Scope) (Snapshot, error)
    Update(context.Context, Scope, Revision, Actor,
        func(Snapshot) (ChangeSet, error)) (Commit, error)
    Checkpoint(context.Context, Scope) (Checkpoint, error)
    Ready(context.Context) error
    Close() error
}
type Provisioner interface {
    EnsureEmpty(context.Context, Board) error
}
```

| Operation | Exact semantics |
| --- | --- |
| `View` | One coherent committed state at one scoped revision; complete detached records, bounded before returning. No initialization, recovery writes, partial/tolerant tree read or dropped parse failure. Unknown scope: `not_found`. |
| `Update` | Validate scope, nonnil callback and complete server Actor. Under the writer transaction/lock, compare full scoped expected revision, run callback at most once on detached state, validate changes, atomically publish facts/provenance/revision/event, return sorted changed IDs. No unconditional write variant. |
| `Checkpoint` | One coherent Snapshot plus contiguous events 1..Counter from the same logical read transaction. No separate View+audit combination; no signing or replay claim. Bounds overflow refuses the whole result. |
| `Ready` | All configured handles/schema/recovery/capabilities usable; no secrets or board metadata in public output. A startup/latched recovery failure is unavailable. It does not authenticate anyone or establish production admission. |
| `Close` | Stop new operations, drain admitted in-flight operations, close handles exactly once, return the stable close error on repeated calls. Later operations refuse `unavailable`. Server stops admission and drains HTTP before invoking it. |
| `EnsureEmpty` | Explicit control-plane provisioning, never network board CRUD. Create only absent empty scope at revision 0 with no events. Existing identical metadata: no-op even with existing facts; mismatched metadata: `scope_conflict`, never overwrite. Failure must not report a usable partially provisioned scope. |

No board deletion, backend switching, import parsing, raw SQL, partial writes,
resource payload upload, arbitrary files, capability minting, or transaction
nesting API is in this protocol. Native/archive import is validated by shared
Transfer/domain then uses one `Update`; emptiness and expected revision are
checked inside that transaction. Backup formats can remain provider-specific
offline operator tools. Portable comparison/export uses the common Checkpoint;
neither authorizes restore or cutover.

Callbacks are trusted server domain functions: bounded, deterministic from their
inputs and injected clock/ID generator, no external I/O, no credentials, no
provider re-entry or pointer retention used to mutate persistence. They run zero
times on stale revision/invalid scope; otherwise at most once. A DB may not
silently rerun the callback on serialization retry. Return `revision_conflict`
and require the server to recompute from a fresh read instead. The provider must
detach/copy the accepted ChangeSet before commit; retained caller slices cannot
mutate stored facts. Callback errors return unchanged and have no committed effect.

Actor shape validation is mandatory but is not authentication: AccountID is
nonempty valid UTF-8, at most 256 bytes, with no control characters; RequestID is
1..128 printable ASCII bytes; Operation is one of the existing allowlisted
mutation operation names (including `import`); At is a nonzero UTC instant.
These bounds apply equally to synthetic fixtures. The boundary remains
responsible for establishing the actor; passing shape checks grants no authority.

## P4. Errors and refusal mapping

Typed protocol error `{Code, safe Message}`; implementation causes remain in
protected server diagnostics, never raw paths, DSNs, tokens or board contents.
HTTP/CLI adapters own status/exit mapping. Context cancellation/deadline remains
recognizable through standard `errors.Is` and maps to unavailable/transport
failure, with the outcome rule below. Success data is zero/absent on error.

| Code | Meaning / server mapping proposal |
| --- | --- |
| `invalid_request` | Invalid IDs, scope mismatch in expected revision, invalid Actor, duplicate/disjointness failure; HTTP 400. |
| `not_found` | Admitted scope absent, or delete target absent; HTTP 404. Unauthorized scope refusal occurs before lookup. |
| `scope_conflict` | Existing scope metadata differs during provisioning; control-plane conflict, HTTP 409 if ever adapted. |
| `revision_required` | Missing network precondition, server rejects before provider; HTTP 428. |
| `revision_conflict` | Expected Counter differs; HTTP 412, current revision disclosed only after authorization. |
| `invalid_snapshot` | Record-count/schema/record-shape or provenance validation refuses; HTTP 422. |
| `payload_too_large` | Defined byte bound exceeded, never truncate; HTTP 413. |
| `unsupported_schema` | Unknown/newer persistent format; HTTP 503. Never initialize or downgrade it. |
| `unavailable` | Closed/busy/recovery/required capability/backend unavailable or revision overflow; HTTP 503. |
| `storage_error` | Detected malformed committed facts/history or pre-commit storage failure; HTTP 500. |
| `commit_outcome_unknown` | Commit publication may have occurred but durability/acknowledgment could not be established; HTTP 503 with reconcile guidance. Never automatically retry. |

`unauthorized`/401 and `forbidden`/403 are server authorization errors, not
provider-derived identity verdicts. Domain errors (`dependency_conflict`,
`query_error`, etc.) retain their existing mappings and roll back callback work.
There is no "unsupported transaction" success fallback. A provider lacking any
required guarantee refuses startup; optional optimization capabilities do not
relax semantics.

## P5. Revision, isolation and ordering

Revision begins at 0 per Scope and increments exactly once on every successful
editor command including no-op and import. No successful command increments it
twice; reads, dry-runs and refusals never increment it. Overflow at max uint64
refuses without callback or mutation. Exactly one event is stored at the new
counter with the same Scope, Actor and sorted distinct changed IDs. Event here
records v1-style facts, not a digest-bearing D6 journal or authenticated history.
Old events are immutable through the product API. A storage administrator can
rewrite storage; a provider is not an independent attestation system.

Updates are serializable within one Scope, including writers using separate
handles/processes in any supported deployment topology. Concurrent updates with
the same expected revision have one winner, other callers get conflict (or
explicit bounded owner-lock refusal before admission); no lost update. Products
cannot alias state, revision, cache entries, journal, lock key or backup target.
Per-scope concurrency is allowed; globally serialized writes are conforming and
must not be advertised as parallel. Cross-scope transactions are not supported.
No snapshot is held across network calls; reads see before or after a commit,
never a mixed pair. Single server owner of a private local directory remains the
initial supported topology; no network FS, HA, replication or distributed
transaction claim follows from the word database.

Provider snapshot Elements sort by bytewise stored full ID; Provenance by path;
Commit.ChangedIDs sorted unique; events ascending revision. Nested checklist,
resource and edge arrays preserve stored order. Domain computes the same graph,
counts, filters and plans on the same complete snapshot for either provider.
Providers do not implement a second DSL/query engine.

Existing list pagination remains `skip=0`, `take=50`, range 1..1000, after common
filtering and full-ID sort. It is deterministic within a revision. For a multi-call
stable scan, propose an optional **read** `If-Match` scoped revision: first page
returns it, later pages supply it; if changed, return `revision_conflict` and
restart the scan. Checking it and reading the page use the same View result.
Without a pinned revision, separate requests are independently consistent and
may shift across writes. No opaque cursor, historical snapshot retention or
keyset-pagination promise is introduced. Count/list/plan within one response use
one View, never several provider reads.

## P6. Transaction, durability and recovery

All facts affected by one domain ChangeSet, deletes, provenance replacement,
revision and event share one commit decision. Provider validation precedes that
decision. Nil provenance means unchanged; nonnil empty means replace with empty.
An empty accepted ChangeSet still advances revision and writes one empty-ID event.
Duplicate case-folded upserts, duplicate deletes, intersecting upsert/delete IDs,
unknown deletes, malformed records or bad provenance digest/path refuse atomically.

Before the durable decision, callback/validation/cancellation/faults leave the
previous logical state and journal intact. After the durable decision, a crash
may leave a fully committed command without a response. Cancellation does not
retroactively roll back a committed command. The client must read/reconcile;
there is no automatic write retry or exactly-once network-delivery claim.
When the implementation cannot distinguish pre/post decision (e.g. parent fsync
failure after a rename), return `commit_outcome_unknown`, latch scope unavailable
until recovery/reconciliation, and retain evidence. Never label that error as a
known rollback. Recovery exposes either the complete old or complete new state,
never mixed facts/revision/events, and is idempotent. Invalid/ambiguous recovery
evidence refuses and is retained for an operator.

DB: use the chosen engine's real durable transaction and finite lock/operation
bounds. Existing bbolt's CAS and coherent read transaction are reusable. Any
engine configuration disabling durable commits is outside this contract.

FS: preserve existing markdown, lock, journal-before-write, sync/rename,
reciprocal overlay, move recovery and fault mechanisms as reusable components.
Add one provider commit envelope spanning **the complete** ChangeSet plus scoped
revision/event; a mutex alone, pair-only generation counter, sequential
WriteReadme/WriteProgress calls or a non-authorizing write journal is insufficient.
FS logical reads use that envelope/lock or return unavailable until recovery;
external legacy readers/writers of the live tree are outside the service boundary
and must be stopped before adoption. Unreadable/unknown meaningful fields refuse
rather than being omitted. Do not call legacy LoadBoard's execution/CR-derived
authority hooks from the provider. Preserve any non-product fields as explicit
inert provenance only when an approved compatibility mapping is lossless;
otherwise block import/adoption, never silently discard them.

Default proposed limits, for parity: 10,000 Elements per Scope; 64 MiB compact
UTF-8 serialized Snapshot (protocol key/record order, described below); 64 MiB
complete serialized event NDJSON including LF; 8 MiB per provenance payload;
existing network query/archive limits remain 8/64 MiB. Charge sizes before
unbounded decoding/accumulation and refuse, never truncate. This strengthens the
old store's raw allocation limits and requires validation in both adapters.
Limit changes belong to the common contract, not server tuning that changes
which implementation accepts a request. Resource/time bounds still need measured
qualification; these byte caps are not a total process-memory guarantee.

For the size/parity oracle, protocol v1 JSON uses compact Go encoding/json,
UTF-8, default HTML escaping, no trailing newline; structs in declaration order
in P2 with snake_case keys; nested Scope `{product_id,board_id}`, Revision
`{scope,counter}` and Actor `{account_id,request_id,operation,at}`. Element retains
the exact existing native-v1 declaration/tag order and nullable facts; optional
review/task_class retain omitempty. Provenance `{path,sha256,data}` uses lowercase
SHA256, standard padded base64 for bytes. Snapshot/ChangeSet field names are
`board,revision,elements,provenance` / `upserts,deletes,provenance`; Board is
`{scope,name,description}`. Commit is `{revision,changed_ids}`; Event is
`{format_version,scope,revision,actor,changed_ids}`. UTC times use RFC3339Nano.
Top-level empty arrays are explicit `[]`, except ChangeSet.provenance nil is
`null`. Element nested null/empty distinctions stay as stored. Protocol runner
owns canonical vectors; adapters may use different internal encodings.

## P7. Capability and admission at the server boundary

Startup validates immutable explicit config, selected provider/schema/features,
protected auth policy readiness and scoped catalog before opening listeners or
provisioning any scope. Provider selection grants no authority. A provider's
readiness/feature report is an operational claim checked by the server, never an
owner capability or approval.

Per request: authenticate using the existing selected transport boundary; resolve
and validate product/board route; authorize the exact `(principal, ProductID,
BoardID, operation/role)`; parse/allowlist and bound input; enforce write revision
precondition; construct Actor from authenticated account and server-generated
request ID/time; call the domain/provider. Reject a denied request before any
provider lookup/read/provision callback, including scope-existence probes. Check
current policy each request, including keep-alive, and again at the mutation
admission point after body parsing. The documented admission point linearizes
with policy revocation under the owning authorizer's accepted policy; revocation
does not undo an already admitted transaction. If the authority adapter cannot
provide that contract, production is held. No upstream capability API is invented.

If a deployment uses capability proofs, the accepted server authorizer must
validate issuer, audience, subject, operation, exact scope, expiry and revocation
before admission; presence of a caller JSON field cannot admit it. This is a
boundary requirement, not a new token format or provider method. Filesystem
permissions are service storage custody, not principal authorization. Shared
Unix UID remains one identity; projected AccountID, assignee, product metadata,
imported signatures and provider journals never establish identity/grants.

## P8. Configuration, compatibility and open decisions

Proposed server config shape (schema example only, not accepted CLI flags):

```json
{
  "storage": {
    "provider": "filesystem",
    "filesystem": {"root": "/absolute/server-owned/board-state"}
  },
  "legacy_product_id": "explicit-product"
}
```

Database configuration selects `provider:"database"` and a separately approved
engine-specific block. No engine/DSN/driver is frozen by this example. Exactly
one selected block; unknown, duplicate, missing, empty, conflicting or
unsupported fields refuse before I/O. No environment/cwd probing, client flag,
fallback on failure, mixed per-request provider or hot switch. Storage root is
absolute, canonical, service-owned, private, unaliased and separate from policy,
runtime sockets and export destination. DB secret acquisition, if needed, is an
explicit protected server integration and never a client/provider payload/log.
Backend change requires stopped writer, new target, approved checkpoint/import,
reconciliation and separate owner authorization. No automatic data copying.

Proposed wire extension: explicit `/v2/products/{productID}/boards/{boardID}`
prefix for q/export/import and a scoped authorized board catalog. Query/domain
operation names retain existing behavior. Proposed v2 ETag/If-Match syntax is
`"scope-<scope_sha256>-rev-<counter>"`: lowercase 64-hex SHA256 of the exact compact
Scope JSON bytes defined in P6, followed by canonical unsigned decimal Counter
(no leading zeros except 0). Reject weak tags, `*`, multiple values, malformed
values and a digest unequal to the route's exact Scope as `invalid_request`.
This binds the full Scope and Counter; a tag from another scope refuses. v1 routes
may remain only with one explicitly configured `legacy_product_id`; scope is
injected server-side, with product-aware authorization, never chosen by a header
or default. v1 revision tags remain target-URI preconditions, never authority.
Without that explicit compatibility mapping v1 board operations refuse; public
health/version behavior does not imply v1 data readiness. No current client
silently guesses a product. Product-aware projection/client/API work is owned by
the integrator and must be reviewed together, before either backend is adopted.

Persistent scoped schema is new. Existing schema-v1 DB metadata and native-v1
archives have no ProductID; do not reinterpret them in place. Proposal: retain
legacy read-only/export tools; import approved checkpoint into a new empty
explicit Scope with source digest/mapping recorded, new revision-1 local event,
old journal retained as inert provenance, no fabricated historical product
identity. Native-v2 scoped transfer format requires the shared compatibility
producer's reviewed contract before implementation; the proposed scoped ETag
syntax above must be reviewed with the product-aware client/server changes;
they are not prerequisites to testing the provider module with synthetic values.
No existing DB/schema/archive file is changed by this draft. Existing unsigned
maintenance exports remain unsigned; D6 digest/signature/migration gates stay held.

The existing source uses Storm v3.2.1 over bbolt v1.3.4. This is the smallest DB
reuse candidate, **not** a d205 engine selection. Root must resolve whether to
retain that engine before DB dependency/driver implementation. Neither SQL nor
another database is invented here. Scope schema/version, packaging and wire
compatibility are review decisions, explicitly recorded in decisions.json.

Contract review acceptance must freeze this spec, conformance.json and the
shared runner ownership. Both adapter producers then implement against that one
contract, with independent reviews bound to their exact heads. Shared integration
and runtime admission are separate gates. Source tests, compilation, Linux
synthetic crash tests, native filesystem qualification, production admission and
activation are distinct claims and must be reported separately.
