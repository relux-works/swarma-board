package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"sort"

	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

// AuditEvent is the existing schema-v1 AuditRecord, without added fields or a
// reinterpretation of historical records. It records changed IDs, not a digest
// or a replayable change set. Digest-bearing history needs an approved migration.
type AuditEvent = AuditRecord

// JournalLimit bounds the complete NDJSON event stream (including newlines).
// Overflow refuses the export; it never returns a truncated history.
const JournalLimit = 64 << 20

// ExportView returns detached state and the complete store-owned task journal
// from one bbolt read transaction. It performs no signing, file append, or writes.
// Callers must not combine a separate View and Audit to build an export.
func (d *DB) ExportView(ctx context.Context, id string) (contract.Snapshot, []AuditEvent, error) {
	if id != d.project.ID {
		return contract.Snapshot{}, nil, failure("not_found")
	}
	if e := ctx.Err(); e != nil {
		return contract.Snapshot{}, nil, e
	}
	var s contract.Snapshot
	var events []AuditEvent
	err := d.db.Bolt.View(func(tx *bolt.Tx) error {
		var e error
		s, e = d.snapshot(tx)
		if e != nil {
			return e
		}
		events, e = journal(ctx, tx, s.Revision)
		return e
	})
	if err != nil {
		return contract.Snapshot{}, nil, err
	}
	return s, events, nil
}

func journal(ctx context.Context, tx *bolt.Tx, revision contract.Revision) ([]AuditEvent, error) {
	out := []AuditEvent{}
	var previous contract.Revision
	remaining := JournalLimit
	err := tx.Bucket(audits).ForEach(func(k, v []byte) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		// Charge raw bytes before decoding or allocating an event. Stored records
		// use this same JSON encoding; also charge re-encoding below for safety
		// when validating a noncanonical or manually modified database.
		if len(v) >= remaining {
			return failure("payload_too_large")
		}
		if len(k) != 8 || previous >= revision || binary.BigEndian.Uint64(k) != uint64(previous)+1 {
			return failure("storage_error")
		}
		var event AuditEvent
		if decodeEvent(v, &event) != nil || event.Revision != previous+1 || !sort.StringsAreSorted(event.ChangedIDs) {
			return failure("storage_error")
		}
		for i, id := range event.ChangedIDs {
			if id == "" || (i > 0 && id == event.ChangedIDs[i-1]) {
				return failure("storage_error")
			}
		}
		encoded, e := json.Marshal(event)
		if e != nil {
			return e
		}
		n := max(len(v), len(encoded)) + 1
		if n > remaining {
			return failure("payload_too_large")
		}
		remaining -= n
		out = append(out, event)
		previous = event.Revision
		return nil
	})
	if err != nil {
		return nil, err
	}
	if previous != revision {
		return nil, failure("storage_error")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return out, nil
}

// appendPosition refuses a mismatched tail or an existing next key, so Update
// never overwrites an event, even in a damaged database. Full continuity is
// validated by ExportView; ordinary mutations do not rescan all history.
func appendPosition(tx *bolt.Tx, revision contract.Revision) error {
	b := tx.Bucket(audits)
	k, v := b.Cursor().Last()
	if revision == 0 {
		if k != nil {
			return failure("storage_error")
		}
		return nil
	}
	var event AuditEvent
	if !bytes.Equal(k, u64(uint64(revision))) || decodeEvent(v, &event) != nil || event.Revision != revision {
		return failure("storage_error")
	}
	return nil
}

// Refuse unknown persisted event fields rather than silently interpreting a
// changed journal shape as v1. The schema itself remains unchanged.
func decodeEvent(v []byte, event *AuditEvent) error {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if e := dec.Decode(event); e != nil {
		return e
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return failure("storage_error")
	}
	return nil
}
