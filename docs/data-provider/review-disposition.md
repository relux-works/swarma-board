# Contract review dispositions

This source-only revision responds to all ten prior findings. The owner binds
findings 1/2; other choices below are explicit proposals requiring fresh
independent review. Independent review pending.

| Finding | Explicit disposition | Contract / future verification |
| --- | --- | --- |
| 1 — refused URI (blocking) | Owner-bound `urn:sha256:<64-lowercase-hex>`; bare sha256 refuses. Existing domain and safeURI/Encode checks need no relaxation. | P2; C33/C34. A narrow source witness does not qualify provider semantics. |
| 2 — export/reimport (blocking) | Owner-bound native-v2 raw payload form includes referenced, shared, empty and unreferenced bytes. One coherent export and one target CAS import; no persisted import-provenance growth or history replay. | P3; C24/C42/C46. Explicit size/count/entry bounds, integrity/scope validation, old-client refusal and maintenance export rule. Format/wire implementation and review still required. |
| 3 — lifecycle/refcount | Shared domain computes resulting same-scope counts; positive-to-zero deletion and named replacement reclaim quota in the same Update. Shared refs retain bytes; explicitly unreferenced puts need explicit deletion. No hidden provider GC. | P2/P6; C43 through C13–C16. Provider independently validates refs; temporary physical space/reclaim separately disclosed. |
| 4 — limits/recovery | 32 MiB/resource; 256 MiB raw/scope; 128 MiB Snapshot; 256 MiB events; 512 MiB ChangeSet; 1,024 MiB Checkpoint; 10,000 counts; 8 MiB/provenance. Changed bounds, not old parity. Reviewer mixed-tree totals and old 25 MiB upload justify headroom only. | P6 table; C19/C37/C46. Cleanup with event headroom; 90% event warning; terminal event stream stays read-only/exportable without reset, compaction or hidden rollover. Explicit recovery at every cap. |
| 5 — text-only | Provider opaque/binary-capable; domain rejects any NUL payload before put/import with resource_binary_refused, retaining old FS policy. No added payload UTF-8 restriction. Old remote binary behavior is deliberately narrowed. | P2/P4; C44 tests both layers separately. |
| 6 — strict FS/notes | Only new recognized-schema provider trees open; legacy stays old task-board. Unknown unrepresentable sections/incomplete pairs refuse unchanged. Openability is a later-script target only. Review's real notes reproduction is acknowledged; merged PR5 is composed. | P6; C31/C49; followups.md attributes 1242/1244 and 18-directory reviewer counts, names exact merged PR5. No private dataset run here. |
| 7 — rename/reparent | Rename epic/story containers, reparent story subtree, and cover descendant physical paths without adding logically unchanged descendants to ChangedIDs. | P6; C45 repeats every real C13–C16 stage, collision and recovery-kill/double-reopen; DB shares oracle. |
| 8 — history | Per-element activity history deliberately dropped. Actor + changed IDs cannot reconstruct field deltas/old values; imported activity is inert. | P5; C48. Future history needs separate reviewed scope/API/retention. |
| 9 — v1 routes | Exactly one explicit configured v1_product_id; absent disables v1, invalid fails startup. Full-pair authorization/internal revision binding; v2 never falls back. | P8; C30/C47; v1 resource export refuses without negotiated payload form. Wire/auth review remains. |
| 10 — FS time | Finite proposed 30 s reads, 300 s Update/Checkpoint, 2 s lock, 330 s Close, 600 s startup/recovery. Unmeasured, not a feasibility claim. | P6; C41/C50 require actual 10,000-element/large-byte/subtree clocks, exits, sync counts and temporary space. Failed allowed workloads hold qualification pending common design/budget review; expiry preserves outcome barriers/evidence. |

Accepted scoping, CAS, event identity/ordering and whole-transaction atomicity
remain. Resource lifecycle and capacity admission operate inside that decision.

The original reviewer **did not review database-brief.md, wire/auth in depth, or
every conformance oracle**. Its executed evidence was only an Encode scratch test
at board `a1b8716` and reviewer board measurements dated 2026-11-06, not executions
against PR4's documents. Those counts remain attributed evidence, not this
worker's measurements. The revised DB brief/wire/oracles need fresh review; the
old verdict does not cover them. C01–C50 remain unexecuted contract cases.
Document validation and a narrow Encode witness do not qualify native-v2, either
provider, durability, auth, Mac behavior, production transport/admission or
activation. **Zero runtime qualification is claimed.** Fresh independent review must bind the
signed new head/tree and document hashes before freeze; no adoption or landing is authorized here.
