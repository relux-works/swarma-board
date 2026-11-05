# swarma-board implementation contract v1

Architecture unit board-architecture; architect: architecture owner; 2026-11-05 UTC. **Agreed by this architect for implementation as a new product proposal; pending independent review, checks and owner-controlled signed landing.** This is neither an independent review verdict nor an upstream platform contract. This document is normative at the destination repository root. `decisions.json`, `file-ownership.json` and `task-decomposition.json` are companion machine-readable handoffs. The supplied `inputs/spec.md` acceptance cases A01–A15 remain required except the explicit deltas below. In a conflict this contract controls the implementation proposal; owners retain landing/cutover authority.

Source: private `relux-works/skill-project-management`, detached pin `09bac3d89822c9b83c04dff6483d7b2074097a82`. Fresh clone in `source/skill-project-management-pinned`; inspected first-party source only, read-only. Root/selected first-party AGENTS.md do not exist in the pinned tree; vendored AGENTS.md is outside the extraction scope. This architecture handoff authorizes no external communication, worker launch, secret access, implementation or review verdict. Evidence references below are paths and line numbers at that exact pin. The old product/live board remains unchanged and authoritative until authorized cutover.

## D1: one backend and one ownership model

Use Storm v3.2.1 over bbolt v1.3.4, as pinned in `tools/board-server/go.mod:6–12`, for both loopback and remote service. No SQL replacement and no client markdown/filesystem board store. Server-owned durable files are required storage, not a client filesystem mode. One process owns a private local data directory; one DB per project, registry of project handles. No shared live files between hosts, replicas or network filesystems. Bind and data directory are explicit deployment inputs. New module: `github.com/relux-works/swarma-board`, Go `1.25.5`, one root go.mod, no nested modules/go.work or whole legacy CLI transplant. Preserve applicable LICENSE/NOTICE and record extraction pin/file attribution.

Evidence: `internal/registry/registry.go:20–43,125–170` under tools/board-server already provides project handles, write mutex, DB creation and leases. `internal/store/storm_store.go:16–68,304–331` already supports transaction-scoped nodes and atomic reciprocal writes. This is evidence of reusable mechanisms, **not** proof all old mutations are atomic. Adapt them to the transaction contract below. The old registry's 0755 directory creation is not the new privacy default: data dir 0700, DB/temporary files 0600. Keep the bbolt file lock, finite 2s open timeout and graceful lease drain on shutdown.

Database schema version is `1`, metadata includes project ID and monotonic uint64 revision initially `0`. All reads see a single read transaction. All mutations use one write transaction containing expected-revision comparison, affected elements, hierarchy/edges, provenance if importing, revision increment, and audit. A board mutex may serialize requests but never substitutes for the database transaction. Do not call root DB Begin/Init inside an active transaction. Failed validation, callback error, storage error before commit or stale revision rolls back everything. No-op successful editor requests still increment revision and record one audit event; refused requests never do. Overflow refuses with `unavailable`.

Unknown/newer schema refuses opening/readiness; no silent legacy DB conversion. MVP has no migration framework: create new v1 DBs; import via snapshot. Versioned explicit migrations can be added when there is an actual v2. Back up each project using a consistent bbolt read-transaction copy including revision/audit/provenance; restoring is an offline operator action into a separate data directory, never overwriting a running service. No backup HTTP or scheduler is required.

## D2: product lifecycle

Stored statuses: `backlog`, `analysis`, `to-dev`, `development`, `to-review`, `reviewing`, `integrating`, `done`, `closed`, `blocked`. No status is inferred from workers/processes/sessions or a run ledger. An authorized editor may request any canonical status, including reopening, subject to these guards:

- Development/to-review/reviewing/integrating/done require every direct leaf blocker to exist and be done/closed. Missing never means satisfied. Closing a leaf is administrative cancellation and may bypass blockers; record it explicitly.
- Development and to-review require nonempty assignee. Entering development from another status for a task/bug with an explicit estimate requires `estimated`; imported legacy null estimate is grandfathered. Reasserting development does not re-take work, but still checks blockers/assignee.
- Epic has no parent, story has an epic parent, task/bug has a story parent. Parent IDs must exist in the same project and match type. No self/ancestor dependency or hierarchy cycle.
- A container can enter done/closed only if all descendants are terminal. Creating, reparenting or reopening an active descendant beneath a terminal ancestor refuses `dependency_conflict`; explicitly reopen ancestors first. Linking a new active blocker to an already done leaf refuses; administrative closed leaves may retain unresolved blockers, which plans diagnose.
- No automatic parent state changes or chain handoffs. Stored container status is explicit. Do not expose an ambiguous aggregate-status field in MVP; counts, children and derived dependencies supply container views.

Assignee is a task label, never an authenticated identity or seat allocation. Review `required|light|none` and task_class `code|metadata|docs|research` remain descriptive only; default required/code for newly created elements. Title and slug/name remain separate; create defaults title to name. New task/bug estimate is explicit `{kind:"notEstimated"}`, containers have null persisted estimate. Estimated is `{kind:"estimated",scale:{kind:"fibonacci",number:N}}`, N any nonnegative representable integer, matching `pkg/board/estimate.go:105–133`; do not invent a Fibonacci-membership restriction. `partiallyEstimated` is read-only container rollup, never a write value. Preserve imported null versus explicit notEstimated. Checklist is ordered.

Explicit semantic delta: remove integrating-checkpoint release and CR completion gates. `pkg/board/board.go:171–185` releases certain integrating siblings only with loader-derived immutable CR evidence. The new product contains no such authority and requires terminal blockers. No imported boolean/marker/actor substitutes for acceptance. Manual done records product facts; it is not review ACCEPT, commit acknowledgement or proof of landing. Source CR gates and source holds remain intact in the unchanged product. Source `validation.go:29–100` supplies estimate/assignee/blocker guards to extract selectively, not the CR call graph.

## D3: graph and planning

Canonical writable task edges are each leaf's `blocked_by`. `blocks` is computed inverse, not separately writable. No new container links in MVP. Preserve imported **manual container** edges separately as `manual_blocked_by` with advisory classification, never as leaf readiness authority. A task edge projects story/epic advisory dependencies; derived container links are never persisted as manual facts. Import checks supplied legacy reciprocal blocks against blocked_by; inconsistency refuses instead of repairing. Link/unlink is leaf-to-leaf only. Reparent validates hierarchy and all changed ancestor/dependency relationships within the same transaction and recomputes read projections.

Keep existing scope semantics: no scope means project; epic/story children includes its subtree leaves plus ancestor context; leaf children is that leaf; related adds bidirectional task-dependency closure to each seed; scope sets union/deduplicate each root's resolved scope. Default children for project/epic/story, related for task/bug. A mixed-type scope set with omitted mode reports `mixed` if resolutions differ. Unknown roots refuse; explicit empty set refuses. `active` defaults false; true removes done/closed phase nodes after resolving blockers against the **full** snapshot. Terminal blockers outside the selected scope are satisfied, not missing. Nonterminal outside-scope, dangling and terminal-element-with-active-blocker are distinct dependency problems.

Phases are numbered Kahn topological waves of leaf task/bug nodes. They are computed results, never scheduling/reservation objects. Containers list ancestor IDs of phase leaves. Critical path is the longest **edge-count** path in the DAG (estimate values do not weight it). Empty and all-isolated plans return `criticalPath:[]`; ties select the lexicographically smallest full-ID sequence among longest paths. Sort roots, neighbors, wave leaves, containers and diagnostics by canonical full ID. Full graph SCC diagnostics report only cycle members, not downstream nodes; also report a reproducible closed chain and inducing links. Task cycles and container projection cycles are separate; advisory container cycles do not prohibit a valid task plan.

If task cycles or dependency problems exist, return a diagnostic plan with `usable:false`, empty phases/path and full problems/cycles. Do not present partial phases as actionable. This is a deliberate clearer replacement for any legacy partial-plan behavior. HTTP 200 for a successful **diagnostic read** is allowed; CLI plan exits 1 when unusable. No mutation or execution occurs. Persisted data cannot acquire cycles/dangling edges through accepted API/import, but planner must handle corrupt/synthetic snapshots fail-closed.

Wire plan result is `{ "plan": { "scope": {"id":"", "type":"project", "name":"Project", "ids":[], "mode":"children", "active":false}, "usable":true, "summary":{"totalElements":0,"totalPhases":0}, "phases":[{"phase":1,"elements":[],"containers":[]}], "criticalPath":[], "dependency_problems":[], "task_cycles":[], "projection_cycles":[] } }`. Scope id is singular root, empty for project, or sorted roots joined by + for a set; ids is the sorted explicit roots (empty for whole project). Scope type is the singular element type, project, or set; name is singular title/name, Project for whole project, or Scope set (N roots). totalElements counts scoped leaf phase candidates after active filtering, including candidates diagnosed unusable. criticalPath is full ID strings. Cycle objects are `{members:[],chain:[],links:[{blocked_id,blocker_id}]}`. Dependency problems are `{elementId,blockerId,reason}` with source reason strings `out-of-scope`, `dangling`, `resolved-element-has-active-blocker`. Keep `blockedBy` task selector casing; phase rows show active blockers, while get/export preserve full stored edges. No compact-plan alternative contract.

Reuse `pkg/board/dependencies.go:30–134`, `dependency_cycles.go:71`, `plan/graph.go:109–193,310–355,392` and `query/operations.go:590–640` selectively. Existing unweighted algorithm uses `dist[id]+1`; map iteration tie order must be fixed.

## D4: HTTP, DSL and CLI

Routes below are the complete network allowlist. `boardID == project_id`, validated `[a-z0-9][a-z0-9_-]{0,63}`; reject path-shaped/percent-encoded separator variants. Element lookup is project-scoped and case-insensitive while preserving the stored stable ID; new canonical IDs have uppercase type prefix and lowercase base36 suffix. Preserve valid legacy sequential IDs on import; new IDs use source distributed TYPE-YYMMDD-six lowercase alphanumeric suffix format with cryptographic randomness and collision retry. IDs never change on rename/reparent.

| Route | Auth and result |
| --- | --- |
| GET /health | Liveness only; 200 `{status:"ok"}`. |
| GET /ready | 200 `{status:"ready"}` only when storage/schema and configured auth adapter initialized, otherwise 503 unavailable; no paths/config. |
| GET /v1/version | 200 `{product:"swarma-board",commit:<exact build commit>,contracts:{swarma_board_api:"v1",task_plan:"v1",snapshot_import:"v1",revision_precondition:"v1"}}`. Unstamped dev builds identify commit `dev`; qualification artifacts require exact commit. |
| GET /v1/me | Authenticated `{account_id:<principal ID>}`. |
| GET /v1/boards | Authenticated `{boards:[{id,name,description}]}`, only granted projects, sorted id. |
| POST /v1/boards/{boardID}/q?mode=json | Viewer reads / editor mutations, single DSL statement, UTF-8 text/plain body, operation result JSON and ETag. Other modes refuse. |
| GET /v1/boards/{boardID}/export | Viewer, native snapshot tar.gz at a committed revision, Content-Type application/gzip, ETag and `X-Snapshot-SHA256` of returned archive. |
| POST /v1/boards/{boardID}/import?dry_run=true | Project admin, application/gzip, validation/report only; no If-Match required, ETag identifies validation snapshot. |
| POST /v1/boards/{boardID}/import | Project admin, explicit dry_run=false or absent, empty target only, revision and digest preconditions; atomic result report. |

Unknown routes 404; unsupported method 405. No network project/user/token CRUD in MVP. Explicit offline startup fixture provisions synthetic projects/grants; production provisioning belongs to the held auth/deployment adapter. This replaces the spec's optional broad admin machinery and avoids implementing another identity system. Health/readiness/version contain no task/identity metadata. Other routes require Bearer auth, including loopback. No account from headers or query parameters.

One existing agentquery AST statement only, including for reads; reject multiple statements with 422 unsupported_batch before any work, and unknown operation/field/argument/preset with query_error/unsupported_operation. No extension registry loading at runtime. Parse/allowlist before repository callback; authorize before revealing project data. Arguments use existing agentquery quoting/enum/bool/int/list grammar; no shell evaluation. Only the first declared id argument may be positional; create's type may be positional, plan's scope may be positional; other args are named. Duplicate keys, mixed duplicate positional/named value, invalid booleans, invalid enums, numeric overflow and extraneous args refuse. No arbitrary nested selectors; flat listed fields and presets only.

| Read | Exact allowed arguments |
| --- | --- |
| get | id required |
| list | type,status,parent,assignee,blocked optional; skip default 0; take default 50, range 1..1000; stable full-ID ordering |
| count | type,status,parent,assignee,blocked optional; no pagination |
| summary | none |
| plan | scope optional ID or [IDs]; mode children/related optional; active bool default false |

Mutation signatures (`?` optional) are complete; retain legacy spellings but strip execution args:

```
create(type,name,parent?,description?,scope?,ac?,review?,task_class?)
set_details(id,title?,description?,scope?,ac?,review?,task_class?)
rename(id,name)
assign(id,agent)
unassign(id)
set_status(id,status)
set_estimate(id,estimate,scale?,number?)
link(id,blocked_by)
unlink(id,blocked_by)
check_item(id,item)
uncheck_item(id,item)
add_checklist_item(id,text)
remove_checklist_item(id,item)
set_notes(id,text,set=false)
set_parent(id,parent)
delete(id,confirm=true)
```

At least one optional update field is required for set_details; omitted keeps value, explicit empty clears text. Name is nonempty and normalized by existing source slug rules; title is never overwritten by rename. Assign requires nonempty label, unassign explicitly clears. Estimate estimated requires scale=fibonacci and number; notEstimated forbids scale/number; leaf-only. Checklist item is 1-based and in range. Notes append text separated by newline when existing text is nonempty; set=true replaces. Resource-reference writes are not exposed in this MVP; import/export/read preserve them. `delete` is **leaf-only** with literal confirm=true, no children and no other element referencing it (including manual advisory edges); otherwise dependency_conflict. Unlink first; no cascading delete. This intentional source delta avoids implicit subtree/data loss and ambiguous confirmations. No automatic mutation dry-run engine. Import dry-run remains required.

Allowed element selectors: `id,type,name,title,status,review,task_class,estimate,parent,assignee,description,scope,ac,blockedBy,blocks,derivedBlockedBy,derivedBlocks,checklist,notes,created,updated,isBlocked,isAdvisoryBlocked,children,preconditionResources,outcomeResources`. Checklist rows `{text,done}`; resources `{name,uri,description}`. Null estimates remain distinguishable in snapshot; leaf query estimate normalizes null to notEstimated, containers use existing read-only estimate rollup from the source. Strings absent in imported legacy data render empty; arrays render [] not null. created/updated are UTC RFC3339 or empty for explicitly missing legacy times. No observability/runtime/checkpoint fields.

Presets: minimal=`id,status`; default=`id,name,status`; overview=`id,name,status,assignee,parent`; planning=`id,name,status,blockedBy,blocks,derivedBlockedBy,derivedBlocks,parent`; progress=`id,status,review,task_class,estimate,checklist,notes,blockedBy,derivedBlockedBy,isBlocked,isAdvisoryBlocked`; full=all allowed selectors in the order above. Default selector is default. get/list/plan accept selectors; count/summary/mutations reject selectors. get result `{get:<row>}`; list `{list:[rows]}`; count `{count:N}`; summary `{summary:{total:N,byType:{epic:N,story:N,task:N,bug:N},byStatus:{<every canonical status>:N}}}`; mutation `{<operation>:{id:<affected ID>,changed_ids:[sorted IDs]}}`. These envelopes are newly frozen product v1 shapes; do not inherit undocumented agentquery wrapper variants. Plan shape is specified above. No search/agents/models/activity/ledger/handoff/normalization/resource-upload operations. Their absence is intentional scope control, not stubs that return success.

For every project read and mutation return `ETag: "rev-N"` and `X-Request-ID`. Client mutation must send exact strong `If-Match: "rev-N"` from a prior selected-project read; absent 428 revision_required, malformed 400 invalid_request, stale 412 revision_conflict with safe current_revision. Wildcard, weak and multi-value tags refuse. Dry-run report/export/read do not increment revision. User may supply CLI `--if-match N`; otherwise client first performs authorized `count()` and uses its revision for the single mutation, never silently refreshing after 412. `q` is read-only and `m` mutation-only, checked locally and server-side allowlist. Network outcome unknown: return transport_error and advise read/reconcile; never automatically retry writes.

Error JSON: `{"error":{"code":"...","message":"...","details":{}},"request_id":"..."}`; details contain safe IDs, current revision or bounded validation facts, never text bodies/internal paths/credentials. 400 invalid_request; 401 unauthorized (missing/invalid/revoked/expired/disabled identity); 403 forbidden (authenticated project role insufficient or project ungranted, same result for nonexistent ungranted project); 404 not_found for element/authorized project missing; 409 already_exists/dependency_cycle/dependency_conflict; 412 revision_conflict; 428 revision_required; 413 payload_too_large; 422 query_error/unsupported_operation/unsupported_batch/invalid_parent/blocked/estimate_required/not_assigned/invalid_snapshot/snapshot_version_unsupported; 500 storage_error; 503 unavailable. No HTTP 200 rejected mutation, no in-band error result treated as success. Parser/error output must not echo the full query.

CLI binary `swarma-board`: q QUERY, m QUERY, plan [ID] (`--scope` comma-separated roots mutually exclusive with ID, `--mode`, `--active`, `--critical-path`, `--phase N`, `--json`), import FILE (`--dry-run`, `--sha256`, `--if-match`, `--accept-exclusions`), export FILE, version (`--server` for network discovery). Global `--json`; mutation --if-match supported. `--critical-path` and `--phase` are mutually exclusive views of one fetched plan, never new server algorithms. CLI errors JSON use the same envelope with safe generated request ID. Exit 0 success; 2 local argument/config/contract/account mismatch; 3 auth/access refusal; 4 stale/conflicting state (409/412); 1 other errors including unusable plan. No local board mode, TUI, service autostart, session/scheduler/ceiling/spawn commands, renderer/viewer launch or inherited config export. Server lifecycle is explicit separate binary `swarma-board-server`; it never launches workers.

## D5: narrow trust seams and transactions

The following interfaces are **new local product composition seams**, not assertions about swarma-platform/configurator APIs. Baseline producer materializes them verbatim in internal/contract before the four implementation branches. No external platform import is permitted until its owner supplies a repository pin and accepted adapter contract.

```go
// package contract; imports only context, encoding/json, io, time.
type Revision uint64
type Projection struct { Endpoint, ProjectID, AccountID string }
type ProjectionSource interface { Resolve(context.Context) (Projection, error) }
type CredentialSource interface {
    Bearer(context.Context, Projection) (string, error)
}
type Principal struct { AccountID string }
type Authorizer interface {
    Authenticate(context.Context, string) (Principal, error)
    Require(context.Context, Principal, string, string) error // project, viewer|editor|admin
    Projects(context.Context, Principal) ([]Project, error)
    Ready(context.Context) error
}
type Project struct { ID, Name, Description string }
type Operation struct {
    Name string
    Args map[string]json.RawMessage // decoded typed values; id/scope positional normalized to names
    Fields []string                // expanded, validated selector names
}
type Parser interface { Parse(string) (Operation, error) }
type AuditContext struct { AccountID, RequestID, Operation string; At time.Time }
type Snapshot struct {
    Project Project
    Revision Revision
    Elements []Element
    Provenance []ProvenanceFile
}
type ChangeSet struct {
    Upserts []Element
    Deletes []string
    Provenance []ProvenanceFile // import-only replacement; ordinary mutations leave nil
}
type Commit struct { Revision Revision; ChangedIDs []string }
type Repository interface {
    View(context.Context, string) (Snapshot, error)
    Update(context.Context, string, Revision, AuditContext,
        func(Snapshot) (ChangeSet, error)) (Commit, error)
    Ready(context.Context) error
    Close() error
}
type Call struct {
    ProjectID string
    Operation Operation
    Expected *Revision // required on writes, nil on reads
    Audit AuditContext // authenticated account and server-generated time/request ID
}
type Reply struct { Revision Revision; JSON json.RawMessage }
type PreparedImport struct {
    SHA256 string
    Snapshot Snapshot
    Report ImportReport
}
type ImportOptions struct { ProjectID, ExpectedSHA256 string }
type ImportReport struct {
    SHA256, Format string
    Version int
    ElementCount int
    Exclusions []string
    Warnings []string
}
type Transfer interface {
    Prepare(context.Context, io.Reader, ImportOptions) (PreparedImport, error)
    Encode(context.Context, io.Writer, Snapshot) error
}
type Service interface {
    Execute(context.Context, Call) (Reply, error)
    Snapshot(context.Context, string) (Snapshot, error)
    Import(context.Context, string, Revision, AuditContext, PreparedImport) (Commit, error)
}
type ProvenanceFile struct { Path, SHA256 string; Data []byte }
type Error struct { Code, Message string; Details map[string]any }
// Error implements error; no HTTP dependencies. Status/exit mappings belong to adapters.
```

Element and nested Estimate/ChecklistItem/ResourceRef types live in internal/contract, fields exactly match the native element record below with JSON snake_case tags. Go names: ID, Type, Name, ParentID, Title, Description, Scope, AcceptanceCriteria, Status, AssignedTo, CreatedAt, UpdatedAt, BlockedBy, ManualBlockedBy, Checklist, Notes, Estimate, Review, TaskClass, ResourceRefs. CreatedAt/UpdatedAt use *time.Time to preserve missing legacy timestamps; Estimate *Estimate; scalar enums use strings; slices are concrete types. Estimate `{Kind string; Scale *EstimateScale}`, EstimateScale `{Kind string; Number int}`, ChecklistItem `{Text string; Checked bool}`, ResourceRef `{Name,URI,Description,Type string}`. Contract types carry data only. No dependency on chi/Cobra/agentquery/Storm. No domain/framework interface zoo or generic DI container.

Service receives repository+clock+ID generator; Execute is implemented by domain owner using query parser/selector helpers. Server authenticates and checks operation role before Service; Service checks AccountID nonempty on mutations as defense against accidental untrusted in-process callers. Service is internal trusted code, not a second auth boundary. Repository owns transaction callback lifetime: supplies detached snapshot inside the same write transaction, applies returned ChangeSet plus revision/audit, commits once, never re-invokes callback. Callback must not do network, I/O, nested repository calls, side effects or launches. Validate and compute all changes against its provided snapshot, not a prior View. Repository validates duplicate IDs/upserts/deletes defensively; domain owns product invariants. Domain captures response payload in callback and returns it only if Commit succeeds. Reads use one View then compute entirely in memory.

Import Prepare decodes/validates structure/checksums with no repository access. Domain Import validates complete candidate graph/lifecycle field values without replaying status transitions, then calls Repository.Update and enforces empty target inside the callback; all imported elements/provenance/audit appear together. Authorizer and import report/bytes are not persisted grants. No full-board replacement endpoint is exposed. In-process untrusted data never supplies AuditContext account/time/request ID.

Client composition resolves exactly endpoint/project/account, separately resolves credential, verifies endpoint, calls /v1/version, then /v1/me and compares account before project requests. Missing/invalid/ambiguous projection, credential, adapter, capability or account mismatch fails closed before writes. No ambient env/token discovery, cwd board fallback, runtime ownership or another identity's credential file. CredentialSource returns token in memory only; do not export it to child environment, include in URL/config/output or persist it as board data. Never follow redirects with credentials; reject all redirects for authenticated client requests. Nonloopback endpoint requires verified HTTPS; no TLS-disable flag. Local HTTP is allowed only for literal 127.0.0.1 or [::1], never a DNS name assumed local.

Production configurator/auth source U1 is unresolved and **does not block synthetic implementation**. Ship explicit injected in-memory/fd-backed synthetic adapters under internal/testadapter with test/example-only values. CLI synthetic harness accepts an explicitly named projection fixture and credential fd; server synthetic harness accepts an explicitly named fixture of projects, accounts, SHA256 token hashes, expiries/disabled flags and grants. Projection fixture JSON has exactly endpoint, project_id, account_id. Server fixture JSON is {schema_version:1,synthetic:true,projects:[{id,name,description}],accounts:[{id,disabled}],tokens:[{sha256,account_id,revoked,expires_at}],grants:[{account_id,project_id,role}]}; expires_at is UTC RFC3339 or null. Unknown fields, duplicate identities/grants/hashes, dangling references and invalid roles refuse startup. The explicit credential fd contains one UTF-8 bearer string with an optional trailing newline and a 4096-byte maximum, is consumed once and closed; it is never discovered from environment or paths. Synthetic project fixtures create only missing empty v1 projects; they never rewrite existing project content. Grant/account/token fixture changes are evaluated at explicit service restart; production dynamic provisioning/revocation adoption remains held. Synthetic server binds literal loopback only, rejects nonloopback even with fixture; remote-style adapter coverage uses httptest verified TLS. No automatic demo accounts or default credentials. Server with no configured Authorizer returns startup error before opening listener; CLI with no configured projection/credential adapter errors. Normal production entrypoints remain fail-closed until an owner-supplied adapter is independently reviewed. Fixture mode is visibly marked synthetic, cannot certify production adoption, and never reads real account files. Exact flags: CLI `--synthetic-projection FILE --synthetic-credential-fd N`; server `--synthetic-fixture FILE --listen 127.0.0.1:PORT --data-dir DIR`. Fixture secret hashes only, raw token is supplied by the explicitly opened client fd. These are local test harness contracts only.

Reuse hash-only credential validation/revocation/expiry/disabled-account logic from tools/board-server/internal/auth/service.go:109–156 as evidence, not its bootstrap/admin system wholesale. Every request checks current grants; roles viewer<editor<admin, explicit per-project grant required even for admin. No global admin bypass or trusted identity proxy header. No permissive CORS; reject Origin-bearing project mutation requests, including import. Require exactly one Bearer Authorization header; cookies never authenticate. Request logger records ID, route template, status, operation name and affected IDs/counts, never query/body/header values/resource payloads.

Limits: query 8 MiB; import 64 MiB compressed, 256 MiB expanded including headers/padding, 8 MiB per regular entry, 10,000 entries; body plus response client read max 64 MiB for JSON, 64 MiB compressed export. Limit board to 10,000 elements for this bounded MVP and fail above cap. Header read timeout 5s; query timeout 30s; import/export 120s; client corresponding timeout 35/125s; server max headers 32 KiB. Limits are safety/transport bounds, not worker ceilings. Check context before beginning and before commit; after successful commit report success even if the request later disconnects, leaving read/reconcile to client. No paid/runtime/provider integrations.

## Snapshot/import contract (D4/D5)

Native v1 is tar.gz containing exactly `.swarma-board-transfer.json`, `elements.json`, and optional `provenance/<safe-name>` files. No binary resources in MVP. Native manifest fields: `format:"swarma-board"`, `version:1`, `source_commit` string, `project_id` string, `revision` uint64, `redaction_manifest_sha256` (empty for native synthetic data), `files:[{path,sha256}]` sorted by path. Manifest lists every other regular entry exactly once, never itself; reject missing/unlisted/duplicate entries. elements.json is `{schema_version:1,elements:[...]}` sorted by ID. Native project_id must match explicit target, no implicit remap; revision is export provenance, import creates target revision N+1 rather than copying it. Source commit identifies exporter build; imported original source digest survives in provenance. Domain Import appends one inert provenance/import-SHA256.json record containing archive_sha256, source_format, source_version, source_project_id, source_revision, source_commit and exclusions from the validated input/report (never raw credential/config/authority fields). Identical existing provenance is retained, collisions with different bytes refuse. PreparedImport.Snapshot.Project.ID and .Revision carry validated source identity/revision; Transfer adds source format/version/commit as a validated provenance record so no parser-specific metadata interface is required. Native roundtrip preserves original provenance; import audit uses the current authenticated actor only.

Each element record has exactly these fields, all required except review/task_class and with explicit empty arrays/text allowed:

```
id,type,name,parent_id,title,description,scope,acceptance_criteria,status,
assigned_to,created_at,updated_at,blocked_by,manual_blocked_by,
checklist:[{text,checked}],notes,estimate,resource_refs:[{name,uri,description,type}]
```

Optional descriptive review/task_class values are the D2 enums. created_at/updated_at are null for missing legacy value or UTC RFC3339; do not manufacture historical timestamps. estimate is null for legacy absent or the D2 object. No persisted blocks/derived edges/checkpoint/authority fields. manual_blocked_by is permitted only on containers, blocked_by only on leaves. Resource type precondition/outcome, URI credential-free https or urn only; URI userinfo/query/fragment and unsafe schemes refuse, never fetch. Archived binary/local path resources or unresolved required resources yield invalid_snapshot; hold real adoption rather than dropping payload. Names, ordering, text and references survive native roundtrip. JSON duplicate keys, unknown fields, invalid UTF-8 and trailing data refuse. Provenance files are inert, size-bounded, checksummed bytes; they are never executable, live audit or grants.

Legacy adapter accepts inspected task-board tar.gz v2 `.task-board-transfer.json` `{format:"task-board",version:2,activity:[{element_id,path,sha256}]}`, element hierarchy README.md/progress.md, and manifested sanitized `.activity/<ID>/events.ndjson`. Extract parsing knowledge only from pkg/board/markdown and transfer, not the filesystem store or native-context legacyimport command. Preserve full README/progress text fields/checklist/notes, IDs/hierarchy/status/assignee/estimate and classified manual container edges. Known product sections are parsed; any unknown meaningful section/field or unrepresentable product content blocks with bounded report, rather than disappearing. Supplied reciprocal links must agree. Source activity hashes must match; preserve sanitized history as opaque inert provenance, explicitly not authenticated authorship/live audit. Do not regenerate hashes/signatures in importer.

Known removed execution metadata keys: integrationCheckpointed, change_request lifecycle/revision/state/evidence, run_id, session_id, provider, model, commit_ack, txn_id. If represented as recognized structured legacy metadata they appear in report Exclusions and have no authority. Execution directories/files (.runtime, .runs, native session payloads), reserved marker authority in notes, credentials, unmanifested activity and unknown artifacts **refuse** pending upstream sanitization; importer never silently strips arbitrary user notes. Every nonempty exclusion list requires explicit CLI `--accept-exclusions` and matching `X-Import-Accept-Exclusions: true` for commit (dry-run always reports); importer revalidates identical digest at commit. This header is consent to listed data exclusions, never permission/auth. Source provenance text claiming roles/acceptance is inert.

Real legacy snapshots require owner-authorized sanitizer custody bundle identifying source/exporter pin, archive SHA256, target project, explicit assignee mapping, redaction manifest/digest and resource decisions. No default reassignment: preserve labels unless authorized mapping explicitly supplied in the sanitized input. This unit has no real snapshot. Synthetic fixture does not establish custody for real data. Refuse real adoption pending that bundle.

Use archive/tar and compress/gzip streams; no shell extraction or URL fetch. Reject traversal, absolute/backslash/NUL/noncanonical/duplicate paths, symlinks/hardlinks/devices/special entries. Canonical directory entries ending / may be skipped after path validation and counted toward entry/header/expanded limits; they are not files in the manifest. Reject concatenated gzip members and trailing archive data; count decompressed bytes including headers/padding, not just claimed file sizes. Enforce compressed/expanded/per-entry/count limits while reading, before allocations; no file writes needed. Native deterministic export orders paths/IDs and uses zero gzip timestamps; exports exceeding bounds refuse. Unknown meaningful product fields never silently drop.

Dry-run returns `{import:{sha256,format,version,element_count,exclusions:[],warnings:[]}}` plus ETag, with zero writes. Commit requires `X-Snapshot-SHA256` exact lowercase 64-hex archive digest matching recomputation, strong If-Match, empty target under transaction and explicit accepted exclusions when present. Absence/mismatch digest is invalid_snapshot; commit validates every time, no server staging token/cache. Repeated import fails stale revision or target-not-empty, never duplicates. Success `{import:{revision:N,element_count:K,sha256:D}}` and ETag. Native reimport preserves facts/graph/planning and inert provenance, not live audit identity/revision.

## Implementation packets, scope reductions and gates

`task-decomposition.json` replaces the spec's serial B03→B04→B05→B07 chain with one small type/fixture baseline then four independent real producers: domain/query, server/storage/auth adapter, client/CLI, transfer. Each compiles/tests against contract interfaces and synthetic fakes without waiting for another implementation. Integration owner alone edits shared go.mod/contract/composition. Package ownership is exclusive, not permission to edit the source product. No extra orchestration framework, message bus, migration framework, durable phase objects, HTTP admin UI, render pipeline or 40 artificial tasks.

Counterproposals to spec: freeze one product API contract and minimal fixtures rather than wait for unknown upstream U1; preserve four advertised capabilities but no separate registry/framework; use offline explicit synthetic provisioning rather than broad admin/token API; leaf-only confirmed delete rather than implicit subtree cleanup; no general mutation dry-run or generic schema migration engine; no ambiguous stored-vs-aggregate status API; no binary resource backend absent an authorized payload requirement; no new skill until interface/dogfood evidence exists. Each reduction removes coupled work without weakening DAG, task facts, transaction/auth guards, sanitized import or independent landing gates.

Implementation can begin immediately on the architecture proposal and synthetic seams once the orchestrator supplies the private destination base. U1 blocks production adapter/adoption, not these packets. Real snapshot/resource mapping/custody blocks real import/dogfood; independent review plus required checks and authorized signed landing block release; owner grant blocks cutover/remote deployment. This unit creates no repository, PR, worker, Matrix message, credential, source edit, product implementation, deployment or review verdict.

## D6: approved transport, task journal and adoption delta (P5)

Authority: approved integration requirements define these semantics. This section overrides earlier production-local TCP and universal Bearer wording. The frozen P1–P4 HTTP implementation remains an explicitly synthetic harness; D6 production transport/journal qualification is follow-up work, not a claim about that harness. Shared Go `Repository`, `Service`, `Transfer`, `Operation`, `Call`, `AuditContext`, `ChangeSet`, `Commit`, `Snapshot` and native-v1 stay unchanged.

**Local kernel identity / remoteTLS.** Production local endpoints use HTTP over `unix:///absolute/socket/path`, with no authority, userinfo, query, fragment, relative/noncanonical path, or TCP fallback. Internal HTTP authority is fixed `swarma-board.invalid`. Production Unix clients resolve the same three-field Projection once and never call CredentialSource or send Authorization. Server rejects Authorization on Unix; UID is obtained only from Linux SO_PEERCRED on the accepted connection and stored under a private server context key. A missing peer credential refuses authentication. Map that UID to an enabled account through Host-protected policy and check mapping plus current project grants on EVERY request, including keep-alive. No query, cookie, account/UID/proxy header or projected AccountID establishes identity. Version/me/project/account preflight, routes, roles, ETags and revision rules remain unchanged. Remote endpoints require verified `https://host[:port]`, the accepted production credential adapter and exactly one Bearer header. TLS alone is not account identity. Literal-loopback HTTP remains explicit synthetic-only; no production localhost TCP listener. All redirects remain refused.

Host supplies service UID, distinct protected client UIDs or a separately approved authentication design, runtime/socket ownership/mode policy, UID reuse/revocation, mapping/grants authority, storage/backup, TLS trust and credential custody. A socket directory may permit intended client UIDs without exposing private service storage. Validate mapping/runtime readiness before accepting requests. Never unlink an arbitrary occupied socket path. Agents sharing OS UID `agent` are indistinguishable by SO_PEERCRED; no per-agent isolation claim or multiple identities inferred from projected AccountID is permitted. Production U1 repository/pin/API and these Host policies remain external gates.

**Task-mutation journal and separate signed export.** Each successful editor mutation, including no-op and import, commits state, revision and exactly one append-only journal event atomically. Revision N has events 1..N. Refusal/rollback appends none. No prior-event update/delete API, second-file append, external signer call in transaction, git-state commit or obligation-journal coupling. Event contains format version, project ID, revision, authenticated account, server-generated request ID/time, operation, sorted changed IDs and SHA256 of canonical committed change bytes including provenance. Imported activity/signatures remain inert provenance, never current authorship/grants or journal rows. Backup includes the journal. Application append-only semantics do not prevent a privileged storage administrator rewriting the DB; independently retained signed checkpoints are a distinct trust layer.

P5 inspection: current schema-v1 store.AuditRecord has Revision, Context, ChangedIDs, but no format version/project/change digest. It commits atomically; it does not yet meet the full D6 journal. DB.Audit and DB.View are separate reads, not an export checkpoint. Any persistent format delta requires explicit approved schema/new-empty-project disposition. Never silently reinterpret schema v1 or fabricate historical identity/digests. The following format is frozen for a proposed schema-v2 follow-up; the schema transition itself remains held pending root approval.

Canonical change format `swarma-board-change-v1`: exact compact UTF-8 Go encoding/json bytes, no trailing newline, default HTML escaping, struct keys in this order: `format_version` (1), `upserts` (contract.Element records sorted by bytewise ID), `deletes` (IDs sorted bytewise), `provenance` (null for unchanged, otherwise full replacement array sorted by Path). Upserts/deletes use explicit [] when empty. Element fields use the exact native-v1 contract.Element tags/declaration order; optional fields retain their omitempty semantics, nullable facts stay null, stored order of checklist/resource refs/edge lists remains unchanged, timestamps are UTC RFC3339Nano. Provenance rows have keys `path`, `sha256`, `data`; data is standard padded base64 of exact stored bytes, sha256 is lowercase SHA256 of those bytes. Validate declared digest, unique paths/IDs and disjoint upserts/deletes. Do not alter committed facts just to canonicalize a digest. Changes are detached before sorting. Include an empty successful change and nil-versus-empty provenance golden witness. Digest is lowercase SHA256 of these exact bytes.

Journal NDJSON `swarma-board-task-audit-v1`: one compact JSON object plus LF per ascending revision, keys in this order: `format_version` (1), `project_id`, `revision`, `account_id`, `request_id`, `at` (UTC RFC3339Nano), `operation`, `changed_ids` (sorted, explicit []), `change_sha256`. No BOM/blank lines/trailing JSON. Store-owned `AuditEvent` may hold these data; public shared interfaces do not change.

Offline server maintenance export is separate from board CLI, public routes and native-v1. Host stops the writer; retain an exclusive directory ownership lock, open only an existing approved DB read-only with bounded lock timeout, no initialization/fixture provisioning/Storm metadata writes. Refuse busy/missing/invalid schema. One store.ExportView read transaction returns detached snapshot and events ending at the same revision. Bound journal reading/NDJSON at 64 MiB before unbounded allocation; reject overflow, never truncate. Existing Transfer.Encode produces `snapshot.tar.gz` (64 MiB compressed limit); `task-audit.ndjson` carries the journal. Manifest format `swarma-board-audit-export-v1` contains, in that key order, `format`, `version` (1), `project_id`, `revision`, `exporter_commit` (exact stamped build), `journal_first` (0 if empty else 1), `journal_last` (revision), `snapshot_sha256`, `journal_sha256`, `signer_key_id`. Exact compact UTF-8 encoding/json bytes with no newline are signed by a designated EXTERNAL custodian. Native-v1 archive/export route never claims to be a signed audit export. Signer algorithm, signature container, key identity/trust anchor, revocation/rotation, custody and retention remain owner decisions. Do not infer trust from a supplied signature, use the SSH bot landing key, introduce private signer material into storage, or report signed success before approved-key verification. Tamper/wrong-key/unavailable-signer witnesses require that approved contract.

**Dead service / adoption / rollback.** Host/launcher starts, supervises and stops the separate service. CLI never spawns service/session/worker, never falls back to local board files and never retries writes. Missing socket, refused connection, bad TLS or dead service yields transport_error / exit 1 and safe Host/launcher recovery guidance; interrupted write means unknown outcome, read/reconcile. Invalid/missing mapping/adapters refuse startup/readiness (HTTP 503 unavailable), without a listener on missing production authority. Current synthetic client has nonzero refusal/unknown-outcome behavior; Host-specific wording and Unix paths remain its owned follow-up.

Before writer switch: record original source-custodian extraction notification receipt or explicit owner disposition; obtain authorized sanitized snapshot/digest, custody/redaction/resource/assignee decisions; dry-run and import into NEW EMPTY target, reconcile fields/counts/IDs/edges/plans/exclusions and prove backup/restore. Freeze source writes, obtain/reconcile final checkpoint into a fresh target if source changed, then owners authorize exactly one writer after independent review/checks/signed landing. No dual writes. Rollback stops new writes and captures a fresh post-cutover snapshot+journal checkpoint read-only, signed when approved custodian is available. Missing signer preserves explicitly UNSIGNED evidence and does not satisfy signed-export acceptance. Dead service recovery is Host offline recovery/consistent backup, never CLI spawning. Stale old writer stays disabled until all post-cutover mutations are reconciled and owners separately authorize resuming writes.

D6 qualification: T1 real distinct UIDs/current keep-alive revocation/spoof refusal and explicit same-UID limitation; T2 equivalent facts/plans on Unix and verified remoteTLS, synthetic labeled; T3 dead service/mapping/TLS refusal with no spawn/fallback/retry; T4 atomic exact-one journal and unchanged old rows under CAS/fault/kill/restart; T5 bounded read-only coherent export and approved-trust signature/tamper tests; T6 final pre-switch reconciliation and fresh read-only rollback checkpoint retaining post-cutover mutation. Synthetic acceptance never closes Host/U1/signer/custody/cutover gates.

### Transport successor implementation status (synthetic qualification)

The composed successor uses the delivered package APIs, preserving shared Go
interfaces. Local server composition installs `httpapi.UnixConnContext` on the
actual accepted Unix connection and one `auth.NewPeerAuthorizer` for both
`Authorizer` and `PeerAuthenticator`. Its resolver implements `ResolvePeer`,
`Require`, `Projects`, `Ready`; production protected authority remains external.
Remote TLS uses a separate Bearer handler without that hook. Client composition
injects `*client.UnixTransport` from `client.NewUnixTransport(endpoint)`;
`SyntheticHTTP` is an explicit test-only opt-in. The delivered internal Unix
HTTP authority is `swarma-board.local`; it is never DNS-resolved or dialed over
TCP. These concrete APIs supersede F1's proposed constructor names only.

`store.AuditEvent` currently aliases existing v1 `AuditRecord`. `ExportView`
returns coherent bounded v1 events, not the proposed digest-bearing v2 journal.
Schema migration remains HELD; no missing historical fields are invented. The
maintenance/export producer owns its separate package; external signing custody
remains pending.
New synthetic transport tests cannot satisfy distinct-UID T1, full v2 T4,
approved-signer T5, production adoption or T6 cutover requirements.

### Offline maintenance implementation status

Completed maintenance-producer component `37d189daecdeb98c862925c44c02f4acdf82bc20` is integrated
unchanged. Server `maintenance-export` dispatches before service initialization,
requires an explicit existing database, project, new private output directory,
exact build commit and Host stopped-writer assertion, and uses only read-only
export plus structural/hash verification. It publishes three exact detached
streams via private staging and Linux amd64 atomic no-replace publication.
Host maintenance ownership must exclude restart and path replacement beyond the
database handle lifetime; a read-only bbolt lock also excludes an active writer.
No service stop/spawn/resume, local client fallback, public maintenance route,
schema initialization or implicit migration is added. The checkpoint stays
unsigned. No protected signer policy is configured or inferred. Synthetic CLI
export/reimport and contention witnesses do not close production signer, UID,
Source custody, filesystem crash-recovery or cutover gates. See integration
README for publication failure semantics and inherited resource limits.
