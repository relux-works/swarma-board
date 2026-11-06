# Source observations — 2026-11-06 UTC

These are historical source and metadata observations, not fresh runtime claims.
The documentation composes main `d2e3d201939aa88c6acdaa09bf0dcee157928b18`
(merged PR5); PR6 remains separate. No product source/module/dependency changes
relative to that main, full Go suite, board binary, server or private-board run
are part of this draft. Prior narrow URI witness evidence constitutes zero
runtime qualification. Sources and exact heads are bound in README.

## What exists in source

`swarma-board` was observed with default `main` at
`a1b87160b509732125ea8a6b21ade43321ffd8fc`. Source includes separate client and
server commands, an authenticated HTTP boundary, domain/query/transfer logic,
Storm/bbolt per-project durable storage, handle leases, CAS, coherent v1 audit
export, explicit single-trust-unit Unix deployment code, synthetic TLS helpers
and Linux/Darwin platform files. There is one root Go module. No standalone
data-provider module, server provider selector, product-scoped protocol or FS
adapter exists in this observed tree. These are the current assigned extension.

At observation GitHub had **zero open PRs**. The three repository PRs returned by
the all-state query were merged: #1 bootstrap, #2 Darwin platform, #3 provenance
cleanup. Branches in the metadata snapshot resolve to main or its ancestors;
none identifies an unmerged FS provider candidate. This is not a claim about
unpublished work on another machine.

The current `skill-project-management` default branch is still
`09bac3d89822c9b83c04dff6483d7b2074097a82`, the same extraction source named by
the board provenance. Its `pkg/board/DataStore` and
`tools/board-cli/internal/store/FileSystemDataStore` implement markdown storage,
locks, pair/move/activity recovery and non-authorizing write journals. These are
the concrete existing FS mechanisms to preserve. Open-PR and relevant branch
**metadata** were inventoried; no explicit new FS-provider PR/head was identified.
The source custodian must confirm any other intended FS candidate before this
inventory can be considered exhaustive.

## What actually runs / what is not verified

The task input says the existing Curator wrapper is thin and is not a board
process. The wrapper was not inspected or observed executing. It is
not evidence of an activated board service. Source constructors and deployment
descriptors show possible behavior, not that a process is running. Actual
deployment identity/version, service supervisor, endpoint readiness, client use,
FS/DB live data custody and remote production status are **NOT VERIFIED**.

Source explicitly gates production (`internal/deployment/profile.go:22–63`),
selects only explicit single-trust-unit UDS deployment, and keeps remote-TLS
activation out of that CLI path (`internal/app/deployment_command.go:90–95`).
Synthetic verified-TLS constructors are present
(`internal/app/transports.go:50–65`). None of these source facts closes a runtime
gate. Shared kernel UID is not per-agent identity/isolation. D6 journal digest,
external signer custody and native durability
remain separate, held claims. A Git commit signature is not an audit signature.

## What CI and review metadata actually claim

The observed board tree contains no `.github/workflows` files. GitHub Actions
returned zero runs; current-head check-runs returned zero; current-head statuses
returned zero entries (aggregate state `pending`, which does not mean a failed
check or a running CI job). The workflows content query returned HTTP 404.
Branch-protection query returned HTTP 403 indicating plan/visibility limitations;
no upgrade or visibility change was attempted. Therefore required-check policy
could not be established through that API. This unit does **not** assign the
board `COMPLETE_NO_REQUIRED_CHECKS`; another repository's disposition
does not establish policy for this repository.

PR #3 has a COMMENTED review record describing an independent exact-head review
of `a1b87160...` / tree `03ac0349...` for bounded provenance cleanup. That record
claims token/AST preservation, signature/patch checks, focused deployment
ordinary/race/vet and builds/inert controls; it explicitly disclaims final
full-suite/native execution and deployment/visibility authorization. The actor
recording it is the bot account; the metadata does not independently attest the
underlying reviewer model. This unit observed that claim and did not rerun its
tests or expand its acceptance to this architecture draft.

The repository's integration README and PROVENANCE contain earlier synthetic
qualification claims and explicit remaining gates. Source test names for CAS,
rollback, crash atomicity, checkpoints, Unix/TLS and Darwin exist. Test existence,
old claims and merged PR state are distinct from a current pass. This unit runs
only document/schema/source-link checks, not Go tests, builds or a board binary.

## Next gate

Independent review pending. No adoption, merge or task-board change is authorized.
This result is a proposed contract, shared conformance contract/matrix and two
separate implementation briefs. The contract owner must arrange independent
exact-head review, resolve engine/source-custody/module/compatibility decisions,
freeze the shared contract digest and staff the two implementations afterward.
Production activation is not part of this draft. No merge is authorized here.
