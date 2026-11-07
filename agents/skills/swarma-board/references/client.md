# Connection and snapshot reference

## Connection selection

The projection contains exactly three nonempty string fields, with no duplicate
or extra keys, bounded to 16 KiB:

```json
{"endpoint":"unix:///absolute/run/board.sock","project_id":"example","account_id":"example-account"}
```

Use the authorized projection for the already running service. This example
names no real deployment. Project IDs match `[a-z0-9][a-z0-9_-]{0,63}`; account ID
is a preflight expectation, not identity proof.

The shipped deployment client accepts:

```sh
swarma-board --deployment-profile=single-trust-unit --deployment-status
swarma-board --deployment-profile=single-trust-unit --deployment-projection=/absolute/projection.json q 'count()'
```

The first command is local static capability reporting, not readiness or
production admission. The second reads the explicitly supplied canonical
absolute projection path and uses its Unix endpoint. Profile/projection inputs
must be unambiguous; deployment inputs cannot mix with synthetic flags.
`single-trust-unit` is UDS-only, shares one OS UID and supplies no per-agent
isolation. `production` is refused; O/S/C principals and UID-helper plus a
protected per-request policy publisher remain gates. Status output does not
satisfy them or authorize activation.

Unix endpoints have the form `unix:///absolute/socket/path`, with no authority,
userinfo, query, fragment or noncanonical path. A matching Unix transport is
injected; the client never resolves a Bearer credential or sends Authorization.
The server's kernel peer UID and policy determine identity and project grants.
Changing projected `account_id` cannot create another identity under a shared UID.

Synthetic qualification is a separate route, never production discovery:

- `--synthetic-local-projection FILE` explicitly injects Unix transport, with no
  credential fd or TLS CA. The executable prints a synthetic-mode notice.
- TCP fixtures use both `--synthetic-projection FILE` and
  `--synthetic-credential-fd N`; the bounded one-shot fd is consumed and closed.
  No inline bearer flag is supplied. Unix endpoints are refused by this route.
- `--synthetic-tls-ca FILE` supplies explicit test trust for HTTPS. Certificate
  and endpoint hostname verification remain mandatory. Literal-loopback HTTP
  requires explicit synthetic mode; redirects are refused for every transport.

Every service operation creates a client that resolves one projection, checks
`/v1/version` for the product and all four v1 capabilities (`swarma_board_api`,
`task_plan`, `snapshot_import`, `revision_precondition`), then checks `/v1/me`
against the selected account before any board request. TCP uses one resolved
credential; Unix uses none. No ambient adapters, guessed endpoint or local
board fallback is provided. Server lifecycle and policy custody are external.

## Snapshot transfer

`import FILE --dry-run` reads a regular archive, validates it server-side and
returns a report without changing facts or revision. Dry-run refuses
`--if-match` and `--accept-exclusions`. Inspect the report and its digest before
an authorized commit:

```sh
swarma-board --deployment-profile=single-trust-unit --deployment-projection=/absolute/projection.json import /absolute/snapshot.tar.gz --dry-run
swarma-board --deployment-profile=single-trust-unit --deployment-projection=/absolute/projection.json import /absolute/snapshot.tar.gz --sha256 "$validated_sha256" --if-match "$selected_revision"
```

The variables stand for the validated archive's lowercase SHA256 and the
selected target's known revision. Commit requires identical bytes/digest and an
empty target; it is not merge, replacement or migration. Add
`--accept-exclusions` only with explicit consent to the report's nonempty
structured exclusions. That consent does not grant authorization. A changed
archive requires fresh validation; unknown commit outcome requires reading and
reconciliation, not resubmission.

`export NEW_FILE` reads a coherent native snapshot without advancing the board
revision. The client checks gzip content type, archive SHA256 and revision ETag
before publishing. The destination must not exist, including as a symlink;
publication stages in the destination directory and links a complete synced
0600 file without replacement. A local output failure is not a successful
export. This archive is not the separate offline maintenance journal export
and carries no approved audit signature.

Client bounds are 8 MiB per query and 64 MiB per archive/JSON response. Bounds
on response headers/body and operation time also apply; inspect the
[transport implementation](../../../../internal/client/transport.go) when
working on deadline behavior rather than inventing timeout flags.

## Source map

- [CLI commands](../../../../internal/cli/command.go),
  [grammar](../../../../internal/cli/validate.go),
  [archive files](../../../../internal/cli/files.go): exact flags, argument
  validation, output and no-overwrite behavior.
- [Client](../../../../internal/client/client.go) and
  [errors](../../../../internal/client/errors.go): preflight, ETags, digest
  checks, response bounds, unknown outcomes and exit mapping.
- [Executable dispatch](../../../../internal/app/client_command.go),
  [deployment dispatch](../../../../internal/app/deployment_command.go),
  [deployed composition](../../../../internal/app/deployment_profile.go),
  [profile](../../../../internal/deployment/profile.go) and
  [projection loader](../../../../internal/platform/projection.go): current
  reachable behavior. The older
  [wiring proposal](../../../../docs/deployment/wiring-proposal.md) describes its
  original unwired base; use the landed dispatch for current availability.
- [D4 contract](../../../../contract.md#d4-http-dsl-and-cli) defines DSL/CLI,
  revisions, roles and native import. [D6](../../../../contract.md#d6-approved-transport-task-journal-and-adoption-delta-p5)
  overrides earlier universal Bearer/local TCP wording and separates Unix
  identity, maintenance export and owner-controlled adoption. Later
  implementation-status paragraphs identify delivered constructor names and
  qualification limits; do not treat a proposal as completed qualification.
