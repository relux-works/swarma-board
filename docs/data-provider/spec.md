# Proposed server data-provider protocol v1

This is a reviewable proposal under the authority and exact source bindings in
[README](README.md). MUST below defines the proposed common contract; it does not
claim owner ratification. The superseding owner decision is **no migration**;
earlier migration requests are withdrawn, with no migration gate for this protocol.
No implementation or deployment is authorized by this draft alone. Existing product behavior outside the listed deltas stays in
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
    Resources []ResourceInfo
}
type ChangeSet struct {
    Upserts []Element
    Deletes []string
    Provenance []ProvenanceFile
    PutResources []ResourceInput
    DeleteResources []string
}
type ResourceInfo struct { SHA256 string; Size int64 }
type ResourceInput struct { Info ResourceInfo; Reader io.Reader }
type ResourceReader struct { Info ResourceInfo; Reader io.ReadCloser }
type Actor struct { AccountID, RequestID, Operation string; At time.Time }
type Commit struct { Revision Revision; ChangedIDs, ChangedResourceIDs []string }
type Event struct {
    FormatVersion uint32
    Scope Scope
    Revision Revision
    Actor Actor
    ChangedIDs []string
    ChangedResourceIDs []string
}
type Checkpoint interface {
    Snapshot() Snapshot
    Events() []Event
    OpenResource(string) (io.ReadCloser, error)
    Close() error
}
```

`ProductID` and `BoardID` each use the existing ASCII project-ID grammar
`[a-z0-9][a-z0-9_-]{0,63}`; empty, uppercase, traversal, encoded separators,
backslashes, NUL and Unicode lookalikes refuse before storage lookup. This is
identifier validation, not path cleaning or an authorization decision. BoardID is
the existing project ID within one product (project scope, not an extra tenancy
level). Scope is always the pair; neither a
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

### First-class content-addressed resources

A resource is provider-owned opaque bytes, keyed by the full Scope plus the
lowercase 64-hex SHA256 of those exact bytes. Empty bytes are legal and have the
standard empty SHA256. Size must equal the exact streamed byte count; digest/size mismatch refuses
`invalid_snapshot`, malformed digest refuses `invalid_request`. Resource bytes
are immutable. Identical bytes may exist independently in multiple scopes;
physical deduplication cannot expose existence or permit cross-scope reads,
deletes, quotas, locks or recovery effects. Providers never fetch a URI or execute,
unpack, interpret or sanitize resource bytes.

Existing ordered Element.ResourceRefs remain intact. The reserved URI scheme
`urn` and namespace `sha256` are matched ASCII case-insensitively, before the
generic URI allowlist. Thus `URN:sha256:<digest>`, `urn:SHA256:<digest>` and mixed
case variants are same-scope resource references, never ordinary external URNs.
The digest itself MUST be exactly 64 lowercase hexadecimal characters; empty,
short, uppercase or nonhex digests, missing digest separator (`urn:sha256`),
extra suffixes, query/fragment and other malformed reserved forms refuse
`invalid_snapshot`. Valid case variants resolve the same digest while retaining
their accepted URI spelling as reference metadata. Bare `sha256:`
is not an accepted URI. This retains the existing `urn` allowlist in both
`internal/domain/validation.go` and `internal/transfer/validate.go` (`safeURI`),
and works with the existing `Codec.Encode` reference check. Other existing URI
forms retain their existing domain semantics and are not fetched by providers.
Name/Description/Type remain reference metadata, not blob identity. A first-class
reference must resolve in the resulting state of the same Update, including a
newly put resource; missing/foreign resource refuses `invalid_snapshot` with no
cross-scope lookup. Dangling content-addressed references cannot be committed.

PutResources and DeleteResources participate in the same revision-CAS transaction
as records. A nonidentical put at an existing digest refuses invalid_snapshot;
no bytes are overwritten, even for a hypothetical digest collision. An identical existing put is idempotent within an accepted Update;
that Update still advances revision once. Duplicate puts, duplicate deletes or
intersection of those lists refuse `invalid_request`. Delete of an absent
resource is `not_found`; deleting a resource still referenced in the resulting
state refuses `invalid_snapshot`. Unreferenced resources may be stored within
the same bounds. There is no implicit garbage collection, mutable blob update,
unscoped lookup, resource filesystem path, separate staging service or separate
resource revision. ChangedResourceIDs is the sorted distinct digest list of
actual resource membership changes; ChangedIDs retains only changed Element IDs.
Resources are included even when unreferenced in a coherent Checkpoint, with
their exact bytes; a Snapshot contains metadata only. Provenance remains a
separate existing field and is not silently converted into resources.

The shared **domain**, not either provider, owns reference lifecycle. For every
ordinary element mutation it computes old and resulting same-scope reference
counts over the complete snapshot, counting each ResourceRef occurrence. When
an existing referenced digest's count falls from positive to zero, it includes
that digest in DeleteResources in the **same** ChangeSet. A remaining reference
on another element (or another name on the same element) keeps the bytes. A
named-resource replacement puts/verifies the new digest, changes that one named
ref, and deletes the old digest only if its resulting count is zero. Same bytes
under the same name need no membership change. There is no persisted refcount
authority and no async GC; providers independently validate the resulting refs.
Native-v2 import supplies complete initial membership into an **empty target
only**: an archived payload without refs is an explicit unreferenced put and is
retained. There is no old membership to remove and no replacement path; the
domain verifies every resulting reference. This is explicit import membership,
not implicit GC or a provider-specific exception.
Already-unreferenced payloads deliberately put through `put_resource` remain
until explicit `delete_resource`; that operation refuses if any ref remains.
Successful deletion releases count/raw-byte quota at the same commit, including
physical reclamation or bounded reclaimable storage accounted by the adapter;
an unsuccessful/ambiguous transaction cannot report reclaimed logical quota.
Replacement at the byte cap is evaluated on the **resulting** state, not the
temporary sum of old and new payloads. Temporary physical space is a separate
admission check; insufficient disk refuses before decision and retains old state.

The common domain resource policy keeps the old FS **text-only** rule: a
first-class payload containing any NUL byte refuses `resource_binary_refused`
before Update, including import and direct domain puts. It does not add an
unrequested UTF-8 restriction on payload bytes. The provider remains opaque and
binary-capable (provider conformance includes NUL); reference metadata still uses
existing UTF-8/text validation. Remote managers that previously bypassed the FS
validator were binary-capable; this deliberate common-domain restriction is a
compatibility delta, not a claim of parity with all old managers.

## P3. Operations and outcomes

Proposed standard-library protocol:

```go
type Provider interface {
    View(context.Context, Scope) (Snapshot, error)
    ReadResource(context.Context, Scope, string) (ResourceReader, error)
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
| `ReadResource` | Digest lookup in one admitted Scope; verified immutable payload through an owned reader, or `not_found` for absent scope/resource. P3's streaming lifetime rules apply. No fetch/path fallback; corruption is `storage_error`. Reads never advance revision. Later membership deletion cannot change an admitted reader's bytes. This call is not a multi-call snapshot lease. |
| `Update` | Validate scope, nonnil callback and complete server Actor. Under the writer transaction/lock, compare full scoped expected revision, run callback at most once on detached state, validate changes, atomically publish facts/provenance/revision/event, return sorted changed IDs. No unconditional write variant. |
| `Checkpoint` | One owned handle pins a coherent Snapshot, contiguous events 1..Counter, and reader access to every payload in Snapshot.Resources at that same revision. No separate View+audit+current-resource loop; no signing or replay claim. Verify all resource bytes and bounds before returning the handle; P3 defines its lifetime and error rules. |
| `Ready` | All configured handles/schema/recovery/capabilities usable; no secrets or board metadata in public output. A startup/latched recovery failure is unavailable. It does not authenticate anyone or establish production admission. |
| `Close` | Stop new operations, drain admitted in-flight operations within the shutdown bound below, close handles exactly once after drain. A timed-out call returns unavailable without invalidating in-flight handles; later Close calls may finish draining. Once fully closed return the stable final close error on repeats. Later data operations refuse unavailable. Server stops HTTP admission before invoking it. |
| `EnsureEmpty` | Explicit control-plane provisioning, never network board CRUD. Create only absent empty scope at revision 0 with no events. Existing identical metadata: no-op even with existing facts; mismatched metadata: `scope_conflict`, never overwrite. Failure must not report a usable partially provisioned scope. |

No board deletion, backend switching, import parsing, raw SQL, partial writes,
arbitrary files, capability minting, or transaction nesting API is in this
protocol. Bounded resource puts use Update, not an out-of-band blob write.
Existing native-v1/legacy-v2 decoding remains as implemented for their supported
inputs. The bounded **native-v2 payload export** below is the one explicit
transfer addition required by the owner; no historical-data conversion, replay
restore procedure or automatic copying is introduced.
Checkpoint is a coherent protocol value, not an execution or adoption instruction.

### Streaming resource ownership and consistency

Resource payloads cross this interface through readers, never whole `[]byte`
values. Metadata, Elements, Events and existing bounded provenance values remain
detached values. Providers may use private bounded disk spooling or a pinned
immutable backing snapshot; neither adapter may require all payloads in RAM.

For `PutResources`, the caller prepares each Reader before Update and owns it
through Update's return. The callback only returns these prepared inputs; it
does not perform external I/O. After the callback returns, the provider consumes
each reader once, sequentially in put-list order, through EOF, counting Size and
hashing SHA256 before the durable decision. It reads at most the declared Size
plus one byte to detect overflow and enforces the shared caps while reading.
Short/long streams or digest mismatch are `invalid_snapshot`; a non-EOF input
read error is `storage_error` (context errors remain recognizable). Any such
failure has zero committed effects. No seek, replay or hidden retry is required
of an input. The provider never closes caller inputs, including on refusal; the
caller closes them after Update returns. A refusal before consumption may leave
inputs unread. The caller must not mutate, read concurrently or close inputs
during Update; the provider retains neither inputs nor mutable caller buffers
after return. Validated private staging becomes part of the same commit envelope.
Trusted caller inputs must cooperate with the admitted context/deadline; no
claim is made that Go can interrupt an arbitrary blocking Reader.Read.

For `ReadResource`, the provider verifies the complete stored digest/size before
returning a reader, using bounded spooling or stable backing as needed. It must
not expose corrupt bytes and defer integrity checking to an unverified EOF.
The caller owns and closes the returned reader on success, early stop or error.
Only complete consumption to EOF followed by successful Close constitutes a
complete successful payload read; a later I/O error invalidates that read and
must be reported. Safe typed read/close errors follow P4; no raw storage paths
escape. Errors during acquisition return no reader and release provider state.

Checkpoint.Snapshot() and Events() return detached metadata from its fixed
revision; Snapshot.Resources is the complete digest-sorted resource catalog.
OpenResource accepts only a digest in that catalog (`not_found` otherwise) and
returns that revision's verified bytes even after concurrent deletion/update.
It must never resolve through a fresh current-state ReadResource. Both adapters
support sequential opens, including reopening a digest until handle close;
only one child reader need be active at once (another open then refuses
`invalid_request`). Close each child before the next open. The caller closes the
Checkpoint on every path; its idempotent Close closes any outstanding child and
releases all backing state. Child Close is also idempotent and releases its
reader; after handle close, metadata copies remain usable but opens/reads refuse
`unavailable`. Acquisition failure releases all partial handles/staging. A child
read or close failure invalidates the export; it must be reported and the handle
closed, never converted into a complete archive or partial successful checkpoint.

The original operation context remains in force through payload consumption and
Close: ReadResource's 30 s and Checkpoint's 300 s cap include preparation and the
whole stream lifetime, starting at admission. Cancellation/expiry invalidates
further reads/opens, returns recognizable context errors, and releases owned
backing resources; Close stays safe afterward. These handles count as in-flight
for provider Close/drain. Callers close promptly and never carry them across
independent network requests. Output can be streamed to private staging; a wire
disconnect or late read/close failure is an incomplete failed export, never a
successful truncated archive. This defines a contract, not an implemented API.

### Native-v2 own-export/reimport contract

The shared integrator extends transfer, not either adapter. Export acquires one
Checkpoint, reads its catalog/payload readers and closes the handle on every
path, then completes deterministic single-member gzip/ordinary USTAR with no
links, special entries, PAX/GNU extensions, extraction or URI fetch. Manifest path
is `.swarma-board-transfer.json`; required compact-JSON fields in order are
`format:"swarma-board", version:2, source_commit, scope, revision, files,
resources`. Scope/Revision use P6 encoding. `files` is a bytewise path-sorted
array of `{path,sha256,size}` covering every non-manifest file exactly once.
`resources` is a digest-sorted array of ResourceInfo, including **unreferenced**
members. Required `elements.json` is `{schema_version:2,board:{...},elements:[...]}`
with Board encoded as in P6 and the existing Element field/tag order; existing
provenance files retain their
exact paths/bytes. A payload lives only at `resources/<64-lowercase-hex>` as raw
bytes, once per digest; no base64 in tar. Its filename, files checksum, declared
size and ResourceInfo must all agree. No other entries or directory headers are
emitted/accepted. SourceCommit is nonempty NUL/control-free UTF-8 of at most 128
bytes; fixed exporter build metadata, never supplied by an export caller.
For native-v2-eligible state every provenance path has exactly the existing
`provenance/<name>` shape with a single nonempty name of at most **100 bytes**,
retaining the existing ASCII name grammar `[A-Za-z0-9][A-Za-z0-9._-]*` and all
unsafe-path/digest checks. Multibyte names still refuse that grammar; a character
count cannot establish eligibility. The directory is
the USTAR prefix and the name fits its 100-byte field; no split inside a name,
PAX fallback, truncation or renaming is allowed. The revised provider/domain
admission enforces this narrower v2 path bound (`invalid_snapshot`) for new
state, including import, before commit. This does not rewrite the old format's
255-byte total-path validation or existing data: any otherwise valid out-of-range source
refuses native-v2 export explicitly with `invalid_snapshot` before output.
Exactly 100 bytes is eligible; 101 bytes refuses, measured in bytes rather than
characters. All modeled at-cap states below mean this v2-eligible state space.
Empty resources have a zero-length entry. The manifest's source
commit/scope/revision are inert provenance, never Actor, grant or target CAS.

Native-v2 common limits: 20,002 total regular entries (manifest + elements + at
most 10,000 provenance + 10,000 resources), 8 MiB manifest, 128 MiB elements
entry, 8 MiB individual provenance, 32 MiB resource, 512 MiB expanded tar
**including headers, padding and terminator**, 640 MiB compressed gzip including
framing. Charge compressed, expanded, entry/count/JSON and provider bounds before
allocation; no truncation. These are new version-specific limits; old versions'
64/256/8 MiB compressed/expanded/entry limits and the 8 MiB query body stay.
The 640 MiB compressed budget allows overhead for a 512 MiB incompressible tar;
it is an upper refusal bound, not an expected output size or memory claim.
The snapshot cap covers elements plus provenance metadata/base64: their native
entries together are at most 128 MiB plus fixed JSON framing; adding 256 MiB raw
resources, at most 20,002 tar headers/padding and an 8 MiB manifest remains below
512 MiB. Thus all modeled at-cap states have export headroom; encoder validation
must still check exact bytes. Implementations must bound working memory/temporary
space and stream or spool privately; no assumption that buffers fit in RAM.

Import strictly validates the full archive before one scoped CAS Update: duplicate
JSON keys/paths, unknown fields/version, unsafe paths, extra/missing/checksum/size
mismatches, reserved malformed URIs, missing payloads and NUL domain payloads all
refuse with zero effects. Target Scope must equal manifest Scope; cross-scope
remapping is not offered. Import is **empty target only**, preserving root
contract D4/D5: no in-place replacement or rollback is offered in protocol v1.
Under the same target CAS transaction, the callback requires zero Elements,
zero provenance and zero resource membership, otherwise `already_exists`
(HTTP 409) with no mutation. A matching current target revision never overrides
this guard; a stale revision remains `revision_conflict`. A stale own archive
into a nonempty target MUST refuse even with fresh target CAS. The admitted
server Actor and local next revision/event are used, never archive history.
Exported Board metadata must
equal the admitted target's existing Board (otherwise scope_conflict without
mutation); Update cannot rename/provision a Board. This preserves Board metadata
on own-export/reimport without adding board CRUD. Unlike native-v1 Prepare,
native-v2 does **not** append archive manifests/import receipts to persisted
provenance; its inert source report is returned separately. This avoids making an
at-cap board's own export too large to restore to an empty target. The sanctioned
round trip is export, then restore into a separately provisioned empty target
with the **same** Scope and Board metadata (for example on a fresh server), using
EnsureEmpty then explicit admitted native-v2 import. EnsureEmpty is idempotent
provisioning, not an emptiness certificate: inspect the callback snapshot under
CAS, including after a concurrent writer. It never clears an existing target.
The restore preserves all facts, refs and payload bytes and creates one local
import event; repeating it into that now-nonempty target refuses.
This is lossless current-state round trip, not authenticated-history restoration.
It remains subject to CAS, available disk, admission/time bounds and event-tail
space; a sealed event-full scope cannot accept any import. The source archive
remains valid and readable in that case. Native-v1 export must refuse a scope
with first-class resources rather than silently omit bytes; native-v2 is the
default for such scopes, with explicit version negotiation and safe refusal by
old clients. Wire/API integration and compatibility review remain required before
this format is enabled. The maintenance snapshot must choose this payload form
or Checkpoint explicitly; it may not silently call resource-lossy v1 Encode.
The own-round-trip promise is for **domain-valid** state. A provider-only binary
fixture with NUL is legal at that lower layer but is not a valid domain board:
native-v2 domain export refuses it before output with resource_binary_refused,
rather than producing an archive the domain cannot reimport. Checkpoint continues
to preserve its exact bytes for lower-layer diagnosis without claiming restore.

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
mutation operation names (including `import`) plus proposed `put_resource` and
`delete_resource` domain operations; At is a nonzero UTC instant. Combined
record/resource writes retain the admitted domain operation's name.
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
| `already_exists` | Import target has Elements, provenance or resource membership under CAS; HTTP 409, no replacement. |
| `invalid_snapshot` | Record-count/schema/record-shape or provenance validation refuses; HTTP 422. |
| `payload_too_large` | Defined byte bound exceeded, never truncate; HTTP 413. |
| `resource_binary_refused` | Shared domain rejects NUL in a first-class resource; HTTP 422; provider bytes remain opaque. |
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
counter with the same Scope, Actor, sorted distinct changed IDs and changed
resource digests. Event here
records v1-style facts, not a digest-bearing D6 journal or authenticated history.
Old events are immutable through the product API. A storage administrator can
rewrite storage; a provider is not an independent attestation system.

Per-element `activity` history is **deliberately dropped** from this protocol.
Events identify an Actor and changed IDs, but contain neither field deltas nor
old values; filtering them cannot reconstruct what changed on an element.
Imported activity remains inert provenance. No compatibility claim is made for
the old element activity query; a future history feature requires its own scope,
retention, API and review. No history is fabricated from import or checkpoint.

Updates are serializable within one Scope, including writers using separate
handles/processes in any supported deployment topology. Concurrent updates with
the same expected revision have one winner, other callers get conflict (or
explicit bounded owner-lock refusal before admission); no lost update. Products
cannot alias state, revision, cache entries, journal, lock key or backup target.
Per-scope concurrency is allowed; globally serialized writes are conforming and
must not be advertised as parallel. Cross-scope transactions are not supported.
No historical snapshot is held across independent network calls; P3's bounded
stream handle belongs to one admitted read/export operation. Reads see before or after a commit,
never a mixed pair. Single server owner of a private local directory remains the
initial supported topology; no network FS, HA, replication or distributed
transaction claim follows from the word database.

Provider snapshot Elements sort by bytewise stored full ID; Provenance by path;
Snapshot.Resources (also the Checkpoint catalog) sorts by digest; both changed-ID
arrays sort unique; events ascending revision. Nested checklist,
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
resource bytes/membership/references, revision and event share one commit decision. Provider validation precedes that
decision. Nil provenance means unchanged; nonnil empty means replace with empty.
An empty accepted ChangeSet still advances revision and writes one empty-ID event.
Duplicate case-folded upserts, duplicate deletes, intersecting upsert/delete IDs,
unknown deletes, malformed records or bad provenance digest/path refuse atomically.
The provider validates resources and the resulting reference graph before this
decision. Resource-only updates have empty ChangedIDs and the actual changed
digest list; facts-only updates have empty ChangedResourceIDs. Both nil and
empty PutResources/DeleteResources mean no resource change. No resource bytes
appear in events; retained metadata/input buffers cannot mutate state. P3 owns
payload reader lifetime and validation before the decision.

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
FS logical reads conservatively acquire the existing envelope/lock within the
common at-most-2-second lock budget. During a long active write they may return
`unavailable` even while DB readers can complete; serving an old backed snapshot
is not required or claimed. Every successful read/stream remains wholly coherent.
Only this measured active-write contention window's read availability/error
difference is excluded from cross-provider parity (C13/C45). After the writer
finishes, quiescent reads and their semantic errors must match exactly. A latched
recovery barrier still refuses reads until coherent recovery and is tested as
such; it is not an extra general parity exclusion. No torn read is ever allowed.
Existing filesystem behavior and source bytes remain unchanged by this spec.
The new strict FS provider opens **only trees created by its EnsureEmpty** with
recognized provider schema, full Scope metadata and complete coherent envelope.
Legacy trees remain with old task-board; missing schema, unknown sections that
cannot be represented losslessly, and missing README pairs refuse, not repair.
For new trees the adapter must preserve its supported markdown, paths,
field/array behavior and recovery guarantees; unknown source text must never be
silently dropped. No existing board is opened or rewritten here. Strict
server reads must not silently omit unreadable facts. Execution/CR-derived
authority hooks are outside this provider protocol; existing upstream behavior
is not changed by that boundary. If faithful adaptation exposes an incompatibility,
report its exact source seam for protocol review; do not introduce a conversion
project, sanitizer or migration prerequisite. The target for later owner scripts
is a provider-created compatible tree with recognized schema, scoped metadata,
complete representable facts, integrity and recovery envelope; this is an
openability target only, not a migration design, loader or permission. The
supported later-script route for domain-valid native-v2 state is EnsureEmpty
followed by explicit same-scope, matching-Board, empty-target import under P3;
it creates the recognized envelope through the provider, never by editing files.
It does not admit legacy/no-schema trees or relax scope/format refusals.

Renaming/reparenting a container changes physical paths for its entire descendant
subtree even when descendant Element values/ChangedIDs are unchanged. One commit
envelope must cover every affected path plus facts, resource membership, revision
and event. The shared fault suite covers this explicitly; a DB's simpler key
update must not weaken the FS oracle or add descendants to ChangedIDs merely
because their paths moved.

### Common capacity decisions and recovery

All units below are binary MiB (1,048,576 bytes). Proposed common limits replace
the draft's 8 MiB resource / 64 MiB scope numbers; they are **not old parity**.
The review reports 2026-11-06 clone totals of 28 MB architecture, 20 MB session
host (one file >8 MiB), and 17 MB codex-fix after untracking run logs; old server
upload was 25 MiB, old FS had no per-file cap. These are reviewer-supplied mixed
tree totals, not this worker's measurements, raw-resource totals or growth rates.
32 MiB/resource exceeds the old 25 MiB upload by 28%; 256 MiB raw resources is
over nine times 28 decimal MB (and over nine times 28 MiB). It gives explicit
headroom without claiming the unidentified >8 MiB file fits 32 MiB or that any
existing board is admissible. No existing data is opened to establish that.

| Cap | Common value | Refusal and recovery |
| --- | --- | --- |
| Elements | 10,000 | One-over invalid_snapshot; delete existing elements and retry under fresh CAS. |
| Canonical Snapshot | 128 MiB | One-over payload_too_large; reduce/remove facts/provenance/refs in a resulting-state Update. View and coherent Checkpoint remain available at cap. |
| Provenance | 10,000 files; 8 MiB/file; native-v2 name <=100 bytes | Count/path invalid_snapshot / payload bytes payload_too_large; domain may replace/clear provenance to reclaim; no silent dropping; P3 defines v2 eligibility. |
| Resource membership | 10,000; 32 MiB/file; 256 MiB raw total/scope | Count invalid_snapshot / bytes payload_too_large; explicit delete of unreferenced payloads or atomic domain dereference/delete/replacement frees logical quota. |
| Canonical ChangeSet | 512 MiB | Oversized command refuses before decision; send smaller independent commands under fresh CAS where semantics permit; never split a command that requires atomicity. Deletions carry IDs, not removed bytes. |
| Canonical event NDJSON including LF | 256 MiB | Admission tests resulting stream including the next complete event. If it cannot fit, reject Update before decision with payload_too_large; callback may have run once to determine changed IDs. Seal scope read-only only when no next event can fit. See terminal recovery below. |
| Canonical Checkpoint | 1,024 MiB | Whole-result payload_too_large on overflow; this is a redundant encoder guard, not a lower writable-state cap. 128 MiB Snapshot + base64 of 256 MiB resources (~342 MiB) + 256 MiB events + bounded framing is <1,024 MiB. A valid modeled state must remain checkpointable at all other caps. |
| Native-v2 transfer | P3's explicit entry/count/512 MiB expanded/640 MiB compressed caps | Bad/oversized input refuses before Update; old state remains. Valid modeled exports fit with headroom; writer/space failures remain actual failures. Current-state cleanup or a smaller separately scoped command may reduce an oversized request; no truncated archive. |

All resulting-state bounds are checked **together** before durable decision,
including the event that records quota cleanup. Reserve the actual encoded next
event length, not an assumed constant: AccountID/RequestID/ChangedIDs are bounded
but variable. At 90% of event capacity (241,591,910 bytes, floor), signal a safe
capacity warning to the admitted operator and preserve export headroom. The
shared server derives the warning from encoded stream size; it grants no extra
provider API or authority. At an event refusal, a smaller command may fit if its
actual complete event fits; no
bytes from the refused command persist. When not even a minimal valid next event
fits, recovery is **read-only retirement**, not resumed writes:
View/ReadResource/Checkpoint/native-v2 export continue, old events are immutable,
and no compaction/reset/delete/hidden rollover API exists in v1. The server
control plane may provision a different explicitly named empty scope, which
starts at revision 0 with no automatic copying, import, routing alias or history
continuation. Its different Scope fails native-v2's same-scope import rule, so
there is **no supported open-task carry-forward** from a sealed scope into that
new scope in protocol v1. A same-Scope empty target on a fresh server can accept
an explicitly admitted current-state native-v2 restore under P3, with fresh local
history; this is a separate restoration, not unsealing or continuing the original
scope/history, and no automatic rerouting or cross-scope migration is implied.
Continuing the original sealed scope would require a separately reviewed
contract change; operators must not be told that deleting blobs clears history.
The same terminal rule applies to revision overflow. Scope retirement is a
deliberate bounded limitation; an implementation must disclose it before use.

Per-resource/per-provenance caps constrain admission, not existing valid bytes;
at-cap reads still work. Snapshot/blob/count cleanup is possible only while the
next event fits, so warnings precede sealing. On disk exhaustion, pause writes,
retain old state/recovery evidence, and let the storage custodian provide space
outside the protocol before bounded retry/recovery. Logical quota release is
not a claim about immediate engine file shrinkage; adapters must report physical
space/reclaim requirements and cannot promise reuse they do not implement.
No automatic board deletion, storage editing, migration or copying is prescribed.
Limits are identical for both providers and cannot be server tuning. Charge
sizes before unbounded decoding/accumulation; refuse, never truncate. Byte/time
caps need qualification and do not bound total process RAM or disk amplification.

For the size/parity oracle, protocol v1 JSON uses compact Go encoding/json,
UTF-8, default HTML escaping, no trailing newline; structs in declaration order
in P2 with snake_case keys; nested Scope `{product_id,board_id}`, Revision
`{scope,counter}` and Actor `{account_id,request_id,operation,at}`. Element retains
the exact existing native-v1 declaration/tag order and nullable facts; optional
review/task_class retain omitempty. Provenance `{path,sha256,data}` uses lowercase
SHA256, standard padded base64 for bytes. Snapshot/ChangeSet field names are
`board,revision,elements,provenance,resources` /
`upserts,deletes,provenance,put_resources,delete_resources`; Board is
`{scope,name,description}`. Commit is `{revision,changed_ids,changed_resource_ids}`;
Event is `{format_version,scope,revision,actor,changed_ids,changed_resource_ids}`.
ResourceInfo is `{sha256,size}`. For the **logical size/parity encoder only**,
ResourceInput/reader payloads are encoded as `{info,data}` with standard padded
base64 data, including empty string for empty bytes; Checkpoint's logical value
is `{snapshot,events,resources}` with payloads in digest order. Count/encode this
streamingly from validated staging or the checkpoint handle; these are not
reader Go fields serialized by encoding/json, nor whole-buffer return types.
Never consume a one-shot input a second time for size accounting. UTC times use RFC3339Nano.
Top-level empty arrays are explicit `[]`, except ChangeSet.provenance nil is
`null`. Normalize nil resource change lists to `[]` for canonical encoding.
Element nested null/empty distinctions stay as stored. Protocol runner
owns canonical vectors; adapters may use different internal encodings.

Common proposed operation bounds: View/ReadResource default and maximum 30 s;
Update/Checkpoint default and maximum **300 s**, including a 10,000-element
import/FS subtree move; lock acquisition at most 2 s within the operation budget.
Effective budget is min(caller deadline if any, operation cap), not 5 seconds.
300 s is a finite proposal, **unmeasured**, not a promise that 10,000 durable
file updates finish in it. Both providers must qualify maximum-count, large-byte
and deep-subtree cases and report wall time, sync count, storage/config and peak
temporary space. If an allowed FS case cannot complete within 300 s, parity is
NOT VERIFIED and producer release/freeze must revisit this common budget or
algorithm; it may not claim pass, use a provider-specific cap or silently reduce
the shared limits. Cancellation before the commit decision leaves old state;
after it, the outcome rules above apply. The trusted callback must cooperate
with its bounded execution budget; these are not a claim that Go can forcibly
interrupt arbitrary code. Close stops admission immediately and drains to a
documented shutdown deadline (proposed 330 seconds); on expiry return unavailable
while retaining handles needed by unfinished operations, not a false successful
close. Recovery/startup have a separate finite proposed 600 s budget; expiry
retains all evidence, keeps readiness unavailable and requires a custodian-led
bounded recovery attempt after correcting capacity/faults. Do not clear journals
to meet a clock. Any 300 s expiry after decision follows the complete-new or
unknown-outcome barrier rules above; returning a timeout never permits torn reads
or unsafe journal cleanup. A short caller deadline has the same rules. Qualify
actual elapsed times for both providers. No unbounded wait, retry loop or
total-memory guarantee follows from these caps.

The current client has 35 s ordinary / 125 s archive request deadlines in
`internal/client/client.go`; these do **not** yet cover the proposed 300 s
Update/Checkpoint budget. Later shared wire integration MUST align mutation and
archive client/request/transport deadlines with the full 300 s server budget
(including streamed completion), and verify shorter caller deadlines and unknown
outcomes without automatic retry. This draft changes no runtime deadlines or
client code and claims no current end-to-end timing compatibility.

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
  }
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
No backend switching or existing-data copying is part of this protocol work.
Config selection specifies a server implementation; it never transforms data.

Proposed wire extension: explicit `/v2/products/{productID}/boards/{boardID}`
prefix for q/export/import and a scoped authorized board catalog. Query/domain
operation names retain existing behavior. Proposed v2 ETag/If-Match syntax is
`"scope-<scope_sha256>-rev-<counter>"`: lowercase 64-hex SHA256 of the exact compact
Scope JSON bytes defined in P6, followed by canonical unsigned decimal Counter
(no leading zeros except 0). Reject weak tags, `*`, multiple values, malformed
values and a digest unequal to the route's exact Scope as `invalid_request`.
This binds the full Scope and Counter; a tag from another scope refuses.
The v1 route `/v1/boards/{project}` after provider swap binds to exactly **one
explicit server-configured `v1_product_id`**, validated with P2's ID grammar at
startup. V1 project maps to BoardID within that product, and admission authorizes
the full pair. This is a fixed compatibility route binding, never a guessed or
fallback product: no configured binding means v1 routes are disabled (404) before
provider access. V2 always requires its explicit product even when v1 is bound.
V1 preconditions remain v1-shaped only on that route; the server internally binds
their revision to its fixed Scope, and copied/mixed v2 tags refuse. V1 native
transfer remains v1 and safely refuses first-class resources; clients need v2
payload negotiation to export those scopes. Product-aware
projection/client/API details, including bounded remote resource operations, are
future shared integration work after protocol review, not a second client-side
storage implementation. A future server must obtain explicit full scope from
reviewed server configuration/routing; it cannot guess a product or accept a
caller filesystem path. No current DB/schema/archive or filesystem board is
converted, copied or reinterpreted. Only P3's native-v2 payload form is added;
there is no legacy-data mapping. Unknown persistent formats still refuse as required by
the existing storage contract; this refusal is not a migration work dependency.

The existing source uses Storm v3.2.1 over bbolt v1.3.4. This is the smallest DB
reuse candidate, **not** an owner engine selection. Root must resolve whether to
retain that engine before DB dependency/driver implementation. Neither SQL nor
another database is invented here. Scope schema/version, packaging and wire
compatibility are review decisions, explicitly recorded in decisions.json.

Contract review acceptance must freeze this spec, conformance.json and the
shared runner ownership. Both adapter producers then implement against that one
contract, with independent reviews bound to their exact heads. Shared integration
and runtime admission are separate gates. Source tests, compilation, Linux
synthetic crash tests, native filesystem qualification, production admission and
activation are distinct claims and must be reported separately.
