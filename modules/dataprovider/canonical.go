package dataprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
)

// budgetWriter refuses an overflowing chunk before writing it. Any failed
// encoding is incomplete and must be discarded, never published as success.
type budgetWriter struct {
	w                    io.Writer
	n, cap               int64
	ctx                  context.Context
	retainedContextError func() error
}

func (w *budgetWriter) contextError() error {
	if w.ctx != nil && w.ctx.Err() != nil {
		return ContextError(w.ctx.Err())
	}
	if w.retainedContextError != nil {
		return w.retainedContextError()
	}
	return nil
}

func (w *budgetWriter) Write(p []byte) (int, error) {
	if err := w.contextError(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.cap-w.n {
		return 0, NewError(PayloadTooLarge)
	}
	n, err := w.w.Write(p)
	w.n += int64(n)
	if err != nil {
		return n, streamError(err)
	}
	if n != len(p) {
		return n, NewError(StorageError)
	}
	if err := w.contextError(); err != nil {
		return n, err
	}
	return n, nil
}

// preflightJSON charges a lower bound before json.Marshal can allocate a whole
// metadata value. It is used only with this module's fixed record declarations:
// strings cost at least their input bytes, byte slices at least their raw bytes,
// and arrays/records/scalars at least their structural minimum. Escaping,
// base64 expansion and field names can only add bytes; budgetWriter still
// enforces the exact compact encoding cap. No input-sized buffer is built here.
func preflightJSON(v any, remaining int64) error {
	charge := func(n int64) bool {
		if n > remaining {
			return false
		}
		remaining -= n
		return true
	}
	var visit func(reflect.Value) bool
	visit = func(v reflect.Value) bool {
		if !v.IsValid() {
			return charge(1)
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return charge(1)
			}
			return visit(v.Elem())
		case reflect.String:
			return charge(max(1, int64(v.Len())))
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return charge(max(1, int64(v.Len())))
			}
			// Brackets and separators alone cost len+1, including []'s
			// normalized form for nil top-level collections.
			if !charge(1) || !charge(int64(v.Len())) {
				return false
			}
			for i := 0; i < v.Len(); i++ {
				if !visit(v.Index(i)) {
					return false
				}
			}
			return true
		case reflect.Struct:
			if !charge(1) {
				return false
			}
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if field.PkgPath != "" || field.Tag.Get("json") == "-" {
					continue
				}
				x := v.Field(i)
				if strings.Contains(field.Tag.Get("json"), ",omitempty") && x.IsZero() {
					continue
				}
				if !visit(x) {
					return false
				}
			}
			return true
		default:
			return charge(1)
		}
	}
	if !visit(reflect.ValueOf(v)) {
		return NewError(PayloadTooLarge)
	}
	return nil
}
func array[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
func provenanceValue(in []ProvenanceFile) []ProvenanceFile {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		if out[i].Data == nil {
			out[i].Data = []byte{}
		}
	}
	return out
}
func snapshotValue(s Snapshot) Snapshot {
	s.Elements = array(s.Elements)
	s.Provenance = array(provenanceValue(s.Provenance))
	s.Resources = array(s.Resources)
	return s
}
func eventValue(e Event) Event {
	e.ChangedIDs = array(e.ChangedIDs)
	e.ChangedResourceIDs = array(e.ChangedResourceIDs)
	return e
}
func writeJSON(w io.Writer, v any) error {
	if b, ok := w.(*budgetWriter); ok {
		if err := preflightJSON(v, b.cap-b.n); err != nil {
			return err
		}
	}
	p, err := json.Marshal(v)
	if err != nil {
		return NewError(InvalidSnapshot)
	}
	_, err = w.Write(p)
	return err
}
func literal(w io.Writer, s string) error { _, err := io.WriteString(w, s); return err }
func EncodeSnapshot(w io.Writer, s Snapshot) (int64, error) {
	if err := preflightJSON(s, MaxSnapshotBytes); err != nil {
		return 0, err
	}
	b := &budgetWriter{w: w, cap: MaxSnapshotBytes}
	err := writeJSON(b, snapshotValue(s))
	return b.n, err
}
func EncodeCommit(w io.Writer, c Commit) (int64, error) {
	c.ChangedIDs = array(c.ChangedIDs)
	c.ChangedResourceIDs = array(c.ChangedResourceIDs)
	b := &budgetWriter{w: w, cap: MaxEventNDJSONBytes}
	err := writeJSON(b, c)
	return b.n, err
}
func EncodeEvent(w io.Writer, e Event) (int64, error) {
	b := &budgetWriter{w: w, cap: MaxEventNDJSONBytes}
	err := writeJSON(b, eventValue(e))
	return b.n, err
}

func sortedDistinct(ids []string) bool {
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			return false
		}
	}
	return true
}

// ValidateEvents requires the entire contiguous stream 1..Counter. It never
// truncates, repairs, or invents history and charges the exact canonical LF.
func ValidateEvents(s Snapshot, events []Event) error {
	if uint64(len(events)) != s.Revision.Counter {
		return NewError(StorageError)
	}
	budget := &budgetWriter{w: io.Discard, cap: MaxEventNDJSONBytes}
	for i, e := range events {
		if e.FormatVersion != EventFormatVersion || e.Scope != s.Board.Scope || e.Revision.Scope != e.Scope || e.Revision.Counter != uint64(i)+1 || ValidateActor(e.Actor) != nil || !sortedDistinct(e.ChangedIDs) || !sortedDistinct(e.ChangedResourceIDs) {
			return NewError(StorageError)
		}
		for _, id := range e.ChangedIDs {
			if !elementID.MatchString(id) {
				return NewError(StorageError)
			}
		}
		for _, d := range e.ChangedResourceIDs {
			if ValidateDigest(d) != nil {
				return NewError(StorageError)
			}
		}
		if err := writeJSON(budget, eventValue(e)); err != nil {
			return err
		}
		if err := literal(budget, "\n"); err != nil {
			return err
		}
	}
	return nil
}

// StagedResourceOpener opens already validated provider-private staging by put
// index. It MUST NOT rewind/reuse ResourceInput.Reader. Each returned handle is
// owned and closed by EncodeChangeSet. This separates one-shot input consumption
// from logical size accounting without requiring payloads in memory.
type StagedResourceOpener func(index int) (io.ReadCloser, error)

func encodePayload(ctx context.Context, w io.Writer, info ResourceInfo, r io.ReadCloser) (err error) {
	defer func() {
		if closeErr := r.Close(); err == nil && closeErr != nil {
			// Finalizing private source storage cannot establish publication
			// ambiguity or membership refusal. Destination errors retain their
			// separate encoder/budget classification.
			err = committedError(closeErr)
		}
	}()
	if err = literal(w, `{"info":`); err != nil {
		return err
	}
	if err = writeJSON(w, info); err != nil {
		return err
	}
	if err = literal(w, `,"data":"`); err != nil {
		return err
	}
	b64 := base64.NewEncoder(base64.StdEncoding, w)
	_, err = CopyResource(ctx, b64, ResourceInput{Info: info, Reader: r})
	if err != nil {
		_ = b64.Close()
		return err
	}
	if err = b64.Close(); err != nil {
		return streamError(err)
	}
	return literal(w, `"}`)
}

func EncodeChangeSet(ctx context.Context, w io.Writer, c ChangeSet, open StagedResourceOpener) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, ContextError(err)
	}
	b := &budgetWriter{w: w, cap: MaxChangeSetBytes, ctx: ctx}
	err := encodeChangeSet(ctx, b, c, open)
	return b.n, err
}
func encodeChangeSet(ctx context.Context, w io.Writer, c ChangeSet, open StagedResourceOpener) error {
	// Metadata is serialized in P2/P6 declaration order; provenance nil is null.
	if err := literal(w, `{"upserts":`); err != nil {
		return err
	}
	if err := writeJSON(w, array(c.Upserts)); err != nil {
		return err
	}
	if err := literal(w, `,"deletes":`); err != nil {
		return err
	}
	if err := writeJSON(w, array(c.Deletes)); err != nil {
		return err
	}
	if err := literal(w, `,"provenance":`); err != nil {
		return err
	}
	if err := writeJSON(w, provenanceValue(c.Provenance)); err != nil {
		return err
	}
	if err := literal(w, `,"put_resources":[`); err != nil {
		return err
	}
	for i, r := range c.PutResources {
		if err := ctx.Err(); err != nil {
			return ContextError(err)
		}
		if open == nil {
			return NewError(InvalidRequest)
		}
		if i > 0 {
			if err := literal(w, ","); err != nil {
				return err
			}
		}
		h, err := open(i)
		if err != nil {
			if h != nil {
				_ = h.Close()
			}
			// Opening private staging cannot establish publication ambiguity or
			// public membership refusal. Retain the acquisition outcome over
			// cleanup, preserving only standard context sentinels as causes.
			return committedError(err)
		}
		if h == nil {
			return NewError(StorageError)
		}
		if err := encodePayload(ctx, w, r.Info, h); err != nil {
			return err
		}
	}
	if err := literal(w, `],"delete_resources":`); err != nil {
		return err
	}
	if err := writeJSON(w, array(c.DeleteResources)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return ContextError(err)
	}
	return literal(w, "}")
}

// EncodeCheckpoint consumes and closes the entire checkpoint on every path.
// Every child is read through verified EOF and closed; any late read/close error
// fails the whole output. It does not perform fresh current-state lookups.
// An error may accompany partial or complete bytes; the caller must discard
// them. A public-only decorator may report retained admission failure at Close.
func EncodeCheckpoint(ctx context.Context, w io.Writer, cp Checkpoint) (n int64, err error) {
	if cp == nil {
		return 0, NewError(InvalidRequest)
	}
	b := &budgetWriter{w: w, cap: MaxCheckpointBytes, ctx: ctx}
	if retained, ok := cp.(interface{ retainedContextError() error }); ok {
		b.retainedContextError = retained.retainedContextError
	}
	defer func() {
		if ce := cp.Close(); err == nil && ce != nil {
			err = streamError(ce)
		}
		if err == nil {
			err = b.contextError()
		}
	}()
	if err = b.contextError(); err != nil {
		return 0, err
	}
	s := cp.Snapshot()
	events := cp.Events()
	if err = ValidateSnapshot(s); err != nil {
		return 0, err
	}
	if err = ValidateEvents(s, events); err != nil {
		return 0, err
	}
	defer func() { n = b.n }()
	if err = literal(b, `{"snapshot":`); err != nil {
		return
	}
	if err = writeJSON(b, snapshotValue(s)); err != nil {
		return
	}
	if err = literal(b, `,"events":[`); err != nil {
		return
	}
	for i, e := range events {
		if i > 0 {
			if err = literal(b, ","); err != nil {
				return
			}
		}
		if err = writeJSON(b, eventValue(e)); err != nil {
			return
		}
	}
	if err = literal(b, `],"resources":[`); err != nil {
		return
	}
	for i, r := range s.Resources {
		if err = ctx.Err(); err != nil {
			err = ContextError(err)
			return
		}
		if i > 0 {
			if err = literal(b, ","); err != nil {
				return
			}
		}
		var child io.ReadCloser
		child, err = cp.OpenResource(r.SHA256)
		if err != nil {
			if child != nil {
				_ = child.Close()
			}
			err = streamError(err)
			return
		}
		if child == nil {
			err = NewError(StorageError)
			return
		}
		if err = encodePayload(ctx, b, r, child); err != nil {
			return
		}
	}
	err = literal(b, "]}")
	return
}

// EqualCanonicalSnapshot compares modeled values, including nested null/empty
// distinctions, without changing the provider's required ordering.
func EqualCanonicalSnapshot(a, b Snapshot) bool {
	if preflightJSON(a, MaxSnapshotBytes) != nil || preflightJSON(b, MaxSnapshotBytes) != nil {
		return false
	}
	x, xe := json.Marshal(snapshotValue(a))
	y, ye := json.Marshal(snapshotValue(b))
	return xe == nil && ye == nil && slices.Equal(x, y)
}
