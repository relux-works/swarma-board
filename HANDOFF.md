# Swarma Board handoff

## Goal

Swarma Board stores task records, dependencies, plans, and snapshots behind a separate HTTP service. The client connects to an explicitly selected project; local storage belongs to the service. The shipped local mode uses a Unix socket and one Storm/bbolt database per project on Linux. The storage work adds a scoped interface with filesystem and database implementations and one shared conformance contract. Existing filesystem behavior stays available, and migration is out of scope.

## Where things are

`main` is `6263cdaf` (2026-11-13). It contains the service and CLI, task/query/transfer logic, the scoped data-provider module, filesystem and database implementation candidates, and the legacy resource import fix. The current visible work line is [`ci/pr-workflow`](https://github.com/relux-works/swarma-board/tree/ci/pr-workflow), the only branch with an open PR. It adds pull-request checks and documents their results.

Work done after 2026-11-14 that was never pushed is not in this repository. When it is pushed, it goes to its own branch, and this table gets a row for it.

| Branch | State | What is missing |
| --- | --- | --- |
| `ci/pr-workflow` | PR #20 open; Nov 14 hosted check failed in the root-race lane | Resolve the root race-suite timeout at C19, then get a green hosted run. The PR reports that its nested module race suite passed locally. |
| `main` | Current product baseline | Wire the storage interface into the service, finish filesystem/database conformance, and complete the separate deployment and durability gates. |

The closed `docs/task-261107-cpd6wt-provider-no-migration-425`, `feat/dataprovider-db`, and `feat/dataprovider-filesystem` branches are superseded by later merged revisions: PRs #7, #12, and #11 respectively. They are not continuation lines.

## Done in main

- The HTTP service/CLI boundary stores and queries project task facts, dependencies, and plans; the CLI selects a project and transfers snapshots. Both binaries build with the commands below, and integration, app, client, store, and transfer tests passed in the bounded root run.
- Legacy import/transfer fixes preserve literal note headings, validate PAX path extensions, and preserve imported resource bytes and descriptions. The integration, legacy parser, and transfer packages passed in the bounded root run.
- The scoped data-provider module and shared conformance harness tests pass with `(cd modules/dataprovider && go test ./...)`.
- The root `go vet ./...` check passes.
- The shipped local mode uses the existing per-project Storm/bbolt store on Linux. All root test packages except `internal/providerdb` passed on macOS with a short temporary path. The full root suite is not verified because that package did not finish in the available run.

## Not done

- The running service still uses its existing Storm/bbolt store. The filesystem and database candidates are not yet selectable through service configuration or the scoped client routes.
- Full C01-C50 filesystem/database runs and their cross-backend comparison are not accepted. The matrix marks the cases unexecuted or partial; green harness controls alone do not qualify an adapter.
- Production admission, native durability, and activation remain separate gates. The shipped mode is local Linux use and explicitly does not admit production.
- No migration or existing-data conversion is planned.

## Build and test

The repository requires Go 1.25.5 or newer. This Mac has Go 1.25.14 (`darwin/arm64`). Run from the repository root.

```sh
build_dir="$(mktemp -d)"
mkdir -p "$build_dir"
mkdir -m 0700 "$build_dir/provider-evidence"
go build -mod=readonly -o "$build_dir/swarma-board" ./cmd/swarma-board
go build -mod=readonly -o "$build_dir/swarma-board-server" ./cmd/swarma-board-server
PROVIDERDB_EVIDENCE="$build_dir/provider-evidence" \
  PROVIDERDB_REPORT="$build_dir/provider-report.json" \
  go test ./... -timeout=45m
go vet ./...
(cd modules/dataprovider && go test ./...)
```

The full root command `go test ./... -timeout=45m` did not complete on macOS. The provider database package remained running past the available test window; all other root packages passed separately with a short private temp path. The root command uses a 45-minute test timeout; its provider fault tests need a fresh private evidence directory per run. The GitHub Actions branch also runs race tests separately with bounded package and test parallelism.

## Next steps

1. Resolve the root race-suite C19 `metadataBoundsCase` timeout without weakening its contract, then get PR #20's hosted check green.
2. Confirm and accept the data-provider contract revision before extending its current candidate implementations.
3. Wire explicit scoped storage selection into the service and client while keeping the existing store behavior and migration out of scope.
4. Run the complete filesystem and database conformance matrices, compare their exact observable results, and report remaining `NOT_VERIFIED` cases.
5. Complete native durability, production-admission, and activation gates separately; source tests alone do not close them.

Remote state was checked on 2026-11-24 at `6263cdaf`.
