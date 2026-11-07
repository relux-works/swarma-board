# Fifty-case implementation and verification matrix

The frozen action/oracle text lives unchanged in
`conformance/testdata/conformance.json`. Every row below is **NOT EXECUTED for
filesystem and database**. “Control” means only test-only harness verification;
it is never adapter parity, crash persistence, native qualification or a case
acceptance result. Partial rows remain NOT_VERIFIED even when their implemented
assertions succeed. No case is skipped or weakened.

| Case | Layer | Shared implementation / remaining work | Harness control |
| --- | --- | --- | --- |
| C01 | provider | Provision twice at zero; exact empty checkpoint; seeded repeat unchanged | Executed |
| C02 | provider | Metadata conflicts; unknown scope refusals/zero result/callback count | Executed |
| C03 | provider | Three scopes, one update, exact other-scope checkpoint equality; reopen | Executed |
| C04 | provider | Invalid ID grammar across operations; expected full-scope mismatch | Executed |
| C05 | provider | Exact next revision/event; stale CAS zero callback/commit | Executed; broken CAS rejected |
| C06 | provider | Sixteen concurrent writers; deterministic leader held while followers start; one whole winner | Executed |
| C07 | provider | Sentinel callback identity and complete rollback; valid retry | Executed; rollback leak rejected |
| C08 | provider | View/checkpoint/retained ChangeSet detachment; one-shot input/output ownership | Executed; alias rejected |
| C09 | provider | Empty updates; nil preserve/explicit empty clear provenance; exact events | Executed |
| C10 | provider | Duplicate/disjointness/missing-delete/record/provenance refusals; exact old state | Executed |
| C11 | provider | Nil callback/Actor/canceled/expired inputs; callback pause then cancel | Executed |
| C12 | provider | Counter-injection refusal and pure overflow helper; valid full max-counter history conflicts finite cap | Helper only; backend fixture NOT_VERIFIED |
| C13 | provider | Real mapped pause points; concurrent View/resource/checkpoint old/new; measured FS-only unavailable; quiescent exact state | Real driver required |
| C14 | provider | Every mapped pre-decision failure, whole-envelope rollback, double reopen | Real driver required |
| C15 | provider | Post-decision kill and recovery kill, actual exit receipts, exact new state/double reopen | Real driver required |
| C16 | provider | Unknown outcome/zero commit, latched admission, recovered whole old/new; durable-decision justification still required | Real driver required |
| C17 | provider | Concurrent contiguous/immutable history plus stopped gap/duplicate/tail/scope/revision/order corruption | Concurrent portion controlled; real corruption driver required |
| C18 | provider | Reverse order, nested null/order/metadata, exact restart; Compare gate supplies cross-factory comparison | Executed; real comparison not executed |
| C19 | provider | Exact/one-over element/provenance counts/bytes/names, Snapshot bytes and complete event NDJSON through streamed HistoryDriver fixture | Fixture generator controlled; real boundaries not executed |
| C20 | provider | Schema/scope/recovery corruption and readiness refusal; retained physical evidence review remains | Real driver required; partial |
| C21 | provider | Admission stop, live-reader drain, idempotent Close and actual 330s paused-storage timeout/release; final-error injection remains | Not executed; partial |
| C22 | provider | Actual peer opener with measured finite refusal or two-handle CAS winner | Real peer required |
| C23 | server-domain | Shared lifecycle/query/plan/filter/count and foreign edges | Later shared integration |
| C24 | server-domain | Old formats preserved; native-v2/one Update/dry-run/v1-loss refusal | Later shared integration |
| C25 | server-domain | Pinned/unpinned pagination and query bounds | Later shared integration |
| C26 | server | Authentication/authorization/spoofing before provider access | Later shared integration |
| C27 | server | Current-policy/revocation linearization and approved capability adapter | Later shared integration |
| C28 | server | Immutable config/preflight/readiness, exactly one factory, no fallback | Later shared integration |
| C29 | server | Dead-service client boundary/no storage/open/start/retry | Later shared integration |
| C30 | server | Explicit v2 full-scope route/catalog/precondition | Later shared integration |
| C31 | provider | Root symlink refusal and real unsafe/partial/authority-dependent fixtures; source fidelity review remains | Real factory/driver required; partial |
| C32 | qualification | Native OS/filesystem/engine/power-loss/custody qualification | Separate authorized evidence |
| C33 | provider | Empty/abc one-shot puts, exact readers/catalog, reopen, idempotent membership, early close | Executed; unconsumed input rejected |
| C34 | provider | Full malformed URN/digest/size/hash/duplicate/dangling/delete matrix and valid case variants | Executed |
| C35 | provider | Same digest independent scopes; pinned old readers across delete/restart | Executed |
| C36 | provider | Input fault, pinned catalog, single child/reopen/close semantics; combined C13-C16 repetitions remain | Executed streaming portion; partial |
| C37 | provider | File/scope/count at/one-over and replacement at cap; whole-encoder guards verified separately, backend budget qualification remains | Encoder controls only; partial |
| C38 | provider | Real stopped payload/digest/size/catalog corruption plus mapped late reader/close failures through resource reads and checkpoint encoder | Encoder late-failure controls executed; real faults not executed |
| C39 | server | Resource authorization/bounds/no existence leak/fetch/path/fallback | Later shared integration |
| C40 | provider | Resource-only/mixed/idempotent exact event/catalog/bytes/external-ref order; Compare gate supplies cross-factory comparison | Executed; real comparison not executed |
| C41 | provider | Caller cancellation plus actual missing/short/long read/checkpoint lifetimes; C13 lock/C21 drain; startup/recovery timeout and wire alignment remain | Cancellation/control helper executed; real expiry not executed; partial |
| C42 | server-domain | Shared inside-CAS empty-target guard supplied; full archive round trip/tamper/nonempty/race/100-byte cases remain | Guard only; integration NOT_VERIFIED |
| C43 | server-domain | Last-reference lifecycle/replacement/quota and complete fault repetition | Later shared integration |
| C44 | server-domain | Provider opaque NUL/empty/non-UTF8 vs domain NUL refusal/export | Provider stream helper control only; integration NOT_VERIFIED |
| C45 | server-domain | Shared subtree moves/descendants/physical fault coverage/quiescent parity | Later shared integration + real faults |
| C46 | provider | Exact event headroom, one-byte overflow refusal, smaller event fills/seals, at-cap reads, distinct empty scope and real disk-full hooks; other-cap cleanup/restore composition remains | Fixture generator controlled; real backend/integration NOT_VERIFIED |
| C47 | server | Explicit v1 product binding/disabled route and independent v2 | Later shared integration |
| C48 | server-domain | Deliberately unsupported old activity/inert provenance | Later shared integration |
| C49 | provider | Literal markdown/provenance/EnsureEmpty/restart and strict unsafe-tree fixtures; native-v2/current-native admission conflict remains | Real factory/driver/integration required; partial |
| C50 | qualification | Actual 10000-element/max-byte/subtree durable-sync/deadline/kill workload, clocks/exits/resources/config | Separate real workload qualification |

`Report.RequireAll` and `Compare` refuse incomplete results. This draft's explicit
remaining actions are part of the handoff, not new adapter-specific expectations
or evidence that a backend passed. The original vector remains authoritative.
