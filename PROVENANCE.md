# P0 baseline provenance and handoff

This is the local P0 root candidate for `relux-works/swarma-board`,
authored by the baseline integrator.
It is pending separate independent review, checks and release-coordinator-controlled PR
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
architecture inputs; they remain normative candidate documents, not an immutable
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
to this P0 baseline unit. No complete A01–A15 qualification is claimed by P0.

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
The release coordinator releases them from the exact signed P0 head in the bundle, without
waiting for a destination main. The release coordinator owns any administrative empty base,
PR publication and review/landing workflow; this root candidate has no parents.

## Remaining gates

P0 tests and the dependency build probe passed using exact Go 1.25.5, with
GOMAXPROCS=2 and build/test parallelism -p 2. P0 build/tests verify
schema/fixture fidelity, explicit credential byte bounds
and fail-closed unconfigured hooks. A build-only external probe verifies all five
pinned dependencies compile together; no dependency API/provider is invoked.
They do not establish HTTP, storage atomicity, graph, CLI, archive-security or
production qualification. P1–P4 and integrated P5 acceptance remain real work.
Production auth U1, real sanitized snapshot custody, independent review,
required checks, signed landing and owner cutover authorization remain held.

## P5 composition delta

The P5 integrator composes an isolated clone from signed P0
6a0b65cc88787117ace16ba59a2560d70af63461, with exact immutable P2
 afe86ab441251acd23e5ce666527765f3b7507bc, P3
6001a5d77849c12d14a9814c1cc21d5e2972c2b1 and P4
4b4280896dca518aa9223eff63b8eba9e4110c20. Their original commits and source
attribution remain intact. P5 writes only its assigned shared module, contract,
app/cmd/integration and provenance paths. P3's proposed pflag v1.0.9 indirect
module declaration is applied centrally; existing go.sum suffices.

The original P0 architecture hashes above describe the immutable baseline,
not the current documents after subsequent semantic and provenance edits.
P5 appends approved D6 semantics and updates companion decision/ownership/task
records under the approved integration requirements. No upstream platform
API is claimed. P5 consulted the orchestrator's specification for acceptance
coverage; its historical proposed status does not supersede the frozen contract.

D6 requires production Unix kernel identity and remoteTLS, append-only task
journal plus separately signed export, Host service lifecycle/deadservice
refusal, and fresh read-only rollback preservation. These production follow-ups
are explicitly gated; current loopback HTTP and verified test TLS are synthetic
qualification only. Existing schema-v1 audit lacks the new journal digest and
must not be silently reinterpreted. No UID mapping, signer custodian, bot-key
reuse, production adapter, real import or source cutover is inferred.

P5 never fetches or modifies source product code/live data; LICENSE/NOTICE and
shared synthetic fixtures stay byte-identical to P0. No independent review,
main landing, push, production deployment or external notification is performed.
Original source-custodian notification/disposition belongs to the integration coordinator before adoption.

Completed P1 was consumed only after its final receipt at
2026-11-05T15:53:08Z: signed head d73e16961f4b10d4806b8b128194d8c64989909c,
bundle SHA256 ef658dfec6f4ebf6d5acb78fbda3b5e49a526d112ad497e97418c95da7002ba2.
Exact constructors are query.New(), domain.New(repository, clock, ID generator)
and domain.RandomID(type, time). P5 delegates ID generation to that helper and
retains domain collision checks inside the transaction. No unfinished/floating
P1 code or another identity's credential was consumed.

Transport successor composition consumes exact completed signed server
7c956e89aacfc805fc593965ce2689dd18dee140 (bundle SHA256
724dc39e6140e5b2c69cc1d35dee6d3c1dfffebce4c355b1840e07d7c7469485)
and client 23247905d9c74a6cf84ddc7f6c18b774d336e492 (bundle SHA256
973db2d52e63779e912b1c7be17f2e3c82c705801ca21552dc1e904872ab438f).
Their owned packages are incorporated byte-for-byte, with unchanged P1/P4 and
shared Go interfaces/native fixtures/dependencies. Revised spec SHA256 is
6c8c4d5b22aaef64f10e910c49e68ba2489885d3f02adb2645d420de63e7af45.
New code is confined to app/cmd/integration and this provenance record.
The real Unix listener captures the shared current kernel UID; the injected
resolver, Bearer fixture and TLS certificate are explicitly synthetic. No
per-agent isolation, production adapter, remote custody or signed audit-export
qualification is inferred. Existing audit schema stays unchanged, its migration
remains HELD, and maintenance-package ownership remains with the maintenance component owner. Original
49931945 candidate/output remain immutable; transport evidence is separate.

Offline maintenance successor continues exact transport head
352aedf5acac1ee058a36c26befe7916aa48186f and imports only the four unchanged
`internal/maintenance` files from the maintenance producer's completed signed component
37d189daecdeb98c862925c44c02f4acdf82bc20. Its frozen P2 successor and P4 bases
match this composition. The completed result, bundle and integration contract
are copied into a separate immutable handoff input directory; no moving peer
worktree is consumed or written. All P1/P2/P3/P4/shared package bytes remain
unchanged. P5 owns app dispatch, private no-replace publication, CLI process
qualification and documentation. The maintenance producer retains maintenance-package authorship.

Only synthetic temporary databases are used. Schema-v1 remains unchanged; no
v2 history, signature authority, keys, production Source APIs or protected
identity policy is invented. The CLI records an unsigned manifest with exact
build identity, coherent snapshot/journal hashes and v1 revision range. Git SSH
signatures apply only to local source candidates. The authorship hold was removed;
task-specific source custody, external key-custody hold, independent
review/checks and authorized signed landing remain pending. Existing
output and output-transport-r2 artifacts are preserved byte-for-byte, with this
successor's evidence in output-maintenance-r3. No push or live activation occurs.


Stage1 integration continues maintenance head
`3df2eeff13c94bd2f9e78a4512ee98389ae623f8`. The original SSH-signed component
objects are preserved as merge ancestors: static0600 policy
`fd8f2ef44c43dd4b54761cd0ff68d5f67d242139`, deployment descriptor/constructors
`a79afaca71cdefdbf4d6ebaf60430a4e1fec76bf`, and dedicated SSHSIG verifier
`5f858d3cd688216075cb792ebd889e60d567abe0`. Their owned component files and
handoff documents remain byte-identical. P5 adapts the proposed app helper and
owns explicit CLI dispatch, the independent policy-version release pin, static
resolver/catalog binding, stdout status routing and final-binary acceptance.
No module/shared-interface/external platform specification or schema changes occur.

The deployment profile is single-trust-unit, UDS-only, with the exact shared
kernel UID mapped through the frozen release-static policy. O/S/C principals and UID-helper, and the protected per-request policy publisher
remain production blockers, not dependencies of this candidate's integration checks.
Offline exports remain explicitly unsigned; the external signer/verifier seams
are available without a real signing adapter, key discovery or custody claim.
Native v1 is retained; no speculative v2 target is adopted. Packaging/lifecycle
belong to board service orchestrator. Only temporary synthetic state is used.
Prior output directories remain immutable; evidence goes to output-stage1-r4.
Independent review and source-owner-controlled signed landing remain separate.
