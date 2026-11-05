# P0 baseline provenance and handoff

This is the local P0 root candidate for `relux-works/swarma-board`, unit
A2-261105-014-board-baseline, authored by producer `agents2_swarma_dev3`.
It is pending separate independent Muse review, checks and keeper-controlled PR
publication/landing. This candidate is not a release or production adapter.

## Source custody

Fresh read-only clone: `git@github.com:relux-works/skill-project-management.git`.
Detached source commit: `09bac3d89822c9b83c04dff6483d7b2074097a82`.
The source working tree remains unchanged. No live snapshot or source account
configuration was read. No source product code was transplanted in P0.

The source root contains Apache-2.0 LICENSE and attribution NOTICE, retained
unchanged here, with their original copyright/attribution:

- `LICENSE`: SHA256 `d5ea692870bebccd71cb152e617120be8266362d31b04fcbcba155a059aacffb` (copied unchanged).
- `NOTICE`: SHA256 `2a0bab9eb0e75c8fc4f0155178d2e60e31fafc0086b60a6c51df5771f4722826` (copied unchanged).

First-party source inspected for P0: root LICENSE/NOTICE;
`tools/board-server/go.mod`, `tools/board-cli/go.mod`, `pkg/board/go.mod`;
`pkg/board/types.go` (DirName), `pkg/board/markdown/readme.go`,
`pkg/board/markdown/progress.go`; `pkg/board/transfer/format.go`,
`pkg/board/transfer/export.go`, `pkg/board/transfer/import.go`.
These establish dependency pins and synthetic legacy fixture layout, not a copy
of the legacy CLI, store, sessions, runtime, auth bootstrap or renderer.
First-party AGENTS files were absent in the pinned extraction paths; vendored
instructions/code were outside scope.

## Architecture custody

The four architecture handoffs were copied byte-for-byte from the assigned
workbench inputs; they remain normative candidate documents, not an immutable
upstream platform contract:

- `contract.md`: SHA256 `37ccc80bb52dcd5d7af86ae7da5a141067c95021f0422b56b5f6a5ce0e0314c0`.
- `decisions.json`: SHA256 `ff4c468de0781da5924b5a2c16d805eb95c0696d6b9b0fca44fba0b43ce5e396`.
- `file-ownership.json`: SHA256 `8466293b4bdc0f7206a356c5c93e1ad6b214f593f65abddbde36256b35492ac7`.
- `task-decomposition.json`: SHA256 `451039fdd929d2f3ac758e3d0ea45dc3d16d5bdac8d942ba84648b1563fb2ebe`.

`internal/contract/contract.go` materializes the exact D5 declaration block
(formatting/package/import comments only). `element.go` implements the specified
stored record and nested JSON types; nullable timestamps/estimates are retained.
Only optional review/task_class and absent estimate scale use omitempty.
`error.go` implements the required error method without adapter dependencies.

The architecture mentions `inputs/spec.md` A01–A15, but no spec.md was supplied
to this P0 workbench. No complete A01–A15 qualification is claimed by P0.

## Frozen shared base for P1–P4

The single root module declares Go 1.25.5. Direct dependencies are only Storm
v3.2.1, bbolt v1.3.4, chi v5.2.4, agentquery v1.6.1 and Cobra v1.10.2
(the pinned CLI version). `go.sum` records the graph required by these libraries;
transitive graph entries are not permission to adopt unrelated integrations.
No replace directives, go.work, nested modules or vendored product tree exist.
The five dependencies are intentionally retained before consumers are present;
do not run tidy to remove their frozen producer pins during P0.

`internal/testadapter` provides explicit projection/fd credentials and function
fakes of Repository/Service/Parser/Transfer/Authorizer. Hooks fail closed when
unconfigured. These are not durable storage or auth implementations: P1 defines
domain/parser behavior; P2 defines durable CAS/storage, strict fixture/auth and
HTTP checks; P3 defines projection parsing/client/CLI; P4 defines transfer and
legacy parsing. Each owner can test with these hooks without another branch.
Synthetic fixture contents and archive digests are described in testdata/README.
No negative archive suite or real snapshot custody is claimed by P0.

`internal/app/dependencies.go` defines only composition input signatures.
DomainInputs uses Repository, Now func() time.Time, and NewID
func(elementType string) (string,error). P1 may consume these directly or expose
a constructor taking the same three values. P2 accepts Service/Parser/Transfer/
Authorizer. P3 accepts separate ProjectionSource/CredentialSource. No concrete
composition, listener or command stub is implemented before P5.

Shared files have one writer: baseline-integrator. P1–P4 must propose shared
contract/dependency changes to that owner and work only in their assigned paths.
The keeper releases them from the exact signed P0 head in the bundle, without
waiting for a destination main. The keeper owns any administrative empty base,
PR publication and review/landing workflow; this root candidate has no parents.

## Remaining gates

P0 tests and the dependency build probe passed using exact Go 1.25.5, with
GOMAXPROCS=2 and build/test parallelism -p 2. P0 build/tests verify
schema/fixture fidelity, explicit credential byte bounds
and fail-closed unconfigured hooks. A build-only external probe verifies all five
pinned dependencies compile together; no dependency API/provider is invoked.
They do not establish HTTP, storage atomicity, graph, CLI, archive-security or
production qualification. P1–P4 and integrated P5 acceptance remain real work.
Production auth U1, real sanitized snapshot custody, independent Muse review,
required checks, signed landing and owner cutover authorization remain held.
