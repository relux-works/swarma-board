# Data-provider contract review packet

Status: **DRAFT — source contract only; independent review and adoption pending.**

Independent review pending. This draft uses `urn:sha256:` references and
bounded native payload export/reimport. See [all ten dispositions and review
limits](review-disposition.md); cases C01–C50 remain proposed, not executed.
The revision composes `main` at `4bb8406e025b1377426f15e7a78be72061c023e4`
(merged PR6 bounded PAX fix and PR5 notes fix), retaining all product code
byte-for-byte. This packet contains no provider implementation.

Owner decision, 2026-11-06: «никаких миграций не надо делать , потом скриптами по месту разберемся».
No migration; drop migration sections; keep the protocol and both implementations.
Earlier owner migration asks are superseded. No migration, sanitizer, pilot or
existing-data conversion work is proposed or made a protocol dependency.
Implementations are separate subsequent work after protocol review; there is
no full product build in this revision. Existing filesystem behavior remains.

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

Owner requirements:

1. Board operations always cross a client/server boundary.
2. Server storage uses a data-provider protocol in its own module.
3. Filesystem and database are separate implementations selected by server config.
4. Product scope belongs in the protocol and data model.
5. Adapt the existing filesystem work; review the contract before staffing both
   implementations against a single conformance suite.
6. First-class scoped content-addressed resources with explicit common bounds.
7. No migration. Existing filesystem behavior remains.

Other design choices in this directory are concrete proposals for review.
The owner requirements do not name a database engine, a module path, an API version,
or a persistent schema version. Existing Storm/bbolt pins are source facts.

The root `contract.md` D1 (one backend, one module), the old ownership exclusions
for the FS store, and D6's frozen shared types conflict with the requested extension.
This draft deliberately does not silently rewrite those documents or authorize
implementation under contradictory contracts. Contract acceptance must name the
approved deltas, frozen conformance digest, module owner
and database engine decision before producer release. The contract owner
coordinates that gate.
No adapter worker was launched, no runtime task data was opened, and no board binary was run.

Draft PR publication is authorized; adoption and merge are not. The exact signed
head/tree and spec/conformance hashes are for fresh independent source-semantics
review. Independent review pending. No producer self-review counts.
See [bounded follow-ups](followups.md) for the landed PAX/safePath and note `##`
parser fixes. The follow-up review's eight dispositions are in
[review dispositions](review-disposition.md). Empty-target-only native-v2 import,
reader-based resources and the remaining interface clarifications require fresh
independent delta review and a marker check on the exact proposed landing head;
no earlier acceptance is inherited by this revision.
