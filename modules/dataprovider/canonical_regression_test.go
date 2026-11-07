package dataprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The writer is cooperative at Write boundaries. No test claims interruption
// of an arbitrary writer blocked inside Write.
type cancellationWriter struct {
	out    bytes.Buffer
	cancel context.CancelFunc
	at     int
	writes int
}

func (w *cancellationWriter) Write(p []byte) (int, error) {
	w.writes++
	n, err := w.out.Write(p)
	if w.writes == w.at {
		w.cancel()
	}
	return n, err
}

type finalizingCheckpoint struct {
	Checkpoint
	finalize func() error
	once     sync.Once
	err      error
}

func (c *finalizingCheckpoint) Close() error {
	c.once.Do(func() {
		c.err = c.Checkpoint.Close()
		if err := c.finalize(); c.err == nil {
			c.err = err
		}
	})
	return c.err
}

func TestCheckpointCancellationAcrossMetadataHistoryAndFinalization(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	for _, history := range []bool{false, true} {
		snap := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}}
		var events []Event
		if history {
			snap.Revision.Counter = 1
			events = []Event{{FormatVersion: 1, Scope: scope, Revision: snap.Revision, Actor: Actor{AccountID: "control", RequestID: "history", Operation: "import", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, ChangedIDs: []string{}, ChangedResourceIDs: []string{}}}
		}
		// Writes 1/2 are envelope/snapshot, 4 is the event when present;
		// final is the last envelope Write. Cancellation must never become success.
		positions := []int{1, 2, 5}
		if history {
			positions = []int{1, 2, 4, 6}
		}
		for _, at := range positions {
			t.Run(strings.Join([]string{map[bool]string{false: "empty", true: "history"}[history], string(rune('0' + at))}, "/"), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				released := make(chan struct{})
				cp, err := OpenCheckpoint(ctx, snap, events, ResourceBacking{Release: func() error { close(released); return nil }})
				if err != nil {
					t.Fatal(err)
				}
				w := &cancellationWriter{cancel: cancel, at: at}
				n, err := EncodeCheckpoint(ctx, w, cp)
				if !errors.Is(err, context.Canceled) || n != int64(w.out.Len()) {
					t.Fatalf("canceled encoding: bytes=%d actual=%d err=%v", n, w.out.Len(), err)
				}
				if w.writes != at {
					t.Fatalf("wrote after cancellation: got %d writes, canceled at %d", w.writes, at)
				}
				select {
				case <-released:
				case <-time.After(time.Second):
					t.Fatal("checkpoint backing not released")
				}
			})
		}
	}
	t.Run("close-cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		snap := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}}
		cp, err := OpenCheckpoint(ctx, snap, nil, ResourceBacking{Release: func() error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		wrapped := &finalizingCheckpoint{Checkpoint: cp, finalize: func() error { cancel(); return nil }}
		_, err = EncodeCheckpoint(ctx, io.Discard, wrapped)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("finalization lost cancellation: %v", err)
		}
	})
	t.Run("late-close-error", func(t *testing.T) {
		ctx := t.Context()
		snap := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}}
		cp, err := OpenCheckpoint(ctx, snap, nil, ResourceBacking{Release: func() error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		wrapped := &finalizingCheckpoint{Checkpoint: cp, finalize: func() error { return errors.New("synthetic close failure") }}
		_, err = EncodeCheckpoint(ctx, io.Discard, wrapped)
		requireCode(t, err, StorageError)
	})
}

func TestIndividuallyInvalidUTF8IsRefusedBeforeCheckpointOwnership(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	first, second := string([]byte{0xc3}), string([]byte{0xa9})
	element := Element{ID: "TASK-1", Type: "task", Name: "valid", Status: "backlog"}
	base := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}, Elements: []Element{element}}
	cases := map[string]func(*Snapshot){
		"split-board": func(s *Snapshot) { s.Board.Name = first; s.Board.Description = second },
		"split-resource": func(s *Snapshot) {
			s.Elements[0].ResourceRefs = []ResourceRef{{Name: "valid", URI: "urn:example:" + first, Description: second, Type: "outcome"}}
		},
		"split-blocked-edges": func(s *Snapshot) { s.Elements[0].BlockedBy = []string{first, second} },
		"split-manual-edges":  func(s *Snapshot) { s.Elements[0].ManualBlockedBy = []string{first, second} },
		"nested-checklist":    func(s *Snapshot) { s.Elements[0].Checklist = []ChecklistItem{{Text: first}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := CloneSnapshot(base)
			mutate(&s)
			requireCode(t, ValidateSnapshot(s), InvalidSnapshot)
			releases := 0
			cp, err := OpenCheckpoint(t.Context(), s, nil, ResourceBacking{Release: func() error { releases++; return nil }})
			requireCode(t, err, StorageError)
			if cp != nil || releases != 1 {
				t.Fatalf("malformed committed text exposed ownership: cp=%v releases=%d", cp, releases)
			}
		})
	}
	// Valid multibyte text and nested order/null distinctions stay representable.
	good := CloneSnapshot(base)
	good.Board.Name = "é"
	good.Elements[0].Checklist = []ChecklistItem{{Text: "日本語"}}
	if err := ValidateSnapshot(good); err != nil {
		t.Fatal(err)
	}
}

// This witness delegates to the actual Snapshot marshal representation. A tiny
// budget must refuse its metadata before MarshalJSON is entered, not merely
// before its allocated result reaches the writer. Inputs are at most 2KiB.
type snapshotMarshalWitness struct {
	Snapshot Snapshot
	calls    *int
}

func (v snapshotMarshalWitness) MarshalJSON() ([]byte, error) {
	*v.calls++
	return json.Marshal(snapshotValue(v.Snapshot))
}
func TestSnapshotPreflightPrecedesWholeJSONAllocation(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	base := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}, Elements: []Element{{ID: "TASK-1", Type: "task", Name: "valid", Status: "backlog"}}}
	for _, kind := range []string{"notes", "provenance", "nested-empty-items"} {
		t.Run(kind, func(t *testing.T) {
			s := CloneSnapshot(base)
			switch kind {
			case "notes":
				s.Elements[0].Notes = strings.Repeat("x", 2048)
			case "provenance":
				s.Provenance = []ProvenanceFile{{Data: make([]byte, 1024)}, {Data: make([]byte, 1024)}}
			case "nested-empty-items":
				s.Elements[0].Checklist = make([]ChecklistItem, 256)
			}
			calls := 0
			var output bytes.Buffer
			b := &budgetWriter{w: &output, cap: 128}
			err := writeJSON(b, snapshotMarshalWitness{Snapshot: s, calls: &calls})
			requireCode(t, err, PayloadTooLarge)
			if calls != 0 || output.Len() != 0 || b.n != 0 {
				t.Fatalf("preflight was too late: marshal_calls=%d output=%d", calls, output.Len())
			}
		})
	}
}

func TestPreflightRetainsExactCanonicalEncodingBounds(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := Snapshot{Board: Board{Scope: scope, Name: "<&\"é"}, Revision: Revision{Scope: scope}, Elements: []Element{{ID: "TASK-1", Type: "task", Name: "valid", Status: "backlog", CreatedAt: &at, BlockedBy: []string{}, Checklist: []ChecklistItem{{Text: ""}, {Text: "日本語"}}, Estimate: &Estimate{Kind: "notEstimated"}}}, Provenance: []ProvenanceFile{{Path: "provenance/a", Data: []byte{0, 1, 2}}}}
	for _, value := range []any{snapshotValue(s), eventValue(Event{Scope: scope}), provenanceValue(s.Provenance), s.Elements, Commit{Revision: s.Revision}} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		b := &budgetWriter{w: &got, cap: int64(len(want))}
		if err = writeJSON(b, value); err != nil || !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("exact encoding changed: %v", err)
		}
		b = &budgetWriter{w: io.Discard, cap: int64(len(want) - 1)}
		requireCode(t, writeJSON(b, value), PayloadTooLarge)
	}
}
