# Database provider

`Open(ctx, privateAbsoluteRoot)` admits a new empty private directory or this
provider's exact `scoped-provider/1` format. Provision explicitly with
`EnsureEmpty(ctx, Board)`. A scope key contains both product and board IDs;
existing board metadata is never overwritten. No legacy database, native
markdown tree, migration, network service or environment-based discovery is
supported.

The provider uses **Storm v3.2.1 / bbolt v1.3.4**, retaining the repository's
existing engine pins. The protocol module remains standard-library-only and its shared conformance
package is unchanged. The adapter lives in root `internal/providerdb`, using the
existing root module's exact engine pins. Shared root module wiring to the
protocol is deferred to the shared integrator. Isolated checks use a private Go
workspace joining the root and protocol modules; no workspace or server wiring
is committed by this backend change.

One private local-directory owner is supported. bbolt's real owner lock refuses
another actual process within the common lock budget. Writes are globally
serialized, including CAS before the single callback. Facts, provenance,
content-addressed resource membership/chunks, scoped revision, event and envelope
checksum share one writable transaction. Durable sync is enabled. A successful
bbolt `Tx.Commit` is the decision; commit errors conservatively return
`commit_outcome_unknown` and latch admission until reopen. Callers must reconcile
and never automatically retry a mutation.

Every admitted scope has complete contiguous event history and an integrity
checksum over its canonical snapshot and stored history. Open validates all
scopes and all resource bytes. Unknown schema fields, malformed history,
mismatched full scope, missing membership, digest/size failures, aliased roots,
symlinks, hard-linked database files and non-private storage refuse. Checksums
are corruption detection, not signatures or authority. An administrator who
rewrites storage and checksums is outside this integrity model. Orphan staging
and read-pin directories after a process death are retained, never used as
recovery decisions or silently interpreted as board facts.

Resources are stored in bounded chunks inside the transaction. Input is consumed
once through verified EOF into private staging, then reopened read-only for
canonical size accounting and engine insertion. Snapshot/catalog/reference and
all byte/count limits are checked against the resulting state, including
replacement at capacity. Idempotent puts still consume and verify the input but
produce no changed digest. No live HTTP body or external resource fetch exists.

ReadResource and Checkpoint copy one coherent read transaction to private pinned
files, close that transaction, verify the backing, and expose the common owned
read-only handles. Live streams do not pin bbolt's mmap or block a writer's
remap; deletion of current membership cannot change pinned bytes. Deadlines
retain the original admission time and expired handles release their leases.
Close stops admission, drains leases within the common shutdown budget and
retains backing on timeout. Final physical close runs once; concurrent Close
callers have separate bounded waits and observe its stable result.

**Chunking is not a total-memory bound.** bbolt retains dirty values/pages until
commit. The dedicated `TestMaximumResourceTransaction` measures an actual single
256 MiB put transaction, then reopens and verifies its complete checkpoint. Run
it alone with an external RSS meter; its optional JSON receipt records submitted
bytes, actual update time, Go allocations, process peak RSS after the update and
database file size. It does not establish a universal RAM ceiling. Metadata and
history validation use bounded in-memory protocol values; full history hashing
is linear in retained history on each update. The complete-history counter
cannot reach max uint64 under the finite event byte cap; an injected counter is
not a valid full-history conformance pass.

The unchanged runner is invoked by `TestConformance` with an actual worker
process, anonymous pipes, same-root restart, a distinct process peer, and the
physical fault driver. Before-decision points are inside the real writable
transaction; after-decision kills happen after successful synced engine commit;
recovery kills happen after engine open and before envelope validation. The
latter is an admission/restart test, not a kill inside a bbolt recovery write.
Predecision I/O errors (including the disk-full mode), ambiguity and exposed
reader/close errors are deterministic test injections at the documented points,
not actual device exhaustion or physical sync-failure experiments. Test hooks
exist only in the package's test binary; there is no public fault switch.

A green Go test is not fifty-case acceptance. Preserve the complete returned
report: partial cases remain NOT_VERIFIED, and later server/domain integration,
cross-backend comparison, native durability and large subtree/platform
qualification remain separate. No power-loss claim follows from process kills.
Operation contexts bound cooperative work; blocking engine/OS I/O is not forcibly
interruptible and requires storage/deadline qualification. This draft does not
install a provider or admit a runtime deployment. Independent exact-head review
and source-owner composition remain required.

The current shared harness is awaiting corrections from independent review.
Its raw statuses are provisional behavioral observations, not accepted complete
conformance. Requalification against the corrected, reviewed common dependency
is required before shared composition or any acceptance claim. The controller
can retain bounded synthetic physical database copies and a hash/time manifest
through its test-only evidence destination, separately from provider APIs.
