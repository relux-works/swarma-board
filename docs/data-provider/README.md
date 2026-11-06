# Data-provider contract review packet

Status: **DRAFT — source contract only; independent review and adoption pending.**

Latest revision: workbench `498-board4-spec-review-fixes`, isolated worker
`agents2_swarma_dev7`, acceptance 2026-11-06 23:04:55.635384 UTC, deadline
23:59:55.635384 UTC (no clock reset). Sirius event
`$g9e8CZ9Gkq7f16A8A5-QoNzarXxrA0hhbwju5CAdmRM`, 22:55:14 UTC, conveys
orch-muse's changes-requested old-semantics review by **Claude Opus 5.5**, against
PR4 head `fd0f0555e615e7f853514ed8c2c5ee8dd6e8c8b9`. The owner binds `urn:sha256:`
and native payload export/reimport. See [all ten dispositions and review
limits](review-disposition.md); cases C01–C50 remain proposed, not executed.
The revision composes `main` at `d2e3d201939aa88c6acdaa09bf0dcee157928b18`
(merged PR5 notes fix), retaining its code byte-for-byte. PR6's PAX composition
belongs to unit 492; this work neither edits nor lands it. Historical 399/425
bindings below are retained as provenance, not claims of current main or renewed
acceptance. The exact signed head and tree are in the exported receipt, avoiding
a self-referential commit hash in this document.

Original 425 architect: workbench `425-board-spec-without-migration`, identity
`agents2_swarma_dev3`, for `TASK-261107-cpd6wt` extending `mo58kt`.
Requested model/effort: `sol/high`; observed model/effort: **unknown**, because no
runtime attestation was supplied. This packet contains no provider implementation.

Superseding authenticated authority: Sirius event
`$Hbt_VEETzXDZtWhMFzkyk1q024iFt45NQPcrv3CzQao`, sender
`@meta-keeper.keeper-sirius:gr.relux.works`, dated 2026-11-06 21:50:39 UTC,
owner batch section 2: **no migration; drop migration sections; keep the protocol
and both implementations**. Earlier owner migration asks are superseded. No
migration, sanitizer, pilot or existing-data conversion work is proposed or made
a protocol dependency. Implementations are separate subsequent workbenches,
after protocol review; there is no full product build in this revision.

This isolated revision descends from 399 head
`7eaf7fb78d499c96b6b6fc6a9dd9bf620efa9195`, tree
`34fd073c278a3eed6f913c39635f69fb36d809ff`, retaining its pinned base below.
The sealed 399 packet and its original receipt remain untouched. 425 has its own
immutable limited revision clock, 2026-11-06 21:54:04.000983–22:29:04.000983 UTC;
399's earlier receipt/clock are history, not a reset or new runtime acceptance.

Frozen source bindings:

| Source | Head | Tree |
| --- | --- | --- |
| `relux-works/swarma-board`, default `main` | `a1b87160b509732125ea8a6b21ade43321ffd8fc` | `03ac0349632592e90f2eb87fa75a3e5cf22cbf18` |
| `relux-works/skill-project-management`, default `main`, FS reference | `09bac3d89822c9b83c04dff6483d7b2074097a82` | `e5d993f465092c00e88170c9628015bb8432ac1d` |

Read [the specification](spec.md), [the conformance contract](conformance.md)
and [machine-readable cases](conformance.json), then the separate
[filesystem brief](filesystem-brief.md) and [database brief](database-brief.md).
[Source state](source-state.md) distinguishes source facts, old qualification
claims, and runtime evidence. [Decisions](decisions.json) separates binding owner
requirements from proposed implementation choices and unresolved gates.

Owner requirements retained from 425, plus the two bound revision decisions above:

1. Board operations always cross a client/server boundary.
2. Server storage uses a data-provider protocol in its own module.
3. Filesystem and database are separate implementations selected by server config.
4. Product scope belongs in the protocol and data model.
5. Adapt the existing filesystem work; review the contract before staffing both
   implementations against a single conformance suite.
6. First-class scoped content-addressed resources with explicit common bounds.
7. No migration. Existing filesystem behavior remains.

Other design choices in this directory are concrete proposals for review.
In particular d205 did not name a database engine, a module path, an API version,
or a persistent schema version. Existing Storm/bbolt pins are source facts.

The root `contract.md` D1 (one backend, one module), the old ownership exclusions
for the FS store, and D6's frozen shared types conflict with the requested extension.
This draft deliberately does not silently rewrite those documents or authorize
implementation under contradictory contracts. Contract acceptance must name the
approved deltas, frozen conformance digest, module owner
and database engine decision before producer release. Root coordinates that gate.
No adapter worker was launched, no runtime task data was opened, and no board binary was run.

Draft PR publication is authorized; adoption and merge are not. The exact signed
head/tree and spec/conformance hashes are for fresh independent source-semantics
review by orch-muse, coordinated through Sirius. No producer self-review counts.
See [bounded follow-ups](followups.md) for the requested PAX/safePath and note
`##` parser fixes; neither is invented as a migration or protocol prerequisite.
