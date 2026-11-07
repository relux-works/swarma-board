# Adapter and integration handoff

Both adapters consume this module and its embedded original fifty-case vector
file unchanged. The current draft supplies common values and executable oracle
actions; [MATRIX.md](MATRIX.md) identifies incomplete subcases explicitly. No
backend or integrated conformance pass is claimed.

The FS source is fixed to `relux-works/skill-project-management` commit
`09bac3d89822c9b83c04dff6483d7b2074097a82`, tree
`e5d993f465092c00e88170c9628015bb8432ac1d`. DB retention is **Storm v3.2.1 and
bbolt v1.3.4**, matching
[`tools/board-server/go.mod` at that pin](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-server/go.mod).
These two decisions are resolved in `docs/data-provider/decisions.json`; no
dependency is added to the standalone protocol and no engine is upgraded.
Future source movement requires a separately reviewed re-pin.

There is one concrete FS admission conflict. The fixed-source decision requires
the adapter to read what the current native task-board writes. At that pin,
[`fs_store.go:632–690`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/fs_store.go#L632-L690)
creates native element directories and writes README.md/progress.md;
[`fs_store.go:603–610`](https://github.com/relux-works/skill-project-management/blob/09bac3d89822c9b83c04dff6483d7b2074097a82/tools/board-cli/internal/store/fs_store.go#L603-L610)
loads them through the existing reporting-faults loader. Frozen
[`spec.md:534–552`](../../docs/data-provider/spec.md#p6-transaction-durability-and-recovery)
instead permits only trees created by the new EnsureEmpty with provider schema,
full scope metadata and complete envelope, explicitly refusing legacy/no-schema
trees. An ordinary current native tree does not acquire that new envelope just
because its markdown is representable. Reading it directly and refusing it are
different admission policies. The source contract owner must resolve this exact
seam for adapter admission; this module does not infer conversion, migration,
repair, new scope identity, or a weakened strict-tree oracle. The common module
can proceed independently of that seam.

C12 has a separate fixture limitation: its “valid max-uint64 revision fixture”
cannot contain every event 1..Counter under P3/P5 continuity and P6's finite
256 MiB event cap. The pure overflow guard is tested. A stopped counter-injection
hook can exercise refusal, but cannot prove a valid persisted complete max-counter
checkpoint. The runner reports that subcase NOT_VERIFIED rather than inventing
history, dropping continuity or modifying the frozen vector.

FS implementation remains a narrow attributed extraction into
`internal/providerfs`. Preserve markdown, pair/move/resource recovery and
non-authorizing journals. One complete ChangeSet, descendant path changes,
resources, scoped revision and event need one durable decision. Map journal sync,
first/last write, rename, resource payload/membership, revision/event, parent sync
and cleanup into FaultDriver.Points, including kill during recovery. A mutex,
pair generation or sequential writes do not meet this envelope. Reject partial
reads and preserve evidence. Only measured active-write contention admits the
bounded-lock unavailable exception; retry quiescent reads and compare exactly.

DB implementation remains `internal/providerdb`, adapting existing root
store/registry mechanisms to full scope ownership and the retained engine pins.
Use a real durable transaction, no callback retry, no independent blob commit,
and scoped immutable checkpoint backing or private bounded spooling. The
256 MiB-resource memory/amplification concern belongs to DB qualification; a
passing small fixture or successful compilation cannot settle it. Record actual
RAM, temporary disk, sync counts, OS/storage/config, clocks and exits in C50.

Factory.Open takes a runner-owned absolute 0700 root. Instance.Restart reopens the
same state after close/crash; OpenPeer uses a distinct actual process/handle and
finite owner-lock behavior. FaultDriver mechanisms may not return expected
snapshots or case verdicts. Its kill receipts must include actual nonzero exit,
mapped point and clocks. Retain the original failure evidence. Missing/unjustified
fault points stay NOT_VERIFIED; factories do not fork the suite.

The shared integrator owns root module wiring and central aliases/conversion,
domain/query/transfer/auth/API/config and the integration cases. It must preserve
the existing fixes and behavior; native-v2 remains empty-target-only with C42
nonempty refusal even at current CAS, same-scope/Board validation, complete staged
archive validation, no receipt growth, and no authority/history replay. Domain
owns last-reference cleanup and NUL refusal. Align existing mutation/archive
client deadlines with full 300 s server work before wire qualification.

Native durability C32 and measured workload C50 are separate qualified test runs.
The 30/300/2/330/600 s caps are retained, unmeasured proposals, not reduced for a
test provider. Exceeding a valid common workload bound holds qualification;
neither factory can weaken limits or claim a power-loss/native pass from Linux
process kills or the module's behavioral controls. This source work installs no
provider and starts no board service, listener, migration, native rollout or
hot reload. Fresh independent exact-head review and marker/checks precede any
separately authorized landing.
