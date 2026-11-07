# Filesystem provider draft

This Linux-only backend stores actual scoped facts and payload files. It imports
only the shared protocol and the standard library. It has no listener, database
engine, ambient root discovery, legacy loader, migration or production wiring.

**Partial implementation:** this generation core does not yet extract the pinned
native README/progress renderer or its hierarchical element paths. It preserves
all protocol fields and literal notes in canonical JSON, but that is not evidence
of native markdown/path compatibility. The native-compatible tree adaptation and
container/subtree path fault qualification remain unfinished FS work. Do not use
the shared runner's green provider assertions as full P6, parity or release
acceptance. Integration and native qualification remain separate.

## Storage and decision

`Open` requires an existing absolute canonical directory owned by the service UID
with mode 0700. Only a completely empty directory acquires a new recognized
schema. Nonempty roots without that schema refuse intact. A real nonblocking
`flock` owner lock remains held until `Close` drains admitted operations. Another
owner refuses within the shared two-second acquisition budget. Operations use
separate locks keyed by the complete product/board pair.

A scope contains immutable Board metadata, `CURRENT`, and one complete generation.
Each generation contains a canonical snapshot, complete event NDJSON, raw
content-addressed resources, and a scoped envelope binding metadata digests.
Every operation validates the selected generation, including resource digest and
size. Extra files, unsafe aliases, foreign scope, malformed schema and incomplete
history refuse. No tolerant scan drops unsupported text. Integrity hashes detect
storage corruption; they are not an authenticated history or authority proof.

An Update first syncs an intent naming the old pointer and new generation. It
validates and syncs the complete new snapshot, every resource and all events,
then syncs the generation directory and a new pointer. One atomic `CURRENT`
rename followed by its parent-directory fsync decides the complete envelope.
Before that rename, refusal removes only unpublished state. A failure after
rename may be ambiguous: return a zero Commit with `commit_outcome_unknown`,
latch admission, and retain evidence. Recovery checks the entire old/new state
and all intents before changing evidence, then idempotently discards an
unpublished generation or finishes committed cleanup. It never fabricates facts
or events. Invalid/ambiguous evidence is retained unchanged.

ReadResource and Checkpoint pin read-only file descriptors to exact immutable
inodes before releasing the scoped lock. Sequential child opens use those same
inodes, including after membership deletion. No live membership lookup supplies
an old checkpoint. The shared owned-handle implementation verifies bytes before
exposure, preserves original admission deadlines, releases expired leases and
makes Close idempotent. Close stops admission immediately and drains within the
shared 330-second cap; timeout retains backing for a later Close.

## Capacity and topology

Logical limits are the unchanged common limits, including event capacity and
retirement. Every original resource input is consumed once through verified EOF;
logical command encoding reopens private staging only. Identical puts verify
actual byte equality as well as size/digest. Resulting-state quotas account for
atomic replacement and deletion. Payloads are streamed with bounded buffers.

This initial algorithm copies retained resources and the complete event stream
into each new generation. Temporary space can approach twice the old/new logical
state, plus metadata. Old inodes pinned by readers also consume space until their
leases end. ENOSPC before publication leaves the old state; it is not a quota
reclamation or engine-shrink promise. Maximum-state RAM, sync cost, temporary
space and 300/600-second workload budgets have **not** been qualified.

The complete max-uint64 history fixture is impossible under contiguous history
and the finite 256 MiB event cap. No counter injection invents that history.
Scope retirement has no compaction/reset/cross-scope carry-forward operation.

Only cooperative single-owner service storage is supported. A shared Unix UID
is one custody boundary; permissions and journals grant no principal authority.
No external writer may share the live root. Linux process kills do not establish
power-loss durability, macOS behavior or any production admission.

## Evidence and reusable test seam

The adapter tests supply the unchanged `conformance.Factory` Instance seams:
Provider, Provisioner, same-root Restart, distinct real peer handle and Cleanup.
The physical FaultDriver is test-only. It maps journal sync, first/last write,
resource payload/membership, event publication, rename, parent sync, cleanup,
recovery and exposed read/close points to the actual code. Error injection is a
deterministic control; it is not a claim of real disk exhaustion or hardware
failure. Kill mode invokes the same backend in a separate test process and
records actual SIGKILL exit status and clocks. No simulated kill satisfies it.

The common runner retains fifty explicit results. Server/domain integration,
native OS/filesystem qualification and large workloads are NOT_RUN here. The
256 MiB raw-resource scope case is also NOT_RUN in the bounded worker: metadata
and copy staging necessarily exceed its 256 MiB-per-fixture physical allowance.
No shared limit is reduced to fit that allowance. A later authorized fixture
budget is needed. Complete history-cap installation is not supplied.

A separate 16 MiB disposable tmpfs control exercises actual ENOSPC, old-state
rollback and clean reopen after space is provided. Its initial failure exposed
a truncated-intent cleanup bug, which was corrected. This small physical control
does not establish maximum-state capacity or native durability. Long-caller
lifetime checks have produced both passes and failures near the shared runner's
expiry boundary; retain those failures and hold lifetime qualification. The
shared conformance base also requires corrections before acceptance, so its
individual green assertions are evidence only, not accepted case verdicts.

## Source mechanisms

The design was checked against `relux-works/skill-project-management` at
`09bac3d89822c9b83c04dff6483d7b2074097a82`, tree
`e5d993f465092c00e88170c9628015bb8432ac1d`: `progress_pair.go` journal-before-write
and whole-pair recovery; `activity_transaction.go` prepared/committed rollback;
`fs_store.go` content sync versus parent-directory durability; and
`board_write_journal.go`'s explicit non-authorizing boundary. The draft extends
those storage principles to one complete provider envelope. It does not copy
upstream CLI, execution authority, credentials or fallback discovery, and does
not claim that the native renderer/path extraction has been completed. Existing
repository LICENSE and NOTICE remain applicable.
