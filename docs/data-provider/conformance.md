# Shared conformance contract v1 (proposed)

This is a test **contract**, not an implemented or executed suite. All case
definitions are in [conformance.json](conformance.json), bound to the source pins
in README. Acceptance freezes its exact digest with spec.md before either FS or
DB producer is released. A producer may not change vectors, omit inconvenient
cases or weaken assertions. The contract owner implements one runner consumed
unchanged by both factories. No test runner/product code is added in this unit.

## Runner boundary

Future runner entrypoint: `conformance.Run(t, Factory, FaultDriver)` in the
standalone protocol module's conformance package. `Factory` opens a provider in a
new owned private temporary state root and returns Provider, Provisioner, cleanup,
and a restart function. FaultDriver provides process kill/fault/pause points
mapped to the provider's real commit boundaries. These are test-only hooks, never
server flags, config, public network routes or request fields. Black-box public
operations perform the assertions; provider-specific hooks only place faults
and observe retained storage. Actual factory/runner signatures are the contract
owner's implementation task after review; both adapters use the same release.

Each case runs with provider=`filesystem` and provider=`database` independently.
Then compare canonical observable results across the two runs, excluding only
generated physical paths, process IDs, timing, backend diagnostics and lock-wait
duration. Do not exclude Scope, IDs, field/null/array values, revision, event
Actor, changed IDs, errors or ordering. Clocks/IDs/Actors are injected synthetic
constants so parity is exact. Server/domain cases run the **same** domain/API
composition with just the provider factory changed. Any integration work needed
for these cases belongs to the shared integrator; it is not two adapter variants.

Fresh factory root for every case, no home discovery, real board tree, account
files, capability proof, production DSN, cached live credentials or listening
server binary. Synthetic in-process API handlers may be tested after source
review and within the required offline confinement. This architecture unit does
not execute them. Filesystem/power-loss qualification is reported separately
from synthetic process-death testing.

## Shared fixture and normalization

The JSON fixture defines three independent scopes. Each is provisioned at
counter 0. Seed the same three Elements in one Update expected 0, yielding
counter 1, one event, changed IDs `[EPIC-a,STORY-a,TASK-a]`. Seeding is a trusted
test-domain callback, never a network provisioning route. Board names are
`A work`, `B work`, `A other`; descriptions empty. Element records use the exact
native-v1 fields/types from the bound source; any omitted string is empty, any
omitted nonnullable slice explicit empty; nullable timestamps/estimate are null.
These fixture omissions are an authoring shorthand, not a wire/storage default.
The fixture records are generated once by the contract runner, identically for
both backends. TASK-a starts with explicit `notEstimated`.

Actors use `account_id=synthetic-editor`, `operation=set_notes`, fixed UTC time
`2026-11-06T00:00:00Z`, request IDs `seed`, `one`, `two`, etc. The seed operation
is `create`; use read-only synthetic grants for denied/read tests. `old` means the
complete seeded Checkpoint (including events), `new` means the one expected
complete Checkpoint after the requested update. Equality compares exact modeled
values plus canonical bytes from P6. Imported provenance and empty/null cases
are checked without deriving identity or authority.

Unless a case explicitly says otherwise, update TASK-a.Notes to `changed` at
expected revision 1. Expected success: counter 2; same three records with only
that field changed; exactly two events; last changed IDs `[TASK-a]`; Actor matches
the injected value; unrelated scopes remain byte-identical. A read with a retained
returned slice is not a live reference to provider state.

## Fault and gate rules

Fault stages are logical: `before_decision` (no durable commit record),
`after_decision_before_ack` (durable decision, no caller success), and
`during_recovery`. A producer supplies a written mapping to real engine/FS
points. If it cannot place/justify a stage, that case is **NOT VERIFIED**, not
skipped/pass. Extra FS phases (journal sync, first/last file write, revision/event
publication, parent sync and cleanup) are mandatory subcases of the common
atomicity cases. DB maps equivalent stages to real transaction/fault points.

Definite pre-decision failure must give `old`. Post-decision crash before ack
must recover `new`. For genuinely ambiguous publication/fsync failures, report
`commit_outcome_unknown`, retain evidence, refuse further admission until
recovery, then expose exactly `old` or `new` as justified by the durable decision.
At no time may a successful View/Checkpoint expose a torn mixture. Tests assert
actual complete states, not merely error presence or file existence.

All provider cases are required for provider parity acceptance. Server cases are
required for integrated acceptance. The physical durability case is required for
the deployment's native durability claim; lack of host/filesystem custody keeps
that claim held and must be reported explicitly. Passing Linux process-death
tests never certifies Darwin, power loss, network filesystems, production auth or
activation. Independent exact-head review, checks and signed PR landing are
separate gates; no conformance result authorizes runtime activation.

## Existing tests to adapt, not duplicate blindly

At board base `a1b87160b509732125ea8a6b21ade43321ffd8fc`:

- `internal/store/store_test.go`: concurrent CAS/restart, detached rollback,
  cancellation/overflow/callback count, crash atomicity and coherent backup.
- `internal/store/export_test.go`: coherent/detached checkpoint, corrupted
  continuity refusal, bounded history and uncommitted transaction visibility.
- `internal/registry/registry_test.go`: project isolation, lease drain, private
  directory/schema/owner-lock refusal and export detachment.
- `internal/httpapi/httpapi_test.go`: role/precondition/route bounds; these must
  gain product-isolation cases in the shared integration layer.

At FS base `09bac3d89822c9b83c04dff6483d7b2074097a82`:

- `tools/board-cli/internal/store/progress_pair_process_unix_test.go`,
  `element_move_process_unix_test.go`,
  `activity_strict_atomicity_process_unix_test.go` and
  `board_write_journal_recovery_process_unix_test.go` are relevant source test
  mechanisms. Their existence is not proof of this new full-envelope guarantee
  or of a pass in this workbench. Extract narrowly with attribution and preserve
  the existing non-authorizing journal assertions.
