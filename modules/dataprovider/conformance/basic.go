package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type providerCase func(*testing.T, *session) string

var providerCases = map[string]providerCase{
	"C01": provisionCase, "C02": unknownCase, "C03": isolationCase, "C04": scopeCase,
	"C05": casCase, "C06": writersCase, "C07": rollbackCase, "C08": detachCase,
	"C09": noopCase, "C10": invalidCase, "C11": cancelCase, "C12": overflowCase,
	"C13": concurrentFaultCase, "C14": preDecisionCase, "C15": postDecisionCase, "C16": ambiguousCase,
	"C17": historyCase, "C18": orderingCase, "C19": metadataBoundsCase, "C20": schemaCase,
	"C21": closeCase, "C22": peerCase, "C31": rootCase,
	"C33": streamCase, "C34": resourceRefusalCase, "C35": resourceIsolationCase,
	"C36": checkpointCase, "C37": resourceBoundsCase, "C38": resourceCorruptionCase,
	"C40": resourceChangesCase, "C41": deadlinesCase, "C46": capacityCase, "C49": strictTreeCase,
}

func provisionCase(t *testing.T, s *session) string {
	for _, b := range s.boards {
		must(t, s.provision.EnsureEmpty(s.ctx, b))
		before := s.observe(b.Scope)
		equal(t, before.snapshot, dp.Snapshot{Board: b, Revision: dp.Revision{Scope: b.Scope}, Elements: []dp.Element{}, Provenance: []dp.ProvenanceFile{}, Resources: []dp.ResourceInfo{}})
		equal(t, before.events, []dp.Event{})
		must(t, s.provision.EnsureEmpty(s.ctx, b))
		s.unchanged(before)
	}
	s.setup()
	for _, b := range s.boards {
		before := s.observe(b.Scope)
		must(t, s.provision.EnsureEmpty(s.ctx, b))
		s.unchanged(before)
	}
	return ""
}
func unknownCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	for _, field := range []string{"name", "description"} {
		b := s.boards[0]
		if field == "name" {
			b.Name = "different"
		} else {
			b.Description = "different"
		}
		code(t, s.provision.EnsureEmpty(s.ctx, b), dp.ScopeConflict)
		s.unchanged(old)
	}
	u := dp.Scope{ProductID: "unknown", BoardID: "absent"}
	v, err := s.p.View(s.ctx, u)
	code(t, err, dp.NotFound)
	equal(t, v, dp.Snapshot{})
	cp, err := s.p.Checkpoint(s.ctx, u)
	if cp != nil {
		_ = cp.Close()
	}
	code(t, err, dp.NotFound)
	if cp != nil {
		t.Fatal("reader returned on refusal")
	}
	calls := 0
	c, err := s.p.Update(s.ctx, u, dp.Revision{Scope: u}, s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	code(t, err, dp.NotFound)
	zeroCommit(t, c)
	equal(t, calls, 0)
	_, err = s.p.View(s.ctx, u)
	code(t, err, dp.NotFound)
	s.unchanged(old)
	return ""
}
func isolationCase(t *testing.T, s *session) string {
	s.setup()
	a := s.observe(s.scope())
	b := s.observe(s.boards[1].Scope)
	c := s.observe(s.boards[2].Scope)
	_, err := s.update(1, "one", "changed")
	must(t, err)
	now := s.expectNotes(a, "one", "changed")
	s.unchanged(b)
	s.unchanged(c)
	s.reopen()
	s.unchanged(now)
	s.unchanged(b)
	s.unchanged(c)
	return ""
}
func scopeCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	bad := []string{"", "../x", "a/b", "a\\b", "%2f", "UPPER", "a\x00b", strings.Repeat("a", 65), "аlpha"}
	for _, id := range bad {
		for _, product := range []bool{true, false} {
			scope := s.scope()
			if product {
				scope.ProductID = id
			} else {
				scope.BoardID = id
			}
			code(t, s.provision.EnsureEmpty(s.ctx, dp.Board{Scope: scope, Name: "invalid"}), dp.InvalidRequest)
			v, err := s.p.View(s.ctx, scope)
			code(t, err, dp.InvalidRequest)
			equal(t, v, dp.Snapshot{})
			r, err := s.p.ReadResource(s.ctx, scope, abcDigest)
			if r.Reader != nil {
				_ = r.Reader.Close()
			}
			code(t, err, dp.InvalidRequest)
			equal(t, r, dp.ResourceReader{})
			cp, err := s.p.Checkpoint(s.ctx, scope)
			if cp != nil {
				_ = cp.Close()
			}
			code(t, err, dp.InvalidRequest)
			if cp != nil {
				t.Fatal("checkpoint on invalid scope")
			}
			calls := 0
			c, err := s.p.Update(s.ctx, scope, dp.Revision{Scope: scope}, s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
			code(t, err, dp.InvalidRequest)
			zeroCommit(t, c)
			equal(t, calls, 0)
		}
	}
	calls := 0
	c, err := s.p.Update(s.ctx, s.scope(), dp.Revision{Scope: s.boards[1].Scope, Counter: 1}, s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	code(t, err, dp.InvalidRequest)
	zeroCommit(t, c)
	equal(t, calls, 0)
	s.unchanged(old)
	return ""
}
func casCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	c, err := s.update(1, "one", "changed")
	must(t, err)
	equal(t, c, dp.Commit{Revision: s.rev(2), ChangedIDs: []string{"TASK-1"}, ChangedResourceIDs: []string{}})
	now := s.expectNotes(old, "one", "changed")
	calls := 0
	c, err = s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	code(t, err, dp.RevisionConflict)
	zeroCommit(t, c)
	equal(t, calls, 0)
	s.unchanged(now)
	return ""
}
func writersCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	start := make(chan struct{})
	leaderEntered := make(chan struct{})
	releaseLeader := make(chan struct{})
	followersStarted := make(chan struct{}, 15)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseLeader) })
	var wg sync.WaitGroup
	type result struct {
		c     dp.Commit
		err   error
		calls int
		id    string
	}
	results := make(chan result, 16)
	for i := 0; i < 16; i++ {
		id := caseID(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id == "C00" {
				<-start
			} else {
				<-leaderEntered
				followersStarted <- struct{}{}
			}
			a := s.actor
			a.RequestID = id
			calls := 0
			c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), a, func(snap dp.Snapshot) (dp.ChangeSet, error) {
				calls++
				if id == "C00" {
					close(leaderEntered)
					<-releaseLeader
				}
				e := snap.Elements[2]
				e.Notes = id
				return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
			})
			results <- result{c, err, calls, id}
		}()
	}
	close(start)
	for i := 0; i < 15; i++ {
		select {
		case <-followersStarted:
		case <-s.ctx.Done():
			t.Fatal("leader/follower admission barrier exceeded bound")
		}
	}
	releaseOnce.Do(func() { close(releaseLeader) })
	wg.Wait()
	close(results)
	wins := 0
	winner := ""
	for r := range results {
		if r.calls > 1 {
			t.Fatal("callback rerun")
		}
		if r.err == nil {
			wins++
			winner = r.id
			equal(t, r.calls, 1)
			equal(t, r.c, dp.Commit{Revision: s.rev(2), ChangedIDs: []string{"TASK-1"}, ChangedResourceIDs: []string{}})
		} else {
			code(t, r.err, dp.RevisionConflict)
			zeroCommit(t, r.c)
			equal(t, r.calls, 0)
		}
	}
	equal(t, wins, 1)
	equal(t, winner, "C00")
	s.expectNotes(old, winner, winner)
	return ""
}
func rollbackCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	sentinel := errors.New("synthetic-domain-error")
	calls := 0
	c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(snap dp.Snapshot) (dp.ChangeSet, error) {
		calls++
		snap.Elements[2].Notes = "must not leak"
		snap.Elements[2].ResourceRefs = append(snap.Elements[2].ResourceRefs, dp.ResourceRef{Name: "inert"})
		return dp.ChangeSet{}, sentinel
	})
	if err != sentinel {
		t.Fatal("callback error was changed")
	}
	equal(t, calls, 1)
	zeroCommit(t, c)
	s.unchanged(old)
	_, err = s.update(1, "one", "changed")
	must(t, err)
	s.expectNotes(old, "one", "changed")
	return ""
}
func detachCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	view, err := s.p.View(s.ctx, s.scope())
	must(t, err)
	view.Elements[2].Notes = "alias"
	view.Elements[2].Estimate.Kind = "alias"
	s.unchanged(old)
	cp, err := s.p.Checkpoint(s.ctx, s.scope())
	closeOnRefusal(err, cp)
	must(t, err)
	defer cp.Close()
	snap := cp.Snapshot()
	snap.Elements[2].Notes = "alias"
	events := cp.Events()
	events[0].ChangedIDs[0] = "alias"
	equal(t, cp.Snapshot(), old.snapshot)
	equal(t, cp.Events(), old.events)
	must(t, cp.Close())
	e := dp.CloneElement(s.seed[2])
	e.Notes = "changed"
	e.Checklist = []dp.ChecklistItem{{Text: "original", Checked: true}}
	e.ResourceRefs = []dp.ResourceRef{{Name: "external", URI: "https://example.invalid/resource", Type: "outcome"}}
	changes := dp.ChangeSet{Upserts: []dp.Element{e}, Provenance: []dp.ProvenanceFile{provenance("copy", "inert")}}
	_, err = s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { return changes, nil })
	must(t, err)
	now := s.observe(s.scope())
	changes.Upserts[0].Checklist[0].Text = "alias"
	changes.Upserts[0].ResourceRefs[0].URI = "urn:changed"
	changes.Upserts[0].Estimate.Kind = "alias"
	changes.Provenance[0].Data[0] = 'X'
	s.unchanged(now)
	// One-shot input and output ownership is exercised by the same shared C33.
	verifyOneShot(t, s)
	return ""
}
func noopCase(t *testing.T, s *session) string {
	s.setup()
	n := uint64(1)
	for _, cs := range []dp.ChangeSet{{}, {Provenance: []dp.ProvenanceFile{provenance("one", "inert")}}, {Provenance: nil}, {Provenance: []dp.ProvenanceFile{}}} {
		old := s.observe(s.scope())
		c, err := s.p.Update(s.ctx, s.scope(), s.rev(n), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { return cs, nil })
		must(t, err)
		n++
		equal(t, c, dp.Commit{Revision: s.rev(n), ChangedIDs: []string{}, ChangedResourceIDs: []string{}})
		now := s.observe(s.scope())
		want := dp.CloneSnapshot(old.snapshot)
		want.Revision = s.rev(n)
		if cs.Provenance != nil {
			want.Provenance = cs.Provenance
		}
		equal(t, now.snapshot, want)
		equal(t, now.events, append(dp.CloneEvents(old.events), dp.Event{FormatVersion: 1, Scope: s.scope(), Revision: s.rev(n), Actor: s.actor, ChangedIDs: []string{}, ChangedResourceIDs: []string{}}))
	}
	return ""
}
func provenance(name, data string) dp.ProvenanceFile {
	h := sha256.Sum256([]byte(data))
	return dp.ProvenanceFile{Path: "provenance/" + name, SHA256: hex.EncodeToString(h[:]), Data: []byte(data)}
}
func invalidCase(t *testing.T, s *session) string {
	s.setup()
	e := dp.CloneElement(s.seed[2])
	lower := e
	lower.ID = "task-1"
	malformed := e
	malformed.ID = "invalid"
	for _, x := range []struct {
		c    dp.ChangeSet
		code dp.Code
	}{
		{dp.ChangeSet{Upserts: []dp.Element{e, e}}, dp.InvalidRequest},
		{dp.ChangeSet{Upserts: []dp.Element{e, lower}}, dp.InvalidRequest},
		{dp.ChangeSet{Deletes: []string{"TASK-1", "task-1"}}, dp.InvalidRequest},
		{dp.ChangeSet{Upserts: []dp.Element{e}, Deletes: []string{"task-1"}}, dp.InvalidRequest},
		{dp.ChangeSet{Deletes: []string{"TASK-999"}}, dp.NotFound},
		{dp.ChangeSet{Upserts: []dp.Element{malformed}}, dp.InvalidSnapshot},
		{dp.ChangeSet{Provenance: []dp.ProvenanceFile{provenance("same", "a"), provenance("same", "a")}}, dp.InvalidSnapshot},
		{dp.ChangeSet{Provenance: []dp.ProvenanceFile{provenance("../escape", "a")}}, dp.InvalidSnapshot},
		{dp.ChangeSet{Provenance: []dp.ProvenanceFile{{Path: "provenance/bad", SHA256: abcDigest, Data: []byte("wrong")}}}, dp.InvalidSnapshot},
	} {
		s.refuse(x.c, x.code)
	}
	return ""
}
func cancelCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, nil)
	code(t, err, dp.InvalidRequest)
	zeroCommit(t, c)
	for _, a := range []dp.Actor{{}, {AccountID: "x", RequestID: "x", Operation: "set_notes"}, {AccountID: "x", RequestID: "x", Operation: "unknown", At: s.actor.At}, {AccountID: "x\n", RequestID: "x", Operation: "set_notes", At: s.actor.At}} {
		calls := 0
		c, err = s.p.Update(s.ctx, s.scope(), s.rev(1), a, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
		code(t, err, dp.InvalidRequest)
		zeroCommit(t, c)
		equal(t, calls, 0)
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(s.ctx)
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(s.ctx, time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		calls := 0
		c, err = s.p.Update(ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("context error not recognizable: %v", err)
		}
		zeroCommit(t, c)
		equal(t, calls, 0)
		s.unchanged(old)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct {
		c   dp.Commit
		err error
	}, 1)
	go func() {
		c, err := s.p.Update(ctx, s.scope(), s.rev(1), s.actor, func(snap dp.Snapshot) (dp.ChangeSet, error) {
			close(entered)
			<-release
			e := snap.Elements[2]
			e.Notes = "canceled"
			return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
		})
		done <- struct {
			c   dp.Commit
			err error
		}{c, err}
	}()
	select {
	case <-entered:
	case <-s.ctx.Done():
		t.Fatal("callback did not start")
	}
	cancel()
	close(release)
	r := <-done
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("cancellation not recognizable: %v", r.err)
	}
	zeroCommit(t, r.c)
	s.unchanged(old)
	return ""
}

func orderingCase(t *testing.T, s *session) string {
	s.setup()
	e := dp.CloneElement(s.seed[2])
	e.Checklist = []dp.ChecklistItem{{Text: "z"}, {Text: "a", Checked: true}}
	e.BlockedBy = []string{"TASK-3", "TASK-2"}
	e.ManualBlockedBy = []string{"TASK-3", "TASK-2"}
	e.ResourceRefs = []dp.ResourceRef{{Name: "z", URI: "urn:example:z", Type: "outcome"}, {Name: "a", URI: "https://example.invalid/a", Type: "precondition"}}
	at := s.actor.At
	e.CreatedAt = &at
	bug := dp.CloneElement(e)
	bug.ID = "BUG-2"
	bug.Type = "bug"
	bug.Name = "bug"
	bug.Estimate = nil
	bug.CreatedAt = nil
	bug.BlockedBy = nil
	bug.ManualBlockedBy = []string{}
	second, third := dp.CloneElement(s.seed[2]), dp.CloneElement(s.seed[2])
	second.ID, third.ID = "TASK-2", "TASK-3"
	story := dp.CloneElement(s.seed[1])
	otherStory := dp.CloneElement(story)
	otherStory.ID = "STORY-2"
	thirdStory := dp.CloneElement(story)
	thirdStory.ID = "STORY-3"
	story.BlockedBy = []string{"STORY-3", "STORY-2"}
	story.ManualBlockedBy = []string{"STORY-3", "STORY-2"}
	c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) {
		// Keep the oracle detached from callback-owned arrays: an adapter
		// mutating its input must not mutate the expected nested order too.
		return dp.ChangeSet{Upserts: cloneSeed([]dp.Element{third, e, thirdStory, otherStory, bug, second, story}), Provenance: []dp.ProvenanceFile{provenance("z", "z"), provenance("a", "a")}}, nil
	})
	must(t, err)
	equal(t, c.ChangedIDs, []string{"BUG-2", "STORY-1", "STORY-2", "STORY-3", "TASK-1", "TASK-2", "TASK-3"})
	now := s.observe(s.scope())
	equal(t, now.snapshot.Elements, []dp.Element{bug, s.seed[0], story, otherStory, thirdStory, e, second, third})
	equal(t, now.snapshot.Provenance, []dp.ProvenanceFile{provenance("a", "a"), provenance("z", "z")})
	s.reopen()
	s.unchanged(now)
	return ""
}
