---
name: swarma-board
description: Query and edit task facts, read plans, and validate/import/export snapshots through the swarma-board root client. Use for CLI configuration, revision conflicts and unknown write outcomes; scoped server-side storage adapters use swarma-board-dataprovider.
---

# Board client

Use `swarma-board` against an explicitly selected service, project and account.
The client has no local board store, service autostart or write retry. For
provider CAS, resources or adapter development, use the separate
[data-provider skill](../swarma-board-dataprovider/SKILL.md).

Before a service operation, read [connection selection](references/client.md#connection-selection).
It distinguishes the shipped Unix deployment path from synthetic qualification
inputs; configuration is never discovered from home, cwd or environment.
`version` alone is local; `version --server` performs service preflight.

## Choose the operation

These are product arguments after the explicit connection inputs:

| Intent | Arguments | Effect |
| --- | --- | --- |
| Read facts | `q 'get(TASK-a){full}'`, `q 'list(type=task,take=50){overview}'`, `q 'count()'`, `q 'summary()'` | Read-only; viewer access. |
| Read a plan | `plan TASK-a --mode related --active`, `plan --scope TASK-a,TASK-b --critical-path`, `plan --phase 1` | Read-only; phase and critical path are views of one fetched plan. |
| Edit facts | `m 'set_notes(TASK-a,text="Reviewed",set=false)' --if-match 7` | One mutation; editor access; revision 7 is an example, not a default. |
| Validate/import | `import FILE --dry-run`; then an explicitly authorized commit | Project-admin access even for validation; see [snapshot transfer](references/client.md#snapshot-transfer). |
| Export | `export NEW_FILE` | Viewer read on the service, plus a new private local archive. |

`q` accepts only `get`, `list`, `count`, `summary`, `plan`; `m` accepts only
mutations. Supply one quoted DSL statement, with named arguments except the
first ID, create's type or plan's scope. Get/list/plan support flat selectors;
count/summary/mutations do not. Use the [CLI validator](../../../internal/cli/validate.go)
and [D4 grammar](../../../contract.md#d4-http-dsl-and-cli) for other signatures,
fields, presets and lifecycle guards. There is no mutation dry-run flag.

For `plan`, positional ID and `--scope` conflict; `--critical-path` and
`--phase N` conflict, and N must be positive. A diagnostic `usable:false` plan
is printed with exit 1; do not use its empty phases as an execution schedule.
Global `--json` selects compact JSON; ordinary results are indented JSON.

## Reconcile before another write

Mutations and committed imports require a selected-project revision. Supply
`--if-match N` as an unsigned decimal uint64 when the intended change depends on
that known revision. If omitted, the client reads `count()` once to obtain a
revision before its single write. This is concurrency protection, not a review
of the facts or permission to change them.

On `revision_conflict` (exit 4), reread the affected facts and reconsider the
change. The client does not refresh and resubmit after a stale precondition.
A write can commit even if its response is lost, malformed, oversized or lacks
a valid revision. Treat the reported unknown outcome as UNKNOWN: stop writes,
read/reconcile the intended facts, and decide whether any further change is
needed. Errors can use `transport_error` or retain `payload_too_large`,
`unavailable` or `storage_error` with reconciliation guidance (exit 1). UNKNOWN
is not a separate wire error code. Never repeat a create, append or import
merely because success was not received.

Errors go to stderr as a safe JSON envelope with `request_id`. Exit codes are
0 success, 2 local argument/configuration/contract/account mismatch, 3 access
refusal, 4 conflicting state, and 1 other errors (including unusable plans and
output failure). Executable deployment configuration refusals may be plain
stderr diagnostics. Ask the service's Host/launcher to recover a dead service;
the client does not spawn, restart or fall back to local files/TCP.

The [source map](references/client.md#source-map) identifies the implementation
and deployment constraints behind this interface. Using the client does not
enable DBFS, migrate storage, authorize cutover or qualify production dogfood.
