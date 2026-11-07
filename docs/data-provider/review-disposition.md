# Contract review dispositions

This source-only revision preserves the ten original dispositions and addresses
the eight follow-up findings below. Independent delta review is pending; no
previous acceptance covers this revised head. The follow-up review was by
**Claude Opus 5.5**, in a session separate from the producer. Its supplied text
begins with finding 2; the source decision supplies blocking finding 1.

## Follow-up findings

| Finding | Disposition | Contract / future verification |
| --- | --- | --- |
| 1 — composition (blocking) | Compose merged PR6 main `4bb8406e025b1377426f15e7a78be72061c023e4`, preserving PR6 PAX and PR5 notes code unchanged. README, source-state, followups and decisions report both landed. | Bind revised docs subtree, full tree and signed one-commit head on that base; earlier exact-tree condition is superseded. No product implementation. |
| 2 — destructive import | Native-v2 restore is empty-target-only, as the root contract requires. Check no Elements/provenance/resource membership inside target CAS; fresh CAS cannot authorize stale-archive rollback. EnsureEmpty never clears a target. | P2/P3; C24/C42/C43/C49. Restore into same-scope matching-Board empty target on a fresh server preserves exact current facts/bytes, creates one local import event, no history replay. Nonempty target refuses `already_exists`. |
| 3 — streaming | ResourceInput uses a caller-owned one-shot io.Reader; ReadResource returns an owned io.ReadCloser; Checkpoint pins metadata/events and opens fixed-revision payload readers. P3 defines validation, EOF/read/close errors, all-path cleanup, contexts, no seek/retry, and bounded private spooling or immutable backing. | P2/P3/P6; C08/C33/C35/C36/C38/C41. Logical canonical encoding is streamable size/parity notation, never a whole-payload buffer interface. Both briefs use this contract. |
| 4 — reserved URN | Scheme and sha256 namespace match ASCII case-insensitively before generic URI validation. Valid variants reference same-scope bytes; malformed reserved forms always refuse, including missing colon or bad digest. | P2; C34 covers uppercase/mixed scheme/namespace, missing payload, truncated/empty/uppercase/nonhex digest, suffix/query/fragment and bare sha256. Existing allowlist alone is not sufficient. |
| 5 — USTAR provenance | V2 eligibility and new-state admission cap the single provenance name at 100 bytes while retaining existing ASCII grammar/path checks. Old-format validation/data is unchanged; ineligible export refuses before output, never truncates or uses PAX. | P3/P6; C19/C42 cover 99/100/101-byte ASCII names and multibyte/unsafe/empty controls. At-cap export headroom is for v2-eligible modeled state. |
| 6 — FS long writes | Preserve conservative bounded-lock reads: `unavailable` is allowed during an active write. Only that measured contention window's FS/DB read availability difference is excluded; successful reads are coherent and quiescent semantic results/errors match. No old-state serving implementation is claimed. | P5/P6; conformance parity rules, C13/C45. Recovery barriers stay explicit and are not a general error exclusion. |
| 7 — client deadline | Current 35 s/125 s client request budgets do not cover the proposed 300 s server operations. Later wire integration must align mutation/archive deadlines through streamed completion and retain unknown-outcome reconciliation. | P6; C41. No runtime timeout or product code changes in this draft. |
| 8 — terminal recovery | Original sealed scope stays read-only. Different scope starts empty and cannot accept its archive: no supported open-task carry-forward there. Explicit same-Scope matching-Board restore into an EnsureEmpty target on a fresh server is supported with fresh local history, without unsealing or automatic routing. | P3/P6; C42/C46/C49. Strict new-schema, legacy, scope and empty-target refusals remain. No migration or implicit cross-scope route. |

Fresh delta review must bind the revised docs subtree and proposed exact signed
landing head/tree, followed by the source owner's exact-head marker check.
The prior review did not cover database-brief.md beyond a keyword pass, every
oracle in depth (14 of 50 closely), or wire/auth beyond comparison with old
behavior. Its scratch execution only concerned finding 4 at `a1b8716`; it did
not execute this revised contract. Nothing here changes the review route or
claims an inherited ACCEPT, implementation, runtime qualification or landing.

## Original findings, retained with follow-up corrections

| Finding | Explicit disposition | Contract / future verification |
| --- | --- | --- |
| 1 — refused URI (blocking) | Owner-bound `urn:sha256:<64-lowercase-hex>`; bare sha256 refuses. Follow-up 4 adds reserved case-insensitive validation before existing allowlist/Encode checks. | P2; C33/C34. A narrow source witness does not qualify provider semantics. |
| 2 — export/reimport (blocking) | Native-v2 raw payload form includes referenced, shared, empty and unreferenced bytes. One coherent export and one empty-target CAS restore; no persisted import-provenance growth or history replay. | P3; C24/C42/C46. Explicit size/count/entry bounds, integrity/scope validation, old-client refusal and maintenance export rule. Format/wire implementation and review still required. |
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
