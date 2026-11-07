# Scoped data-provider protocol and shared conformance runner

This standalone, standard-library-only module implements the protocol values,
safe closed errors, validation, detachment and streaming canonical encoding from
the accepted source contract. It contains no storage adapter or server. The
existing root module, product code and dependency pins are unchanged.

Module path: `github.com/relux-works/swarma-board/dataprovider`.
Version proposal: **v0.1.0**, for source-owner review; no tag is published.
The accepted path and `modules/dataprovider` location are retained. A later root
integration may propose an explicit v0.1.0 requirement with the reviewed relative
replacement `./modules/dataprovider`; release discovery/tag policy is not changed
here. Root `go test ./...` does not include this independent module: also run
`go test ./...` from this directory.

The frozen inputs are:

| Input | Binding |
| --- | --- |
| Accepted source | `f403f0ed5042e79e1b718a98bec3b839eba4b78a` |
| Accepted tree | `197cfb7f13fe47180f6c049da983893bddb843b6` |
| `docs/data-provider/spec.md` SHA256 | `4dd57e520e9b7911c3200b5de9068847f2a451617bbe595ef236f4d6e785a13b` |
| Original fifty-case `conformance.json` SHA256 | `9e5f34b2ea6ea1ee292aff82590c33b0d977ee6dffc3d3e7e6f4929e52184c7f` |

The original JSON is embedded **byte-for-byte**, including historical draft
labels. `conformance.Vectors` verifies its hash and fifty ordered IDs on every
load. Those labels do not negate the accepted source decision. Neither adapter
may alter actions, oracles, fixtures or limits.

`Element`, estimate/checklist/resource records retain every existing native-v1
field, declaration/tag order, nullable value and nested array distinction from
`internal/contract/element.go` at the accepted source. This attributed record
boundary avoids a cyclic dependency on root `internal` packages. The later shared
integrator must centrally alias these exact types or convert **every** field;
it must keep task validation, inverse edges, plans, query semantics and reference
lifecycle in the existing common domain. No adapter owns a second domain model.

The interface is `Provider.View`, `ReadResource`, revision-CAS `Update`, coherent
`Checkpoint`, `Ready`, and `Close`; `Provisioner.EnsureEmpty` is separate server
control-plane provisioning. Identity always includes ProductID and BoardID.
`NextRevision` is a pure check to run under the actual writer transaction. It
does not supply a transaction, a lock, or persistence.

Resource inputs are server-local staged readers prepared before Update, never
live request bodies. `CopyResource` consumes once through EOF, reads at most the
declared size plus one, hashes/counts with constant payload working memory, and
never closes inputs. The provider owns partial staging and must discard it on
failure. `EncodeChangeSet` uses a separate opener for validated private staging;
it never replays the original input reader. `EncodeCheckpoint` consumes its
fixed-revision catalog, verifies each streamed payload, closes each child and
the checkpoint on every path, and propagates late read/close failures. Failed
output is incomplete and must be discarded. Metadata can use the existing
bounded in-memory values; no payload-sized return buffer is introduced.
Snapshot validation and encoding charge a metadata byte lower bound before
whole-value JSON allocation; exact compact encoding still enforces the same cap.
Every serialized text field is checked independently for UTF-8. Checkpoint output
checks cancellation at every Write boundary and after final Close, including
resource-free and history-only streams. An uncooperative blocked Writer cannot
be interrupted by these checks.

`OpenResourceReader` and `OpenCheckpoint` implement the common owned-handle
lifecycle over adapter-pinned immutable `ResourceBacking`: complete pre-exposure
verification, detached metadata, sequential reopen/one active child, cancellation
cleanup and stable idempotent Close. The adapter supplies stable private backing
and a release callback for its in-flight lease. Original admission deadlines must
already cover preparation; these constructors also impose the common cap without
extending an earlier deadline. A mutable live-resource opener is invalid backing.

`CheckEmptyImportTarget` is the native-v2 **inside-CAS** emptiness and Board
metadata guard. It does not implement transfer. Same-scope, matching-Board
restore is empty-target-only; a current target revision cannot override
`already_exists`. No migration, in-place replacement or history replay exists.
The provider is opaque/binary-capable; the shared domain retains NUL refusal.

`conformance.Run(t, Factory, FaultDriver)` is the one shared runner. Factories
open only a new private root and return Provider, Provisioner, same-root restart,
distinct-peer opener, and cleanup. A process-backed Provider proxy permits real
kill testing. FaultDriver maps named logical stages to actual physical points,
arms a bounded ticket, injects faults/kills and supplies actual times/exits.
It can corrupt stopped disposable fixtures or inject an overflow counter, but
cannot supply expectations or verdicts. [The API](conformance/api.go) is concrete;
both future adapters use it unchanged. Hooks are test-only, never server flags.

Every run returns fifty explicit statuses. Assertions fail the Go test; absent
mechanisms, partial scenarios and later integration/qualification remain
`NOT_VERIFIED`, never skipped or passed. A green Go test alone is not an
acceptance result. `Report.RequireAll(t)` gates complete acceptance;
`conformance.Compare(t, filesystemReport, databaseReport)` requires two complete
reports with identical frozen digests and compares canonical modeled outcomes.
All three report gates require the exact frozen ordered case IDs and layers.
Verified provider rows must include valid canonical observations for each fixture
Scope; observations from independent subcases are retained as an ordered union.
Duplicate/missing rows, malformed observations and incomplete results refuse.
The complete checkpoint digest includes Scope, every record/null/array, revision,
Actor, ordered events, membership and payload bytes. The only allowed availability
difference is measured FS active-write bounded-lock refusal; all successful reads
are whole old/new states, and quiescent results/errors must match exactly.

[The fifty-case matrix](MATRIX.md) distinguishes implemented actions, remaining
subcases, and execution. **No FS or DB case has executed in this module unit.**
Test-only behavioral controls and deliberately broken providers verify the
harness; they do not qualify either backend, process-crash persistence, physical
durability, native platforms, authentication or runtime adoption.

[The handoff](HANDOFF.md) records fixed adapter source/engine decisions, precise
contract conflicts and remaining integration/qualification work. Independent
exact-head review, the landing marker and signed landing remain separate gates.
