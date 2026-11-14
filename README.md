# swarma-board

swarma-board stores task facts, dependencies and plans behind a separate HTTP
service. The CLI queries and edits an explicitly selected project and transfers
snapshots. The local deployment uses HTTP over a Unix-domain socket and one
Storm/bbolt database per project; the CLI has no local board store.

The shipped `single-trust-unit` deployment is for local Linux use. It refuses
TCP/TLS activation flags and the `production` profile. Processes sharing the
service's OS UID share one account and its grants: there is no isolation between
those processes. Projected account IDs and assignee labels do not authenticate
callers. The service obtains the peer UID from the kernel and checks its static
policy on each request.

## Build and test

Use Go 1.25.5 or newer, with the local `modules/dataprovider` directory present
as specified by [go.mod](go.mod). Run from the repository root. Set `build_dir`
to an absolute, private output directory outside the checkout:

```sh
build_dir=/absolute/private/build
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

The two executables default to commit `dev`. For a source-pinned build, add
`-ldflags "-X main.commit=$source_commit"` to **both** build commands, where
`source_commit` is the selected source revision. Root `go test ./...` does not
include the nested module's own test suite. See the
[data-provider documentation](docs/data-provider/README.md) for its additional
backend checks; those adapters are separate from this local service path.
The root provider fault tests require a fresh owned mode-0700 evidence directory
outside their temporary fixture roots; choose a new build directory for each run.
The aggregate test allowance above accommodates the existing long-lived timeout
cases without changing their product limits. See the
[provider test contract](internal/providerdb/README.md).

## Prepare the local service

Choose canonical absolute paths without symlink components. Use separate,
nonoverlapping paths for the socket directory, database directory, policy file
and audit-export directory. The socket directory must already exist, be owned
by the effective service UID and have mode 0700; the socket path must be unused.
The database directory is created with mode 0700 if absent and must have that
mode if present. Keep all storage and export locations private to that UID.

The deployment owner supplies the approved release policy. Its regular file
must be owned by the effective service UID, have exactly mode 0600 and one hard
link. The resolver does not install or repair it. The policy has closed JSON
schema v1: `mode:"sharedUID"`, `trust_profile:"single-trust-unit"`,
`remote_tls:false`, exactly one account and UID mapping, plus explicit projects
and project-scoped grants. The mapping UID must match the effective UID.
Use the [exact policy format](internal/auth/STATIC_POLICY.md#exact-operator-format)
with your approved values; its example values are not a deployment policy.

Pin the expected policy version separately in release configuration. The
required `--policy-version` must match `policy_version`; do not derive the
expected value by reading the policy being checked. The following paths and
release name are neutral placeholders to replace with approved local values:

```sh
"$build_dir/swarma-board-server" \
  --deployment-profile=single-trust-unit \
  --unix-socket=/absolute/private/run/board.sock \
  --data-dir=/absolute/private/data \
  --policy-file=/absolute/private/release/policy.json \
  --policy-version=example-release-v1 \
  --audit-export-dir=/absolute/private/audit-export
```

The service provisions databases for granted projects and runs in the
foreground. Its lifecycle owner starts and stops it; the client never starts
or restarts it. `--audit-export-dir` selects the separate maintenance-export
location, not the destination of the client `export` command. It does not
automatically produce a signed audit export.

Observed policy-file drift latches the running instance closed. Restoring the
file does not reopen it: inspect the change and use an approved full service
restart. A file edit is not live policy publication. Linux first use does not
qualify native macOS operation or production deployment.

## Connect and query

Supply a projection file containing exactly these three nonempty string fields
(no extra or duplicate keys, maximum 16 KiB):

```json
{"endpoint":"unix:///absolute/private/run/board.sock","project_id":"example","account_id":"example-account"}
```

The endpoint must match the selected socket; the project and account must match
the policy. `account_id` is a preflight expectation, not an identity credential.
Neither connection configuration nor credentials are discovered from home,
the environment or the working directory. Declare the projection's canonical
absolute filename explicitly:

```sh
projection=/absolute/private/client/projection.json
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-status
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" version --server
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" q 'count()'
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" q 'list(take=1000){id status assignee}'
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" q 'list(status=analysis,assignee="example-contributor",take=50){overview}'
```

`--deployment-status` is local static capability reporting, not service
readiness. `version --server` checks version/capabilities and the selected
account before a board request. `/health` is liveness; `/ready` also checks
storage and policy. Queries require viewer access. List results use stable ID
ordering; paginate with `skip` and `take` (maximum 1000). Selectors inside braces
are space-separated. Stored statuses are `backlog`, `analysis`, `to-dev`,
`development`, `to-review`, `reviewing`, `integrating`, `done`, `closed`, `blocked`.

## Validate and import a copy

Import accepts a tar.gz archive in one of two existing formats:

- Native v1: `.swarma-board-transfer.json`, `elements.json`, and optional
  checksummed `provenance/` files. The manifest's project ID must equal the
  selected target; there is no implicit remapping.
- Sanitized task-board v2: `.task-board-transfer.json`, hierarchy
  `README.md`/`progress.md` files and optional manifested activity. This adapter
  imports facts into the explicitly selected target project. Archive-contained
  local resource references are preserved using the reversible mapping below.

A raw directory or arbitrary JSON is not an import archive. See the
[transfer contract](contract.md#snapshotimport-contract-d4d5) and
[client transfer reference](agents/skills/swarma-board/references/client.md#snapshot-transfer)
for format and refusal details. Obtain an authorized, sanitized copy with its
source revision, archive digest, redaction/resource decisions and explicit
assignee-label preservation or mapping. Diagnose unsupported meaningful facts
instead of silently dropping them. Import never changes the original source.

Legacy-v2 `file://<element-id>/<resource-path>` references resolve only to the
exact owning element's archive-contained `.resources/` entry. The importer never
opens a source path or fetches a URI. Each resource keeps its name, description,
type and ordering; its URI becomes an inert
`urn:swarma-board:resource:sha256:<digest>`. Exact payload bytes survive in
`provenance/resource-<digest>.bin`, and
`provenance/legacy-resource-map-v1.json` preserves the original URI/path and
per-element ordinal associations for reversal. Identical payloads may deduplicate.
Resource classification follows the validated owner `.resources/` layout, not
payload names or contents: `README.md` at any depth there remains resource data,
even when its bytes resemble element metadata. Its association, order and exact
bytes are preserved. Owner-scoped resource labels may contain ordinary colons
in regular file entries and canonical trailing-slash directory headers only
with a closed legacy-v2 manifest and a valid element owner; those headers do not
change resource mapping. Mapping warnings disclose this preservation; preserved
resources are not exclusions.
Missing or orphan payloads, ambiguous associations, traversal, links, userinfo,
queries and fragments refuse; archive and generated-entry budgets still apply.
Native-v1 provenance remains opaque checksummed data and grants no file access.

Project-admin access is required even for dry-run. Start with a new empty target:

```sh
snapshot=/absolute/private/input/snapshot.tar.gz
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" import "$snapshot" --dry-run
```

Dry-run validates without changing facts or revision. Inspect its report and
digest. To obtain a revision through the CLI, make a fresh native export to a
new private filename and read `export.revision` in its JSON result:

```sh
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" export /absolute/private/output/revision-before-import.tar.gz
```

Only after approval, set `validated_sha256` to the dry-run report's lowercase
SHA256 and `selected_revision` to that target revision, then commit the identical
archive bytes. The query and dry-run JSON outputs do not expose revision headers.

```sh
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" import "$snapshot" --sha256 "$validated_sha256" --if-match "$selected_revision"
```

Add `--accept-exclusions` only with explicit approval of nonempty structured
exclusions. Dry-run refuses that flag and `--if-match`. Commit validates again
and requires an empty target; import is not merge, replacement or writer cutover.
The target advances its own revision; the source revision remains provenance.
Compare every imported ID, status, assignee and representable field/dependency
with the approved copy, using paginated queries and a native export.

## Edit, export and reconcile

Editor access permits mutations. Read the current facts and obtain a revision
from a fresh export before each intended change, choosing a new export filename
each time. Set `selected_revision` afresh for each write below; the second write
must not reuse the revision from before the first:

```sh
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" m 'assign(TASK-example,agent="example-contributor")' --if-match "$selected_revision"
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" m 'set_status(TASK-example,status=analysis)' --if-match "$selected_revision"
"$build_dir/swarma-board" --deployment-profile=single-trust-unit --deployment-projection="$projection" export /absolute/private/output/new-snapshot.tar.gz
```

Assignees are labels, not process allocation or authentication. Entering
`development` or `to-review` requires a nonempty assignee for task/bug leaves;
epic/story containers may retain either explicit status while unassigned. Lifecycle,
dependency, estimate and checklist guards still apply. Writes use revision CAS
(`If-Match`); if omitted, the client reads a revision once before its one write
attempt. A stale revision exits 4 without automatic refresh/resubmission. A lost
or invalid write response can mean an unknown committed outcome: read and
reconcile before deciding on another write. Never retry an import or mutation
automatically. Other exits are 0 success, 2 local argument/configuration/contract
failure, 3 access refusal, and 1 other failures or unknown outcomes.

Export requires viewer access and a destination that does not exist, including
as a symlink. It verifies the native archive digest and revision before
publishing a new mode-0600 file; it does not advance the board revision. Stop and
restart the service through its lifecycle owner, then query again to verify
persistence when validating a deployment. This README and synthetic checks do
not constitute a real first-use result.

## Contributor tools and skill

| Tool | Entry point | Output |
| --- | --- | --- |
| Go | Build/test/vet commands above | Executables in `$build_dir`; test/vet diagnostics on the terminal; Go's configured caches |
| swarma-board-server | Explicit local service command above | Unix socket and per-project bbolt databases in the selected private directories |
| swarma-board | Explicit connection plus `q`, `m`, `plan`, `import`, `export`, `version` | JSON on stdout, errors on stderr; `export` writes the selected new archive |

Use the existing [swarma-board client skill](agents/skills/swarma-board/SKILL.md)
for command selection, planning and recovery. A managed profile should declare
the repository's `agents/skills/swarma-board` directory through its supported
profile mechanism. The skill does not discover a connection, launch a service,
grant access or authorize an import. No global installation is required here.

## GitHub Actions CI

The `.github/workflows/ci.yml` workflow runs on pull request open, reopen, and
new commits. It uses GitHub-hosted Ubuntu 24.04 and Go 1.26.10. Root and nested
`modules/dataprovider` modules each run build, vet, and the full race suite;
the workflow also checks tracked Go files with read-only `gofmt -l`. Go package
parallelism is limited to one and in-package test parallelism to two, with two
Go scheduler threads. Each lane reports even when another lane fails, and a
final summary fails the job if any lane fails. Each race lane gets a separate
private provider evidence directory under the runner temporary path; evidence
and reports are not uploaded. The Actions job log and `GITHUB_STEP_SUMMARY`
contain the lane results. The workflow file is the exact command and ordering
reference for local reproduction.
