// Package maintenance builds unsigned offline checkpoints of schema-v1 boards.
// It never stops a service, initializes storage, restores a writer or selects a
// signing key. Host maintenance ownership must outlive the entire Export call.
package maintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/transfer"
)

const (
	Format       = "swarma-board-maintenance"
	Unsigned     = "unsigned"
	MaxManifest  = 16 << 10
	SnapshotName = "snapshot.tar.gz"
	JournalName  = "task-audit.ndjson"
	ManifestName = "manifest.json"
)

var (
	ErrWriterNotStopped = errors.New("maintenance requires an already-stopped writer")
	ErrInvalid          = errors.New("invalid maintenance checkpoint")
	ErrLimit            = errors.New("maintenance artifact limit exceeded")
	ErrSignatureGate    = errors.New("protected signature verification required")
)

// Limits may tighten, but never raise, the frozen artifact ceilings. Zero uses
// the ceiling. These are artifact bounds, not a total process memory budget.
type Limits struct{ SnapshotBytes, JournalBytes int64 }

func (l Limits) normalized() (Limits, error) {
	if l.SnapshotBytes == 0 {
		l.SnapshotBytes = transfer.MaxCompressed
	}
	if l.JournalBytes == 0 {
		l.JournalBytes = store.JournalLimit
	}
	if l.SnapshotBytes < 1 || l.SnapshotBytes > transfer.MaxCompressed || l.JournalBytes < 1 || l.JournalBytes > store.JournalLimit {
		return l, ErrLimit
	}
	return l, nil
}

type Options struct {
	Path    string
	Project contract.Project
	// WriterStopped is an explicit Host assertion, never a stop command or proof
	// of whole-registry ownership. The read-only file lock also excludes writers.
	WriterStopped  bool
	ExporterCommit string
	// Inert declared identifier only; empty is valid for unsigned evidence.
	DeclaredSignerKeyID string
	Limits              Limits
}

type Artifact struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type Journal struct {
	Format string `json:"format"`
	First  uint64 `json:"first_revision"`
	Last   uint64 `json:"last_revision"`
	Count  uint64 `json:"event_count"`
}

// Manifest bytes are json.Marshal of this ordered struct followed by one LF.
// Hashes describe exact artifact bytes, not historical change digests.
type Manifest struct {
	Format              string   `json:"format"`
	Version             int      `json:"version"`
	Status              string   `json:"status"`
	ProjectID           string   `json:"project_id"`
	Revision            uint64   `json:"revision"`
	ExporterCommit      string   `json:"exporter_commit"`
	DeclaredSignerKeyID string   `json:"declared_signer_key_id"`
	Journal             Journal  `json:"journal"`
	Snapshot            Artifact `json:"snapshot"`
	Audit               Artifact `json:"audit"`
}

// Bundle is detached evidence. Callers own private, exclusive output staging and
// publication. Export returns a zero Bundle on every failure; it writes no files.
type Bundle struct{ Manifest, Snapshot, Journal []byte }

// Export retains one read-only handle (and shared database lock) through encoding
// and manifest construction. It uses exactly one coherent ExportView, never a
// separate state read and journal read. It has no live Repository fallback.
func Export(ctx context.Context, opts Options) (out Bundle, err error) {
	if err = ctx.Err(); err != nil {
		return Bundle{}, err
	}
	if !opts.WriterStopped {
		return Bundle{}, ErrWriterNotStopped
	}
	limits, err := opts.Limits.normalized()
	if err != nil {
		return Bundle{}, err
	}
	if !store.ValidProjectID(opts.Project.ID) || !label(opts.ExporterCommit, false) || !label(opts.DeclaredSignerKeyID, true) {
		return Bundle{}, ErrInvalid
	}
	db, err := store.OpenReadOnly(opts.Path, opts.Project)
	if err != nil {
		return Bundle{}, err
	}
	defer func() {
		if e := db.Close(); err == nil && e != nil {
			out, err = Bundle{}, e
		}
	}()
	if err = ctx.Err(); err != nil {
		return Bundle{}, err
	}
	snapshot, events, err := db.ExportView(ctx, opts.Project.ID)
	if err != nil {
		return Bundle{}, err
	}
	var archive, journal bytes.Buffer
	if err = transfer.New(opts.ExporterCommit).Encode(ctx, &boundedWriter{ctx: ctx, dst: &archive, max: limits.SnapshotBytes}, snapshot); err != nil {
		return Bundle{}, err
	}
	jw := &boundedWriter{ctx: ctx, dst: &journal, max: limits.JournalBytes}
	for _, event := range events {
		b, e := json.Marshal(event)
		if e != nil {
			return Bundle{}, e
		}
		if _, e = jw.Write(append(b, '\n')); e != nil {
			return Bundle{}, e
		}
	}
	revision := uint64(snapshot.Revision)
	j := Journal{Format: "swarma-board-audit-schema-v1", Last: revision, Count: uint64(len(events))}
	if revision > 0 {
		j.First = 1
	}
	if j.Count != revision {
		return Bundle{}, ErrInvalid
	}
	m := Manifest{Format: Format, Version: 1, Status: Unsigned, ProjectID: snapshot.Project.ID, Revision: revision, ExporterCommit: opts.ExporterCommit, DeclaredSignerKeyID: opts.DeclaredSignerKeyID, Journal: j, Snapshot: describe(SnapshotName, archive.Bytes()), Audit: describe(JournalName, journal.Bytes())}
	manifest, err := marshal(m)
	if err != nil {
		return Bundle{}, err
	}
	if err = ctx.Err(); err != nil {
		return Bundle{}, err
	}
	return Bundle{Manifest: manifest, Snapshot: archive.Bytes(), Journal: journal.Bytes()}, nil
}

func label(s string, empty bool) bool {
	return (empty || s != "") && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func describe(name string, b []byte) Artifact {
	return Artifact{Name: name, Bytes: int64(len(b)), SHA256: digest(b)}
}
func marshal(v any) ([]byte, error) { b, e := json.Marshal(v); return append(b, '\n'), e }

type boundedWriter struct {
	ctx    context.Context
	dst    io.Writer
	n, max int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.max-w.n {
		return 0, ErrLimit
	}
	n, e := w.dst.Write(p)
	w.n += int64(n)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	return n, e
}
