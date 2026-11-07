# Filesystem producer brief — held until contract review

Implementation follows independent review of the common contract, using the
exact accepted spec/conformance digest and signed base. This brief does not
authorize implementation.

## Immutable inputs

Board integration base: `relux-works/swarma-board`
`a1b87160b509732125ea8a6b21ade43321ffd8fc`, tree
`03ac0349632592e90f2eb87fa75a3e5cf22cbf18`.

Existing FS reference: `relux-works/skill-project-management`
`09bac3d89822c9b83c04dff6483d7b2074097a82`, tree
`e5d993f465092c00e88170c9628015bb8432ac1d` (current default branch at observation).
No newer task-specific FS packet/head was supplied. Root/source custodian should
confirm whether another candidate is the intended “current FS work”; do not
replace this pin with a floating branch or read peer conversations for it.

Consume the reviewed standalone provider module and common runner supplied by
the shared contract owner. The current draft is not authorization to implement
against unreviewed choices or to change an old contract unilaterally.
Implementation is separate work after protocol review. The superseding owner
decision is no migration. Existing filesystem
behavior remains; there is no sanitizer, pilot or data-conversion dependency.
This draft composes merged PR6 `4bb8406e025b1377426f15e7a78be72061c023e4`,
including PR5's notes fix unchanged; the old
pins above remain source-semantics references. Read review-disposition.md and
the revised common limits/native-v2 contract before implementation admission.

## Preserve the existing design

Source evidence at the FS pin:

| Mechanism | Exact source / implication |
| --- | --- |
| Existing protocol | [`pkg/board/datastore.go:3–37`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/pkg/board/datastore.go#L3-L37) already separates domain-oriented data operations and normalized board-scoped locking. Adapt it; do not rebuild a board from arbitrary file CRUD. |
| Required atomic pair/move/recovery seams | [`datastore.go:39–98`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/pkg/board/datastore.go#L39-L98) explicitly forbids unsafe pair fallback and defines store-independent move plans. |
| Markdown store | [`fs_store.go:22–67,97–110`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/fs_store.go#L22-L110) has BoardDir separate from RuntimeRoot and transaction-pinned write time. Keep stable IDs, markdown rendering/parsing and layout knowledge. RuntimeRoot is not a new server dependency. |
| Pair journal and overlay | [`progress_pair.go:33–128`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/progress_pair.go#L33-L128) writes durable pair intent before progress files and uses generation/rollback. Preserve this reasoning and its fault witnesses. |
| Recovery order | [`progress_pair.go:131–165`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/progress_pair.go#L131-L165) completes move/activity/pair/resource recovery before reconciling the write journal. Do not reorder casually. |
| Multi-path rollback mechanism | [`activity_transaction.go:242–321`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/activity_transaction.go#L242-L321) persists prepared/committed state and restores prepared transactions. This is a reuse candidate, not proof it covers the new scoped revision/event envelope. |
| Durable file replacement | [`fs_store.go:1014–1069`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/fs_store.go#L1014-L1069) distinguishes content sync from parent-directory durability. Preserve that distinction; an ambiguous post-rename error is not known rollback. |
| Non-authorizing journal | [`board_write_journal.go:20–75`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/board_write_journal.go#L20-L75) deliberately carries no admission/membership authority. It must remain so. |

## Smallest bounded delta

Implement a **server-side scoped adapter and one provider commit envelope**,
not a replacement filesystem board engine. The existing `DataStore` methods
lack ProductID/BoardID, a shared revision-CAS Update and coherent state+event
Checkpoint. The pair generation is not a global task revision; the write journal
is not the task event stream. Add only the protocol bridge, scoped resolver and
metadata/commit boundary needed for these differences. Preserve existing file
formats and recovery components where they meet the shared contract.

This is a narrow architectural piece, not an evidence-backed claim that its
durability implementation is trivial or already complete. The producer must map
the **whole** ChangeSet (including arbitrary multi-record updates, deletes,
provenance, content-addressed resource bytes/membership/references, revision and
event) to one durable decision; pair-only journals and
process-local locks alone cannot satisfy it. If the current recovery mechanisms
cannot be composed without weakening their guarantees, stop with a precise
design delta for the contract owner; do not weaken parity or discard the FS work.

Current FS reads also need a deliberately strict server adapter:
[`fs_store.go:230–298`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/fs_store.go#L230-L298)
can report tolerant-walk faults and derives CR/checkpoint lifecycle. The new
provider must reject incomplete reads and omit execution-authority derivation;
it must preserve representable task facts. Do not invoke the legacy loader as a
black box and assume its complete semantics belong in the new product.
It opens only new trees created by the provider with recognized schema, complete
Scope metadata and recovery envelope. Legacy trees stay on old task-board;
unknown sections and progress-without-README refuse unchanged. Markdown fidelity
applies to supported new trees, never a promise to open arbitrary legacy trees.
P6's openability sentence is a later-script target, not conversion instructions.

The legacy implementation is under another module's `internal` package and
imports CLI/internal execution and remoteconfig packages (`fs_store.go:13–20`).
It cannot simply be imported into the standalone protocol. Propose a narrow,
attributed extraction of storage/markdown/recovery components into the assigned
adapter, retaining LICENSE/NOTICE and exact provenance. Leave the upstream
repository unchanged. Do not transplant its CLI, sessions, Change Request
authority, runtime goals, credential discovery or hidden control-root fallback.
Existing meaningful fields and markdown behavior must be preserved; a mismatch
is a precise protocol/adaptation question, not permission to drop fields or
invent an existing-data conversion project.

Implement the same ResourceInfo/ResourceInput/ResourceReader, ReadResource, PutResources and
DeleteResources contract as DB. Digest-derived server storage never accepts a
caller path; same digest in another product/project grants no lookup. Preserve
existing resource mechanisms where compatible. Blob publication, record refs,
membership, CAS and event share the complete commit envelope. ReadResource and
Checkpoint verify digest/size before exposing owned readers; P3 defines input
ownership, EOF/read/close errors, handle lifetime, private bounded staging and
one-revision payload consistency. A fresh live resource loop cannot implement
Checkpoint. During long writes, bounded-lock read `unavailable` is allowed only
under P6's contention-window parity rule; quiescent semantic parity remains exact.
Reject corruption and
all common count/byte/time limits without truncation. No URI fetch, unpacking or
implicit garbage collector. No existing resource tree is converted by this brief.

## Ownership and acceptance

Proposed write scope: `internal/providerfs/**` and FS-only attribution notes.
Common provider records/runner, root go.mod/go.sum, `internal/contract`, domain,
auth, public API, app and config remain with the shared integrator. Send proposed
shared changes as a patch in the producer packet; do not race another producer.
No new dependency or version upgrade without a reviewed exact-pin disposition.

Use fresh synthetic state only. Run every shared provider case C01–C22 and C31,
plus FS fault subcases in C13–C16; the integrator runs C23–C30 for both factories.
C32 needs actual native filesystem/host authority. Run C33–C41 too: resource
integrity/isolation/atomicity/bounds and common operation deadlines. Preserve existing markdown,
recovery, unsafe-path and non-authorizing-journal witnesses where meaningful;
do not duplicate implementation-shaped tests merely to increase test counts.
Explicitly show that identical task/board IDs in alpha and beta never alias
paths, locks, cache, revision, events or checkpoints. No external legacy writer
may share the adopted live server tree.
Also run C42–C50 at their assigned layers. In particular map C45 container rename
and reparent through all real C13–C16 phases for descendants outside ChangedIDs.
Domain owns C43 last-ref deletion/name replacement; adapter enforces resulting
ref integrity and quota reclamation atomically. C44 keeps provider bytes opaque
while domain rejects NUL. No 5 s large-update claim is permitted: C50 must measure
the proposed common 300 s operations and 600 s recovery, or hold qualification.

Deliver exact base/head/tree/diff, command clocks/exits, source attribution,
common runner digest, per-case results, failure evidence, supported topology,
actual native limitations and a sanitized hashed packet. All execution remains
offline confined with fresh owned HOME/TMP/cache and read-only source/tools.
Source acceptance, process-death tests, physical durability, production identity,
signed review/landing and activation are separate claims. No server start from
a client/Curator wrapper, runtime data import, migration, deployment or merge is
authorized by this brief.
