package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

func auditBytes(t *testing.T, d *DB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if e := d.db.Bolt.View(func(tx *bolt.Tx) error {
		return tx.Bucket(audits).ForEach(func(k, v []byte) error { out[string(k)] = bytes.Clone(v); return nil })
	}); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestExportViewDetachedHistoryAppendAndRollback(t *testing.T) {
	d, path := openTest(t)
	s, events, e := d.ExportView(ctx, "alpha")
	if e != nil || s.Revision != 0 || len(events) != 0 {
		t.Fatal(s, events, e)
	}
	seed(t, d)
	before := auditBytes(t, d)
	for _, reject := range []string{"callback", "precommit", "cas"} {
		t.Run(reject, func(t *testing.T) {
			expected := contract.Revision(1)
			if reject == "cas" {
				expected = 0
			}
			if reject == "precommit" {
				d.beforeCommit = func() error { return errors.New("fault") }
			}
			_, e := d.Update(ctx, "alpha", expected, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
				if reject == "callback" {
					return contract.ChangeSet{}, errors.New("refused")
				}
				return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c"}}}, nil
			})
			d.beforeCommit = nil
			if e == nil {
				t.Fatal("refusal committed")
			}
			s, events, e := d.ExportView(ctx, "alpha")
			if e != nil || s.Revision != 1 || len(s.Elements) != 2 || len(events) != 1 || !reflect.DeepEqual(before, auditBytes(t, d)) {
				t.Fatal("partial audit/state", s, events, e)
			}
		})
	}
	_, e = d.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Deletes: []string{"TASK-b"}, Upserts: []contract.Element{{ID: "TASK-z"}}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	after := auditBytes(t, d)
	if len(after) != 2 || !bytes.Equal(after[string(u64(1))], before[string(u64(1))]) {
		t.Fatal("prior event rewritten")
	}
	// Existing persisted encoding is exactly the old AuditRecord: no schema or
	// field change, no hash-chain/digest invented for missing historical data.
	want, _ := json.Marshal(AuditRecord{Revision: 2, Context: actor, ChangedIDs: []string{"TASK-b", "TASK-z"}})
	if !bytes.Equal(after[string(u64(2))], want) {
		t.Fatalf("v1 encoding changed: %s", after[string(u64(2))])
	}
	s, events, e = d.ExportView(ctx, "alpha")
	if e != nil || s.Revision != 2 || len(events) != 2 || events[1].Revision != s.Revision {
		t.Fatal(s, events, e)
	}
	s.Elements[0].BlockedBy[0] = "mutated"
	s.Provenance[0].Data[0] = 'X'
	events[0].ChangedIDs[0] = "mutated"
	events[0].Context.AccountID = "mutated"
	fresh, history, e := d.ExportView(ctx, "alpha")
	if e != nil || fresh.Elements[0].BlockedBy[0] != "TASK-b" || string(fresh.Provenance[0].Data) != "inert" || history[0].ChangedIDs[0] != "TASK-a" || history[0].Context.AccountID != actor.AccountID {
		t.Fatal("export aliases storage", e)
	}
	if e = d.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path, project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s, events, e = reopened.ExportView(ctx, "alpha")
	if e != nil || !reflect.DeepEqual(s, fresh) || !reflect.DeepEqual(events, history) {
		t.Fatal("restart export mismatch", e)
	}
	if s, events, e = reopened.ExportView(ctx, "foreign"); code(e) != "not_found" || s.Project.ID != "" || events != nil {
		t.Fatal(s, events, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if s, events, e = reopened.ExportView(cancelled, "alpha"); !errors.Is(e, context.Canceled) || s.Project.ID != "" || events != nil {
		t.Fatal(s, events, e)
	}
}

func TestExportViewCoherentDuringWrites(t *testing.T) {
	d, _ := openTest(t)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 80; i++ {
			a := actor
			a.RequestID = fmt.Sprint(i + 1)
			_, e := d.Update(ctx, "alpha", contract.Revision(i), a, func(s contract.Snapshot) (contract.ChangeSet, error) {
				text := fmt.Sprint(s.Revision + 1)
				return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-a", Notes: text}}, Provenance: []contract.ProvenanceFile{{Path: "provenance/revision", Data: []byte(text)}}}, nil
			})
			if e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	check := func() {
		t.Helper()
		s, events, e := d.ExportView(ctx, "alpha")
		if e != nil || len(events) != int(s.Revision) {
			t.Fatal("torn journal", s.Revision, len(events), e)
		}
		if s.Revision > 0 {
			text := fmt.Sprint(s.Revision)
			last := events[len(events)-1]
			if len(s.Elements) != 1 || s.Elements[0].Notes != text || len(s.Provenance) != 1 || string(s.Provenance[0].Data) != text || last.Context.RequestID != text || last.Revision != s.Revision {
				t.Fatal("torn state and event", s, events)
			}
		}
	}
	for i := 0; i < 80; i++ {
		check()
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	check()
}

func TestExportViewRefusesBrokenHistoryAndAppendOverwrite(t *testing.T) {
	cases := map[string]func(*bolt.Bucket) error{
		"missing": func(b *bolt.Bucket) error { return b.Delete(u64(1)) },
		"extra": func(b *bolt.Bucket) error {
			return b.Put(u64(2), []byte(`{"Revision":2,"Context":{},"ChangedIDs":[]}`))
		},
		"bad-key":  func(b *bolt.Bucket) error { return b.Put([]byte("bad"), []byte(`{}`)) },
		"bad-json": func(b *bolt.Bucket) error { return b.Put(u64(1), []byte(`{`)) },
		"wrong-revision": func(b *bolt.Bucket) error {
			return b.Put(u64(1), []byte(`{"Revision":2,"Context":{},"ChangedIDs":[]}`))
		},
		"unknown-fields": func(b *bolt.Bucket) error {
			return b.Put(u64(1), []byte(`{"Revision":1,"Context":{},"ChangedIDs":[],"Digest":"unapproved"}`))
		},
		"unordered-ids": func(b *bolt.Bucket) error {
			return b.Put(u64(1), []byte(`{"Revision":1,"Context":{},"ChangedIDs":["z","a"]}`))
		},
		"duplicate-ids": func(b *bolt.Bucket) error {
			return b.Put(u64(1), []byte(`{"Revision":1,"Context":{},"ChangedIDs":["a","a"]}`))
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			d, _ := openTest(t)
			seed(t, d)
			if e := d.db.Bolt.Update(func(tx *bolt.Tx) error { return damage(tx.Bucket(audits)) }); e != nil {
				t.Fatal(e)
			}
			s, events, e := d.ExportView(ctx, "alpha")
			if code(e) != "storage_error" || s.Project.ID != "" || events != nil {
				t.Fatal("partial/damaged export accepted", s, events, e)
			}
			before := auditBytes(t, d)
			// Broken tails must refuse before callbacks and must never overwrite the
			// unexpected next event. Interior corruption is caught by ExportView.
			if name == "extra" || name == "missing" || name == "bad-key" || name == "bad-json" || name == "wrong-revision" || name == "unknown-fields" {
				_, e = d.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
					t.Fatal("damaged append invoked callback")
					return contract.ChangeSet{}, nil
				})
				if code(e) != "storage_error" || !reflect.DeepEqual(before, auditBytes(t, d)) {
					t.Fatal("damaged history overwritten", e)
				}
			}
		})
	}
}

func TestExportViewJournalLimitRefusesBeforeDecode(t *testing.T) {
	d, _ := openTest(t)
	// Invalid JSON larger than the production bound must report overflow before
	// attempting to decode or returning a snapshot/partial event stream.
	v := bytes.Repeat([]byte(" "), JournalLimit)
	if e := d.db.Bolt.Update(func(tx *bolt.Tx) error {
		if e := tx.Bucket(meta).Put([]byte("revision"), u64(1)); e != nil {
			return e
		}
		return tx.Bucket(audits).Put(u64(1), v)
	}); e != nil {
		t.Fatal(e)
	}
	s, events, e := d.ExportView(ctx, "alpha")
	if code(e) != "payload_too_large" || s.Project.ID != "" || events != nil {
		t.Fatal(s, events, e)
	}
}

func TestExportViewWhileTransactionIsUncommitted(t *testing.T) {
	d, _ := openTest(t)
	seed(t, d)
	before, historyBefore, e := d.ExportView(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	d.beforeCommit = func() error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() {
		_, e := d.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-new", Notes: "committing"}}}, nil
		})
		done <- e
	}()
	<-entered
	// State, revision and the next event are already written into the writer's
	// private transaction, but an independent ExportView must see only revision 1.
	s, history, readErr := d.ExportView(ctx, "alpha")
	close(release)
	writeErr := <-done
	d.beforeCommit = nil
	if readErr != nil || writeErr != nil || !reflect.DeepEqual(s, before) || !reflect.DeepEqual(history, historyBefore) {
		t.Fatal("uncommitted state/event escaped", readErr, writeErr)
	}
	s, history, e = d.ExportView(ctx, "alpha")
	if e != nil || s.Revision != 2 || len(s.Elements) != 3 || len(history) != 2 || history[1].Revision != s.Revision {
		t.Fatal(s, history, e)
	}
}
