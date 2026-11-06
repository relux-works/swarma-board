# Data-provider contract review packet

Status: **DRAFT — source contract only; independent review and adoption pending.**

Assigned architect: workbench `399-board-data-provider-contract`, identity
`agents2_swarma_dev3`, for `TASK-261107-cpd6wt` extending `mo58kt`.
Requested model/effort: `sol/high`; observed model/effort: **unknown**, because no
runtime attestation was supplied. This packet contains no provider implementation.

Authority supplied to this workbench: Sirius event
`$7OFihcIsXY88XuM-EW1pfC6ZfK8Tg4lj_m3XOXg_9ww`, sender
`@meta-keeper.keeper-sirius:gr.relux.works`, dated 2026-11-06 21:28:28 UTC,
section F, relaying owner d205. The workbench did not independently query Matrix.
The supplied event is the authority input, not an assertion of a new owner decision
by this architect.

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

Only these owner requirements are treated as decided here:

1. Board operations always cross a client/server boundary.
2. Server storage uses a data-provider protocol in its own module.
3. Filesystem and database are separate implementations selected by server config.
4. Product scope belongs in the protocol and data model.
5. Adapt the existing filesystem work; review the contract before staffing both
   implementations against a single conformance suite.

Everything more specific in this directory is a concrete proposal for review.
In particular d205 did not name a database engine, a module path, an API version,
or a persistent schema migration. Existing Storm/bbolt pins are source facts.

The root `contract.md` D1 (one backend, one module), the old ownership exclusions
for the FS store, and D6's frozen shared types conflict with the requested extension.
This draft deliberately does not silently rewrite those documents or authorize
implementation under contradictory contracts. Contract acceptance must name the
approved deltas, frozen conformance digest, module owner, migration disposition
and database engine decision before producer release. Root coordinates that gate.
No worker was launched, no runtime task data was opened, and no board binary was run.
