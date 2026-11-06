# Bounded source follow-ups

These are separate source fixes after protocol review. They are not protocol
implementation, migration dependencies or claims of reproduced runtime failures.
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
