# Database producer brief — held until contract review and engine decision

Task parent: `TASK-261107-cpd6wt` extending `mo58kt`. Root staffs this separate
producer after the common contract is independently reviewed. This brief neither
launches a worker nor chooses a database engine.
This is a next separate workbench after protocol review, not implementation work
inside 425. No migration, sanitizer or pilot work is proposed or required.

Immutable board base: `relux-works/swarma-board`
`a1b87160b509732125ea8a6b21ade43321ffd8fc`, tree
`03ac0349632592e90f2eb87fa75a3e5cf22cbf18`. Consume the same accepted standalone
protocol and conformance digest as the FS producer. No provider-specific fork of
domain logic, records, error semantics or expected test results.

## Actual reusable database source

| Mechanism | Exact source / implication |
| --- | --- |
| Current pins | [`go.mod:1–17`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/go.mod#L1-L17): Storm v3.2.1, bbolt v1.3.4, Go 1.25.5. These are observed pins, not a d205 decision about the future DB implementation. |
| Existing repository seam | [`internal/contract/contract.go:36–61`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/contract/contract.go#L36-L61) already has detached snapshots, ChangeSet callback, revision CAS and lifecycle. Extend ownership into the accepted module instead of duplicating types. |
| Private/schema-aware open | [`internal/store/store.go:49–136`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/store/store.go#L49-L136) uses 0600 files, O_EXCL for new storage, finite 2-second bbolt open timeout, schema checks before Storm initialization. Preserve refusal of legacy/unknown files. |
| Real transaction | [`store.go:182–314`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/store/store.go#L182-L314) reads revision, calls once, writes facts/provenance/audit/revision in one bbolt write transaction. Adapt, do not replace it with an in-memory mutex plus separate writes. |
| Coherent checkpoint | [`internal/store/export.go:15–99`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/store/export.go#L15-L99) already reads bounded detached snapshot and contiguous v1 audit together. Its audit has no D6 change digest; do not report that it does. |
| Scoped handles/drain | [`internal/registry/registry.go:77–199`](https://github.com/relux-works/swarma-board/blob/a1b87160b509732125ea8a6b21ade43321ffd8fc/internal/registry/registry.go#L77-L199) provisions by project and leases handles during operations. Extend map/lock/storage ownership keys to the full product/board pair. |

## Proposed bounded work

First obtain root/source-owner's engine disposition. Retaining Storm/bbolt is
the lowest source delta candidate because the existing implementation already
supplies most transaction mechanics. If another engine is chosen, require a
separate exact dependency/config/custody and transaction design decision. Do not
assume PostgreSQL, SQLite, a network service, SQL, a DSN shape,
new credentials or extra infrastructure. Do not download or upgrade dependencies
until approved exact pins and canonical h1 validation are available.

Against the accepted engine, implement `internal/providerdb/**` as the shared
protocol adapter. For retained bbolt, reuse storage/registry mechanisms and
source tests with attribution. Every key and stored metadata record must bind
ProductID+BoardID. Snapshot, expected revision, Event, provisioning and Checkpoint
must reject a mismatched scope; same board/task IDs in two products must coexist.
Keep task validation/plans/filtering in the common domain layer. No DB-specific
query results, status transitions or transaction retry of callbacks.

Current v1 metadata only has project identity and revision; current audit is
Revision/Context/ChangedIDs. Preserve existing source behavior and bytes. This
adapter brief is for synthetic, explicitly scoped new state against the reviewed
protocol. It neither opens existing data in a new mode nor defines a conversion,
archive mapping or transfer format. Unknown formats still refuse without
initialization. No earlier migration request remains a work dependency.

Implement first-class scoped content-addressed resources in the same durable
transaction: ResourceInfo/Resource, ReadResource, PutResources, DeleteResources,
record references, resource membership, revision and changed-resource event IDs.
Verify SHA256/size on put and read; coherent Checkpoint includes exact resource
bytes. Same digest in another product/project is independent. Resource-only
updates still use CAS and one event. No separate blob service, URI fetch, hidden
transaction retry or resource GC. Enforce all common count/byte/time bounds;
engine-specific encoding cannot change observable parity.

One transaction must include CAS, all accepted changes, provenance, exactly one
revision increment and event. Enforce immutable earlier events, typed safe
errors, canonical ordering/limits, detached data and coherent Checkpoint.
Declare supported process topology; an embedded DB in a private local directory
does not imply network/multi-node/HA support. A real engine transaction and
finite contention handling remain mandatory. Unknown write outcome must produce
reconciliation guidance, never a hidden second attempt.

## Ownership and acceptance

Proposed write scope: `internal/providerdb/**`, DB-only attribution and adapter
factory tests. The shared integrator owns root module/dependency changes,
registry/app wiring, compatibility, auth and all common records/runner. Existing
store/registry files remain read-only references unless root explicitly assigns
their extraction/move to this producer; do not create concurrent shared writers.

Run the same provider cases C01–C22 and applicable C31 subcases; the shared
integrator runs C23–C30 against both providers. Fault placement C13–C16 must map
to actual transaction boundaries, not a fake method returning an error after
an unrelated successful write. C33–C41 cover resources/bounds/deadlines.
C32 native durability stays a separate evidence
claim. Existing tests are starting witnesses, not proof of the new product-scope
or module contract. No result from the old suite automatically passes a new case.

Deliver exact base/head/tree/diff, common runner digest, engine/config/pin
evidence, each case's result, preserved failures, command clocks/exits and a
sanitized hashed packet. Execute only source-reviewed focused validation offline
under the required confinement and fresh owned HOME/TMP/cache. Distinguish
source, synthetic process-crash, physical durability, production admission and
activation. Require independent exact-head review, checks and signed PR landing.
No live DB discovery, runtime task-content ingestion, board server activation,
external service provisioning, dual writes, data migration or merge is authorized.
