package providerdb

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

// Use the real DB directly: the process controller validates change sets before
// forwarding them and cannot establish that DB.Update owns timestamp refusal.
func TestDirectDBTimestampRefusal(t *testing.T) {
	for _, field := range []string{"CreatedAt", "UpdatedAt"} {
		t.Run(field, func(t *testing.T) {
			db, board, resource := admissionFixture(t, "resource")
			ctx := t.Context()
			vectors, err := cf.Vectors()
			if err != nil {
				t.Fatal(err)
			}
			e := dp.CloneElement(vectors.Fixture.Elements[2])
			actor := vectors.Fixture.Actor
			seed, err := db.Update(ctx, board.Scope, dp.Revision{Scope: board.Scope, Counter: 1}, actor, func(dp.Snapshot) (dp.ChangeSet, error) {
				return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before := observeTimestampDB(t, ctx, db, board.Scope)
			if len(before.snapshot.Resources) != 1 || before.snapshot.Resources[0] != resource || len(before.checkpoint) == 0 {
				t.Fatal("timestamp oracle requires a resource-bearing full checkpoint")
			}
			utc := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
			offset := utc.In(time.FixedZone("offset", 3*60*60))
			if !offset.Equal(utc) || offset.Nanosecond() != utc.Nanosecond() {
				t.Fatal("timestamp pair must differ only in UTC representation")
			}
			setTime := func(value *time.Time) {
				if field == "CreatedAt" {
					e.CreatedAt = value
				} else {
					e.UpdatedAt = value
				}
			}
			setTime(&offset)
			calls := 0
			// No bridge, serialization, validation or callback error intervenes
			// between this original offset change set and the actual DB handle.
			callback := func(dp.Snapshot) (dp.ChangeSet, error) {
				calls++
				return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
			}
			commit, err := db.Update(ctx, board.Scope, seed.Revision, actor, callback)
			code, ok := dp.CodeOf(err)
			if !ok || code != dp.InvalidSnapshot {
				t.Fatalf("DB offset refusal: code=%s err=%v, want invalid_snapshot", code, err)
			}
			if calls != 1 || !reflect.DeepEqual(commit, dp.Commit{}) {
				t.Fatalf("offset callback calls=%d commit=%+v, want one call and zero commit", calls, commit)
			}
			if after := observeTimestampDB(t, ctx, db, board.Scope); !reflect.DeepEqual(after, before) {
				t.Fatal("offset refusal changed full facts, events or checkpoint resource bytes")
			}
			setTime(&utc)
			calls = 0
			commit, err = db.Update(ctx, board.Scope, seed.Revision, actor, callback)
			if err != nil {
				t.Fatal("same-CAS UTC neighbor:", err)
			}
			wantRevision := seed.Revision
			wantRevision.Counter++
			wantCommit := dp.Commit{Revision: wantRevision, ChangedIDs: []string{e.ID}, ChangedResourceIDs: []string{}}
			if calls != 1 || !reflect.DeepEqual(commit, wantCommit) {
				t.Fatalf("UTC callback calls=%d commit=%+v, want one call and %+v", calls, commit, wantCommit)
			}
			after := observeTimestampDB(t, ctx, db, board.Scope)
			wantSnapshot := dp.CloneSnapshot(before.snapshot)
			wantSnapshot.Revision = wantRevision
			wantSnapshot.Elements[0] = dp.CloneElement(e)
			wantEvents := append(dp.CloneEvents(before.events), dp.Event{FormatVersion: 1, Scope: board.Scope, Revision: wantRevision, Actor: actor, ChangedIDs: []string{e.ID}, ChangedResourceIDs: []string{}})
			if !reflect.DeepEqual(after.snapshot, wantSnapshot) || !reflect.DeepEqual(after.events, wantEvents) || !bytes.Equal(after.payload, before.payload) {
				t.Fatal("UTC neighbor did not preserve complete expected facts, events and resource bytes")
			}
			t.Logf("direct DB: offset callback_calls=1 zero_commit=true unchanged_checkpoint_bytes=%d; same-CAS UTC revision=%d callback_calls=%d nanoseconds=%d", len(before.checkpoint), commit.Revision.Counter, calls, utc.Nanosecond())
		})
	}
}

type timestampDBObservation struct {
	snapshot   dp.Snapshot
	events     []dp.Event
	checkpoint []byte
	payload    []byte
}

func observeTimestampDB(t *testing.T, ctx context.Context, db *DB, scope dp.Scope) timestampDBObservation {
	t.Helper()
	snapshot, err := db.View(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := db.Checkpoint(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	events := cp.Events()
	defer func() {
		if err := cp.Close(); err != nil {
			t.Error(err)
		}
	}()
	var payload []byte
	for _, info := range snapshot.Resources {
		r, err := cp.OpenResource(info.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(r, info.Size+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != info.Size {
			t.Fatalf("checkpoint resource read=%v close=%v bytes=%d want=%d", readErr, closeErr, len(data), info.Size)
		}
		payload = append(payload, data...)
	}
	var encoded bytes.Buffer
	if _, err = dp.EncodeCheckpoint(ctx, &encoded, cp); err != nil {
		t.Fatal(err)
	}
	return timestampDBObservation{snapshot, events, encoded.Bytes(), payload}
}
