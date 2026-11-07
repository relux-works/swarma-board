---
name: swarma-board-dataprovider
description: Use or extend swarma-board's scoped Go data-provider protocol and filesystem/database adapters. Use for provider CAS, resources, checkpoints and adapter conformance; ordinary task-board commands use the client/server boundary.
---

# Scoped board data providers

Work from the repository's `modules/dataprovider` module, imported as
`github.com/relux-works/swarma-board/dataprovider`. It uses only the Go standard
library. The filesystem adapter is in that module's `backend/fs` package;
the database adapter is root-internal `internal/providerdb`, retaining the root
Storm/bbolt pins. Read the relevant adapter README before opening storage.

These APIs are server-side storage building blocks. Ordinary board operations
cross the client/server boundary. The landed server composition in
`internal/app/deployment_profile.go` uses `internal/registry`; it does not select
these scoped adapters. Refuse a request to enable DBFS with an invented CLI flag
or plugin registration command: neither is implemented by that composition.

## Use the protocol

- Carry both `ProductID` and `BoardID` in every `Scope` and `Revision`.
  Open only an explicitly selected, private, absolute adapter root.
  `EnsureEmpty` is separate control-plane provisioning: repeating it with the
  same metadata preserves facts and does not prove the target is empty.
- Read with `View`. Pass its scoped revision to `Update`, plus an authenticated
  server-supplied `Actor`. The adapter checks CAS before calling the bounded
  callback at most once. Keep domain validation and derived edges in the common
  domain; provider record-shape checks do not implement those rules.
- Stage resource inputs locally before `Update`; supply verified metadata and
  a one-shot reader, never a live request body or external fetch. The provider
  consumes through EOF without closing the input. See `types.go` and `stream.go`
  for ownership and byte limits.
- Consume resource readers through EOF and close them. A checkpoint pins one
  revision; close each resource child before opening another and close the
  checkpoint. Retain the admission context through the complete handle lifetime.
  Discard an encoded export on any late read, write or close failure.
- On `revision_conflict`, reread and reconsider the intended change. On
  `commit_outcome_unknown`, stop writes, recover/reopen and reconcile observed
  state; automatic retry can duplicate a committed mutation. Provisioning and
  import do not authorize migration or replacement of existing data.

`types.go`, `errors.go` and `limits.go` in the protocol module are the concrete
API, closed error vocabulary and bounds. FS generated Markdown is a projection;
editing it is not a supported write or native import path.

## Inert example

From the repository root, with the declared Go toolchain available:

```sh
cd modules/dataprovider
GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go run ../../agents/skills/swarma-board-dataprovider/examples/protocol/main.go
```

The example uses synthetic in-memory values, prints a next revision and then a
cross-board refusal. It opens no provider, data directory, home, credential,
listener or external resource. Expected output:

```text
next revision: 1
cross-board revision: invalid_request
```

Refuse reusing a revision from another board even when its counter matches:
CAS identity includes the full scope pair. `NextRevision` only checks values;
calling it outside a writer transaction does not acquire a lock or persist a
commit. The example therefore makes no storage or durability claim.

## Extend an adapter

For the actual Go interface/factory seam, read
[adapter development](references/adapters.md). It explains source composition,
test registration and the shared acceptance gates. There is no dynamic plugin
loader. Do not describe a new backend as selectable by the running service until
its server/domain integration actually exists.
