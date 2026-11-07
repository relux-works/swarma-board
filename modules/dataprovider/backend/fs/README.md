# Filesystem provider with generated views

This Linux-only backend stores scoped canonical facts and payload files. It
imports only the shared protocol and the standard library. It has no listener,
database engine, ambient root discovery, legacy loader, migration or production
wiring. It implements the accepted P6 `id-rank-pairs/1` mapping for NEW trees.
Native boards and the task-board CLI retain their existing format and behavior.

## Facts and projections

`Open` requires an existing absolute canonical directory owned by the service UID
with mode 0700. An empty root acquires exactly
`{"format":"scoped-filesystem","version":2,"projection":"id-rank-pairs/1"}`.
Missing or unknown schemas, including the earlier private schema-v1 layout,
refuse intact. There is no in-place upgrade. `EnsureEmpty` provisions a complete
scoped generation; repeating it never clears existing state. The explicit
same-Scope, matching-Board, empty-target import guard remains unchanged.

Every generation contains `snapshot.json`, `events.ndjson`, `resources/`,
`elements/`, `projection.json` and `envelope.json`. The snapshot and complete
contiguous event stream are authoritative. All admitted names, parent edges,
nanoseconds, nullable fields, policies, separate manual blockers and ordered
resource references remain in the unchanged common machine representation.
Provider record-shape admission does not add domain graph or filename rules.

Elements in the bytewise-ID-sorted snapshot occupy generation-relative slots
`elements/00000/` through `elements/09999/`, each with exactly `README.md` and
`progress.md`. The snapshot is the complete slot-to-ID mapping. A slot is neither
identity nor an API selector. Insertions and deletions may rerank every slot.
The unchanged common maximum is 10,000 elements; record 10,001 refuses before
publication. Full Scope and ID remain the only identity.

Each view starts with the exact line:

```text
Generated view; snapshot.json is authoritative. Native display defaults are not stored facts.
```

A blank line and the pinned native canonical renderer output follow. The
write-only extraction in `native_view.go` comes from skill-project-management
`09bac3d89822c9b83c04dff6483d7b2074097a82`, under Apache-2.0 (see NOTICE).
README copies Title, Description, Scope and AcceptanceCriteria without fallback.
Progress copies representable fields, maps UpdatedAt to LastUpdate, displays nil
times as native zero time, stable-filters resources by type, leaves Blocks empty
and keeps BlockedBy independent from ManualBlockedBy. Seconds-only display,
empty-policy defaults and grouped resources are view-only. There is no Markdown
parser, editing interface or claim of writable native CLI interchange.

The provider-local envelope requires `projection_sha256`, binding compact UTF-8
`projection.json`. Its field order is layout, scope, revision, snapshot_sha256,
files; entries are path, size, sha256 in bytewise path order. Every read validates
the complete exact slot/file set, permissions, private regular-file ownership,
manifest and deterministic rendered bytes against the selected snapshot. A view
edit refuses even if its hashes and envelope are recomputed. Missing, extra,
unsafe, cross-scope or mixed-generation bytes refuse without repair or import.
Hashes detect inconsistent storage; they do not authenticate a hostile writer.

## Commit, reads and recovery

A real nonblocking `flock` holds root ownership through `Close`; scoped locks use
the full product/board pair and the unchanged two-second acquisition cap. One
Update syncs a durable intent, then a complete new generation: facts, payloads,
events, every projection file and slot directory, manifest and envelope. A synced
new pointer followed by atomic `CURRENT` rename and parent fsync decides all of
it. Predecision failures discard only the known unpublished attempt. Failures
after rename return zero success values with `commit_outcome_unknown`, latch
admission and retain recovery evidence. Recovery validates retained decisions
and complete generations before cleanup; ambiguous or corrupt evidence refuses.

Rename/reparent preserves the complete logical descendant topology. Physical
paths do not encode ancestry. Unchanged descendant records stay outside
ChangedIDs even if insertion reranks their slots. Each read/checkpoint validates
one complete generation; checkpoints detach its exact metadata/history and pin
its resource inodes before releasing the scope lock. They never follow a later
CURRENT. Original caller budgets and common owned-handle semantics are retained.

## Controls and remaining qualification

Direct tests cover exact fact and checkpoint-byte round-trips, literal native
view output, display-only equivalences, insert/delete reranking, full-Scope
isolation, admitted orphan/cycle facts, missing/extra/edited/unsafe pairs, manifest
binding, a coherently rehashed edit and the 10,000/10,001 slot boundary. The slot
boundary uses small records and is not maximum-state workload qualification.

The backend fault driver maps first/last projection writes and manifest sync in
addition to the existing intent/resource/event/decision/recovery points. Subtree
rename/reparent with simultaneous reranking compares whole old/new facts,
provenance, membership, payloads and events after real process death or injected
failure and double reopen. These are provider controls. Domain destination
conflicts and end-to-end C45 integration remain separate. C31/C49 map to
canonical-fact round-trips plus deterministic view integrity, not Markdown
parse/render equivalence. Frozen common runner vectors remain unchanged; their
historical spec digest does not certify this amended layout.

The algorithm copies retained payloads, events and up to 20,000 projected files
per update. Temporary space and sync/write amplification can be substantial;
maximum-state projection/reranking costs, C50, event-cap fixtures, physical
power-loss durability, paired DB/FS parity and native deployment qualification
remain unqualified. No common limits or 30/300/600-second budgets are changed.
Fresh independent whole-source review and an exact new integration marker are
required before public PR or landing. This local successor authorizes neither.
