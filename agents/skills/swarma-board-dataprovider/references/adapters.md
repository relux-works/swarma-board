# Adapter development and test composition

Paths below are relative to the repository root. The extension seam is Go source
composition, defined by `modules/dataprovider/types.go`, not a runtime plugin
registry. `Provider` implements scoped views, resource reads, CAS updates,
checkpoints, readiness and close. Implement `Provisioner` separately where
control-plane creation is required. Compile-time assertions like those in
`modules/dataprovider/backend/fs/fs.go` check both interfaces.

Use the existing adapter matching the requested work:

| Adapter | Implementation | Test composition |
| --- | --- | --- |
| Filesystem (Linux) | `modules/dataprovider/backend/fs` | `conformance_test.go`, `fault_driver_test.go` |
| Database | `internal/providerdb` | `process_test.go`, `fault_test.go` |

An implementation must keep facts, resource bytes/membership, provenance, next
revision and event in one durable decision. CAS runs before a single callback;
refusals expose zero success values. Pin immutable backing for read handles,
honor original admission deadlines, stop admission and drain at close, and retain
ambiguous decisions for recovery. `owned.go` supplies common handle constructors;
it cannot turn mutable live files into immutable backing. Keep domain rules,
authentication and external resource retrieval outside the adapter.

## Register with the test harness

Construct `conformance.Factory` in adapter tests and pass it directly to
`conformance.Run(t, factory, faultDriver)`. Its `Name` is exactly `filesystem` or
`database`; the current harness is not an arbitrary third-backend registry.
`Factory.Open` accepts only the runner's disposable private absolute root and
returns an `Instance` with provider/provisioner, same-root restart, a distinct
peer opener and cleanup. Do not point it at an existing board or production DSN.

Implement the actual `FaultDriver` contract in
`modules/dataprovider/conformance/api.go`: map logical stages to physical adapter
points and supply bounded tickets with actual times, process exits and evidence
retention. Fault hooks belong in tests. A driver supplies mechanisms, never a
verdict or substituted oracle. Record absent physical mechanisms as unverified;
do not describe an injected error as actual device exhaustion or power loss.

Run ordinary protocol controls from `modules/dataprovider`, separately from root
tests, because it is an independent module:

```sh
GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test .
```

For a planned full FS qualification, `backend/fs/TestConformance` can write the
returned report to the explicit test-only `FS_REPORT` destination. DB tests use
`PROVIDERDB_REPORT` and test-only `PROVIDERDB_EVIDENCE`; inspect their callers before
choosing private evidence paths. These suites include costly and long-running
fault scenarios: use an explicit resource/time allowance, isolated state and
the retained exact dependencies rather than treating them as a quick example.
The root database package needs a private Go workspace joining the root and
protocol modules; the landed root `go.mod` does not bind that module. Do not
silently add a dependency, workspace or replacement as a documentation step.

Preserve all fifty returned statuses. `Report.RequireAll(t)` is the complete
report gate; `conformance.Compare` requires two complete reports with matching
frozen digests and canonical observations. A green test with `NOT_VERIFIED` rows
does not qualify a backend. Do not weaken vectors, limits or report gates to
claim acceptance. Server/domain integration, paired parity, native deployment
and physical durability need their own evidence.

## Runtime composition boundary

There is no DBFS runtime registration to execute today. The landed application
uses `internal/registry` through `internal/contract.Repository`, whereas these
adapters implement the scoped protocol's `Provider`. Importing an adapter or
passing a conformance factory does not bridge those interfaces. A future source
change must explicitly compose that boundary, preserve every record field and
domain rule, and implement selection before documentation can promise runtime
activation. Refuse a request to migrate a native tree or edit FS projections to
activate an adapter: neither supplies the missing composition, and strict
adapter admission rejects unrecognized storage intact.
