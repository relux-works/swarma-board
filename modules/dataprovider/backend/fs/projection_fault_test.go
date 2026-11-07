//go:build linux

package fs

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

func subtreeProjection(t *testing.T) (context.Context, *driver, *cf.Instance, dp.Board, dp.Actor, *controlled, dp.Snapshot, []dp.Event, dp.Snapshot, []dp.Event, dp.ChangeSet) {
	t.Helper()
	ctx, d, i, b, a := fixture(t)
	payload := generated('a', 3)
	seed := []dp.Element{
		{ID: "EPIC-1", Type: "epic", Name: "First", Status: "backlog"},
		{ID: "EPIC-2", Type: "epic", Name: "Second", Status: "backlog"},
		{ID: "STORY-1", Type: "story", Name: "Story", ParentID: "EPIC-1", Title: "Original", Status: "backlog"},
		{ID: "TASK-1", Type: "task", Name: "Child", ParentID: "STORY-1", Status: "backlog", ResourceRefs: []dp.ResourceRef{{Name: "old", URI: "urn:sha256:" + payload.Info.SHA256, Type: "outcome"}}},
		{ID: "TASK-2", Type: "task", Name: "Child two", ParentID: "STORY-1", Status: "backlog"},
	}
	_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{Upserts: seed, PutResources: []dp.ResourceInput{payload}, Provenance: []dp.ProvenanceFile{{Path: "provenance/old", Data: []byte("old"), SHA256: hash([]byte("old"))}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := i.Provider.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := i.Provider.Checkpoint(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	events := cp.Events()
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	story := dp.CloneElement(before.Elements[2])
	story.Name = "!!!"
	story.Title = "Moved"
	story.ParentID = "EPIC-2"
	added := dp.Element{ID: "BUG-0", Type: "bug", Name: "Earlier ID", Status: "backlog"}
	nextPayload := generated('b', 3)
	story.ResourceRefs = []dp.ResourceRef{{Name: "new", URI: "urn:sha256:" + nextPayload.Info.SHA256, Type: "precondition"}}
	provenance := []dp.ProvenanceFile{{Path: "provenance/new", Data: []byte("new"), SHA256: hash([]byte("new"))}}
	cs := dp.ChangeSet{Upserts: []dp.Element{story, added}, PutResources: []dp.ResourceInput{nextPayload}, Provenance: provenance}
	want := dp.CloneSnapshot(before)
	want.Revision.Counter++
	want.Elements[2] = story
	want.Elements = append(want.Elements, added)
	slices.SortFunc(want.Elements, func(a, b dp.Element) int { return strings.Compare(a.ID, b.ID) })
	want.Provenance = provenance
	want.Resources = append(want.Resources, nextPayload.Info)
	slices.SortFunc(want.Resources, func(a, b dp.ResourceInfo) int { return strings.Compare(a.SHA256, b.SHA256) })
	wantEvents := append(dp.CloneEvents(events), dp.Event{FormatVersion: 1, Scope: b.Scope, Revision: want.Revision, Actor: a, ChangedIDs: []string{"BUG-0", "STORY-1"}, ChangedResourceIDs: []string{nextPayload.Info.SHA256}})
	return ctx, d, i, b, a, i.Provider.(*controlled), before, events, want, wantEvents, cs
}
func assertProjectionState(t *testing.T, p dp.Provider, ctx context.Context, want dp.Snapshot, events []dp.Event) {
	t.Helper()
	expectExactSnapshot(t, p, ctx, want)
	cp, err := p.Checkpoint(ctx, want.Board.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if !reflect.DeepEqual(cp.Snapshot(), want) || !reflect.DeepEqual(cp.Events(), events) {
		t.Fatal("torn facts/history/provenance/membership")
	}
	for _, info := range want.Resources {
		r, err := cp.OpenResource(info.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		ce := r.Close()
		if err != nil || ce != nil || int64(len(data)) != info.Size || hash(data) != info.SHA256 {
			t.Fatal("torn resource", err, ce)
		}
	}
}
func TestProjectionSubtreeRerankFaults(t *testing.T) {
	// This is a direct provider coherence witness over domain-valid starting
	// facts. Domain destination-conflict validation remains with the domain.
	for _, point := range physicalPoints {
		if point.Stage != cf.BeforeDecision && point.Stage != cf.AfterDecision {
			continue
		}
		modes := []cf.FaultMode{cf.Kill, cf.DefiniteFailure}
		if point.Stage == cf.AfterDecision {
			modes = []cf.FaultMode{cf.Kill, cf.AmbiguousFailure}
		}
		for _, mode := range modes {
			t.Run(point.ID+"/"+string(mode), func(t *testing.T) {
				ctx, d, i, b, a, c, old, oldEvents, want, wantEvents, cs := subtreeProjection(t)
				ticket, err := d.Arm(ctx, c.root, point, mode)
				if err != nil {
					t.Fatal(err)
				}
				defer ticket.Close()
				type outcome struct {
					commit dp.Commit
					err    error
				}
				done := make(chan outcome, 1)
				go func() {
					commit, err := c.Update(ctx, b.Scope, old.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) { return cs, nil })
					done <- outcome{commit, err}
				}()
				if err := ticket.WaitReached(ctx); err != nil {
					t.Fatal(err)
				}
				receipt, err := ticket.Trigger(ctx)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(receipt)
				t.Log(string(raw))
				result := <-done
				if result.err == nil || !reflect.DeepEqual(result.commit, dp.Commit{}) {
					t.Fatal("fault returned success", result)
				}
				if mode == cf.Kill && (receipt.ProcessExit == nil || *receipt.ProcessExit == 0) {
					t.Fatal("missing real process death")
				}
				if mode == cf.DefiniteFailure {
					if code, _ := dp.CodeOf(result.err); code != dp.StorageError {
						t.Fatal(result.err)
					}
					assertProjectionState(t, c, ctx, old, oldEvents)
				}
				if mode == cf.AmbiguousFailure {
					if code, _ := dp.CodeOf(result.err); code != dp.CommitOutcomeUnknown {
						t.Fatal(result.err)
					}
					if code, _ := dp.CodeOf(c.Ready(ctx)); code != dp.Unavailable {
						t.Fatal("ambiguity not latched")
					}
					snap, err := c.View(ctx, b.Scope)
					if code, _ := dp.CodeOf(err); code != dp.Unavailable || !reflect.DeepEqual(snap, dp.Snapshot{}) {
						t.Fatal("ambiguous read exposed facts", err)
					}
				}
				expected, expectedEvents := old, oldEvents
				if point.Stage == cf.AfterDecision {
					expected, expectedEvents = want, wantEvents
				}
				for n := 0; n < 2; n++ {
					p, _, err := i.Restart(ctx)
					if err != nil {
						t.Fatal(err)
					}
					assertProjectionState(t, p, ctx, expected, expectedEvents)
				}
				if point.Stage == cf.AfterDecision {
					// TASK children retained exact values and stay out of ChangedIDs despite
					// both ancestor change and insertion reranking every physical slot.
					if !reflect.DeepEqual(want.Elements[4:], old.Elements[3:]) {
						t.Fatal("descendants changed")
					}
					if !slices.Equal(wantEvents[len(wantEvents)-1].ChangedIDs, []string{"BUG-0", "STORY-1"}) {
						t.Fatal("descendant IDs leaked into event")
					}
				}
			})
		}
	}
}
func TestProjectionRerankRecoveryKill(t *testing.T) {
	ctx, d, i, b, a, c, old, _, want, events, cs := subtreeProjection(t)
	var decision, recovery cf.FaultPoint
	for _, point := range physicalPoints {
		if point.ID == "parent_sync" {
			decision = point
		}
		if point.ID == "recovery" {
			recovery = point
		}
	}
	ticket, err := d.Arm(ctx, c.root, decision, cf.Kill)
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.Close()
	done := make(chan error, 1)
	go func() {
		_, err := c.Update(ctx, b.Scope, old.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) { return cs, nil })
		done <- err
	}()
	if err := ticket.WaitReached(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := ticket.Trigger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(receipt)
	t.Log(string(raw))
	if err := <-done; err == nil {
		t.Fatal("killed update succeeded")
	}
	ticket2, err := d.Arm(ctx, c.root, recovery, cf.Kill)
	if err != nil {
		t.Fatal(err)
	}
	defer ticket2.Close()
	restarted := make(chan error, 1)
	go func() { _, _, err := i.Restart(ctx); restarted <- err }()
	if err := ticket2.WaitReached(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err = ticket2.Trigger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(receipt)
	t.Log(string(raw))
	if receipt.ProcessExit == nil || *receipt.ProcessExit == 0 {
		t.Fatal("recovery process did not die")
	}
	if err := <-restarted; err == nil {
		t.Fatal("killed recovery succeeded")
	}
	for n := 0; n < 2; n++ {
		p, _, err := i.Restart(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertProjectionState(t, p, ctx, want, events)
	}
}
func TestProjectionProviderTopologyAndScope(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	beta := b
	beta.Scope.ProductID = "beta"
	if err := i.Provisioner.EnsureEmpty(ctx, beta); err != nil {
		t.Fatal(err)
	}
	cycle := []dp.Element{{ID: "TASK-1", Type: "task", Name: "!!!", ParentID: "TASK-2", Status: "backlog"}, {ID: "TASK-2", Type: "task", Name: strings.Repeat("a", 300), ParentID: "TASK-1", Status: "backlog"}}
	for _, board := range []dp.Board{b, beta} {
		_, err := i.Provider.Update(ctx, board.Scope, dp.Revision{Scope: board.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{Upserts: cycle}, nil })
		if err != nil {
			t.Fatal("provider added a topology/slug rule", err)
		}
	}
	alpha, err := i.Provider.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	other, err := i.Provider.View(ctx, beta.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(alpha.Elements, cycle) || !reflect.DeepEqual(other.Elements, cycle) {
		t.Fatal("provider repaired topology")
	}
	c := i.Provider.(*controlled)
	dirA := generationFixture(t, c, b.Scope)
	dirB := generationFixture(t, c, beta.Scope)
	if dirA == dirB {
		t.Fatal("scopes aliased")
	}
	// Delete reranks only alpha; beta keeps its complete independent generation.
	_, err = i.Provider.Update(ctx, b.Scope, alpha.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{Deletes: []string{"TASK-1"}}, nil })
	if err != nil {
		t.Fatal(err)
	}
	expectExactSnapshot(t, i.Provider, ctx, other)
	after, err := i.Provider.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Elements, cycle[1:]) {
		t.Fatal("delete/rerank repaired orphan")
	}
	// A cross-scope projection transplant with coherent digest metadata refuses.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := readFixture(t, filepath.Join(dirB, "projection.json"))
	dirA = generationFixture(t, c, b.Scope)
	rebindProjection(t, c.root, dirA, b.Scope, manifest)
	before, err := fingerprintTree(c.root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(ctx, c.root)
	if p != nil {
		_ = p.Close()
	}
	if err == nil {
		t.Fatal("cross-scope manifest admitted")
	}
	afterTree, err := fingerprintTree(c.root)
	if err != nil || before != afterTree {
		t.Fatal("cross-scope refusal changed bytes", err)
	}
}
