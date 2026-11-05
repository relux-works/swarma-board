# Shared synthetic fixtures

Every file here is independently authored synthetic data. Nothing was exported
from a live board. These files grant no production authority or source custody.

- `projection.json`: the exact three-key CLI projection fixture, literal loopback.
- `server.json`: v1 synthetic server fixture; viewer/editor/admin principals and
  explicit grants to one synthetic project. It stores SHA256 token hashes only.
- `elements.json`: native stored records for an epic, two stories and four tasks;
  A/B block C and C blocks D. Null, notEstimated, estimated and estimated-zero
  remain distinct; nullable times, ordered checklist, multiline Unicode text,
  safe inert resource references and manual container edges are represented.
- `native-v1.tar.gz`: exactly the native manifest, elements.json and one inert
  provenance file. Manifest source_commit is `dev`, revision 0; this fixture is
  not an exact-build qualification export. Checksums match the enclosed bytes.
- `legacy-v2.tar.gz`: smaller synthetic v2 archive with empty activity manifest,
  hierarchy README/progress documents, canonical underscore directory names and
  consistent reciprocal links. It intentionally has no activity, estimates,
  timestamps or resources; it is not equivalent to the richer native fixture.
- `archives.sha256.json`: digests of the archive files, not trust attestations.
- `plan-expectations.json`: hand-authored expected task waves/path for project
  scope. Manual story edges remain advisory; estimate values do not weight path.

The public test bearer strings are `swarma-board-synthetic-viewer`,
`swarma-board-synthetic-editor` and `swarma-board-synthetic-admin`. They are
example-only values, not real credentials. An isolated harness explicitly opens
an fd containing the chosen value (optional one LF) and supplies it to
`CredentialFromFile`; the adapter closes it and retains the value only in memory.
No environment discovery, token files, default accounts or listener is provided.

P2 owns strict server-fixture validation and real hash/expiry/grant checks. P3
owns strict projection parsing, endpoint/account preflight and fd flag handling.
P4 owns archive conversion/validation and negative transfer cases. Testadapter
function hooks deliberately provide no storage/auth/domain implementation.
