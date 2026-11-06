# Bounded source follow-ups

These are separate source fixes after protocol review. They are not protocol
implementation or migration dependencies. Original reproduction wording below
is superseded by the actual review/fix disposition at the end.
No task board is changed and no producer is launched by this note.

1. **PAX and safePath** — inspect `internal/transfer/archive.go` at pinned board
   base `a1b87160b509732125ea8a6b21ade43321ffd8fc`. The current raw block gate
   refuses non-ordinary tar types (including PAX/GNU metadata) before archive/tar
   decoding; safePath rejects traversal, absolute/drive-style paths, aliases and
   backslashes. The requested compatibility/safety fix belongs to a focused
   transfer workbench. First bind the expected allowed PAX subset or reaffirm
   refusal, then exercise synthetic PAX path overrides, duplicate paths,
   traversal, symlink/hardlink/special headers, NUL, compressed/expanded/entry
   limits and ordinary USTAR/V7 acceptance. Preserve actual failure and bounds;
   do not relax the raw gate or treat cleaned paths as evidence of safety.
   No runtime archive ingestion or existing-data rewrite is needed.

2. **Notes containing `##`** — inspect `internal/legacyparse/document.go` and
   `parse.go` at the same base. The parser treats unfenced ATX level-two headings
   as section boundaries and has structural escaping/fence handling. A focused
   parser workbench should establish the requested literal-note behavior and
   preserve note bytes with synthetic escaped/unescaped headings, fenced code,
   CR/LF/CRLF, long lines and round-trip cases, while retaining deliberate known
   sections and refusal bounds. A source observation is not a reproduced defect
   or an accepted fix. No filesystem behavior is changed in this spec revision.

Both follow-ups need their own isolated branch, checks and fresh independent
exact-head review. Neither blocks review of this server provider protocol; no
new importer, sanitizer, pilot or migration work is implied.

## Observed source disposition

The independent review reproduced `legacyparse` failures at `a1b8716`: **1242 of
1244** elements parsed over 13 boards; both failures were real notes with literal
`## ` headings. It also reports **18** progress-without-README directories on six
boards. These are attributed reviewer measurements, not worker measurements or
private content read for this draft. The earlier statement that this is merely
a source observation no longer describes the evidence.

Actual [PR5](https://github.com/relux-works/swarma-board/pull/5),
`fix: preserve literal headings inside imported notes`, merged 2026-11-06
22:59:31 UTC at `d2e3d201939aa88c6acdaa09bf0dcee157928b18`. It changes
internal/legacyparse/document.go and synthetic parser/transfer tests. This draft
composes this main, preserving those code deltas unchanged; it does not
rerun private boards or claim the strict provider can open legacy trees.

Actual [PR6](https://github.com/relux-works/swarma-board/pull/6),
`fix: validate bounded PAX path extensions on import`, was open at
`4bb8406e025b1377426f15e7a78be72061c023e4`. Composition remains separate.
No PAX code is edited, reviewed, adopted or merged here.
P3's new native-v2 ordinary-USTAR-only form does not change PR6's old-format work.
These are intake metadata snapshots; preserve subsequent independently owned
deltas without treating this document as their acceptance. No private board or
live archive was read, converted or qualified here.
