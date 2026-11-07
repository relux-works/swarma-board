package conformance

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func mappedPoints(t *testing.T, s *session, stage Stage) ([]FaultPoint, string) {
	t.Helper()
	if gap := missingFaults(s); gap != "" {
		return nil, gap
	}
	seen := map[string]bool{}
	phases := map[string]bool{}
	out := []FaultPoint{}
	for _, p := range s.faults.Points() {
		if p.ID == "" || strings.TrimSpace(p.Physical) == "" || seen[p.ID] {
			t.Fatal("fault points require unique IDs and concrete physical mappings")
		}
		seen[p.ID] = true
		if p.FSPhase != "" {
			phases[p.FSPhase] = true
		}
		if p.Stage == stage {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, "No justified real fault point for " + string(stage)
	}
	if s.name == "filesystem" {
		for _, p := range []string{"journal_sync", "first_write", "last_write", "rename", "resource_payload", "resource_membership", "revision_event", "parent_sync", "cleanup"} {
			if !phases[p] {
				return nil, "Mandatory FS phase mapping missing: " + p
			}
		}
	}
	return out, ""
}
func overflowCase(t *testing.T, s *session) string {
	s.setup()
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	must(t, s.p.Close())
	must(t, s.faults.InstallRevision(s.ctx, s.root, s.scope(), math.MaxUint64))
	p, pr, err := s.instance.Restart(s.ctx)
	if err != nil {
		return "Counter fixture could not be admitted; max revision is unreachable under contiguous finite event cap (see handoff)"
	}
	s.p = p
	s.provision = pr
	calls := 0
	c, err := s.p.Update(s.ctx, s.scope(), s.rev(math.MaxUint64), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	code(t, err, dp.Unavailable)
	zeroCommit(t, c)
	equal(t, calls, 0)
	return "Overflow refusal observed on injected counter; full valid max-uint64 history is unrepresentable under finite event cap, so unchanged valid checkpoint NOT_VERIFIED"
}

type updateResult struct {
	c   dp.Commit
	err error
}

func envelopeChange(s *session) dp.ChangeSet {
	story := dp.CloneElement(s.seed[1])
	story.Notes = "changed story"
	task := dp.CloneElement(s.seed[2])
	task.Notes = "changed task"
	task.ResourceRefs = []dp.ResourceRef{ref(abcDigest), ref(emptyDigest)}
	cs := dp.ChangeSet{Upserts: []dp.Element{task, story}, Provenance: []dp.ProvenanceFile{provenance("envelope", "whole")}, PutResources: []dp.ResourceInput{input("abc"), input("")}}
	if s.resourceReplacement {
		cs.DeleteResources = []string{info([]byte("old")).SHA256}
	}
	return cs
}
func expectedEnvelope(t *testing.T, s *session, old observation) (dp.Snapshot, []dp.Event) {
	want := dp.CloneSnapshot(old.snapshot)
	want.Revision.Counter++
	want.Elements[1].Notes = "changed story"
	want.Elements[2].Notes = "changed task"
	want.Elements[2].ResourceRefs = []dp.ResourceRef{ref(abcDigest), ref(emptyDigest)}
	want.Provenance = []dp.ProvenanceFile{provenance("envelope", "whole")}
	want.Resources = []dp.ResourceInfo{{SHA256: abcDigest, Size: 3}, {SHA256: emptyDigest}}
	changedResources := []string{abcDigest, emptyDigest}
	if s.resourceReplacement {
		changedResources = append(changedResources, info([]byte("old")).SHA256)
		sort.Strings(changedResources)
	}
	events := append(dp.CloneEvents(old.events), dp.Event{FormatVersion: 1, Scope: s.scope(), Revision: want.Revision, Actor: s.actor, ChangedIDs: []string{"STORY-1", "TASK-1"}, ChangedResourceIDs: changedResources})
	return want, events
}
func startEnvelope(s *session) <-chan updateResult {
	done := make(chan updateResult, 1)
	go func() {
		c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { return envelopeChange(s), nil })
		done <- updateResult{c, err}
	}()
	return done
}
func awaitUpdate(t *testing.T, s *session, done <-chan updateResult) updateResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-s.ctx.Done():
		t.Fatal("faulted update exceeded test bound")
		return updateResult{}
	}
}
func trigger(t *testing.T, s *session, ticket FaultTicket, p FaultPoint, mode FaultMode) {
	t.Helper()
	r, err := ticket.Trigger(s.ctx)
	must(t, err)
	if r.PointID != p.ID || r.Mode != mode || r.ReachedAt.IsZero() || r.TriggeredAt.Before(r.ReachedAt) {
		t.Fatal("fault receipt must bind actual point, mode and ordered clocks")
	}
	if mode == Kill && (r.ProcessExit == nil || *r.ProcessExit == 0) {
		t.Fatal("kill receipt lacks actual nonzero process exit")
	}
	if mode != Pause && !r.EvidenceRetained {
		t.Fatal("fault evidence was not retained")
	}
	for current := s; current != nil; current = current.parent {
		current.receipts = append(current.receipts, r)
	}
}
func arm(t *testing.T, s *session, p FaultPoint, mode FaultMode) FaultTicket {
	t.Helper()
	ticket, err := s.faults.Arm(s.ctx, s.root, p, mode)
	must(t, err)
	if ticket == nil {
		t.Fatal("nil fault ticket")
	}
	t.Cleanup(func() { must(t, ticket.Close()) })
	return ticket
}
func concurrentFaultCase(t *testing.T, s *session) string {
	points, gap := mappedPoints(t, s, BeforeDecision)
	if gap != "" {
		return gap
	}
	// All mapped physical phases, including post-decision publication, are
	// observed; the old/new oracle is identical for both adapters.
	points = s.faults.Points()
	for _, p := range points {
		if p.Stage != BeforeDecision && p.Stage != AfterDecision {
			continue
		}
		t.Run(p.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			want, events := expectedEnvelope(t, local, old)
			ticket := arm(t, local, p, Pause)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			started := time.Now().UTC()
			ctx, cancel := context.WithTimeout(local.ctx, dp.ReadTimeout)
			defer cancel()
			v, err := local.p.View(ctx, local.scope())
			if err != nil {
				equal(t, v, dp.Snapshot{})
				if local.name != "filesystem" {
					t.Fatal(err)
				}
				code(t, err, dp.Unavailable)
				lockElapsed(t, started)
				t.Logf("FS active-write unavailable window start=%s elapsed=%s", started.Format(time.RFC3339Nano), time.Since(started))
			} else {
				if p.Stage == BeforeDecision && !dp.EqualCanonicalSnapshot(v, old.snapshot) {
					t.Fatal("uncommitted View visible before durable decision")
				}
				if !dp.EqualCanonicalSnapshot(v, old.snapshot) && !dp.EqualCanonicalSnapshot(v, want) {
					t.Fatal("torn View during write")
				}
			}
			checkpointStarted := time.Now()
			cp, err := local.p.Checkpoint(ctx, local.scope())
			if err != nil {
				if cp != nil {
					_ = cp.Close()
					t.Fatal("handle on refusal")
				}
				if local.name != "filesystem" {
					t.Fatal(err)
				}
				code(t, err, dp.Unavailable)
				lockElapsed(t, checkpointStarted)
			} else {
				if cp == nil {
					t.Fatal("missing successful checkpoint")
				}
				t.Cleanup(func() { must(t, cp.Close()) })
				snap := cp.Snapshot()
				if p.Stage == BeforeDecision && !dp.EqualCanonicalSnapshot(snap, old.snapshot) {
					_ = cp.Close()
					t.Fatal("uncommitted Checkpoint visible before durable decision")
				}
				ev := cp.Events()
				if dp.EqualCanonicalSnapshot(snap, old.snapshot) {
					equal(t, ev, old.events)
				} else {
					equal(t, snap, want)
					equal(t, ev, events)
				}
				_, err = dp.EncodeCheckpoint(ctx, io.Discard, cp)
				must(t, err)
			}
			resourceStarted := time.Now()
			rr, err := local.p.ReadResource(ctx, local.scope(), abcDigest)
			if err == nil {
				equal(t, readAll(t, rr.Reader), []byte("abc"))
				if p.Stage == BeforeDecision {
					t.Fatal("uncommitted resource visible")
				}
			} else {
				// Close unexpected ownership before any assertion can terminate
				// the subtest; provider shutdown may wait for this lease.
				if rr.Reader != nil {
					_ = rr.Reader.Close()
				}
				equal(t, rr, dp.ResourceReader{})
				c, ok := dp.CodeOf(err)
				if !ok || c != dp.NotFound && (local.name != "filesystem" || c != dp.Unavailable) {
					t.Fatal(err)
				}
				if c == dp.Unavailable {
					lockElapsed(t, resourceStarted)
				}
			}
			trigger(t, local, ticket, p, Pause)
			r := awaitUpdate(t, local, done)
			must(t, r.err)
			now := local.observe(local.scope())
			equal(t, now.snapshot, want)
			equal(t, now.events, events)
			equal(t, local.read(local.scope(), abcDigest), []byte("abc"))
		})
	}
	return ""
}
func lockElapsed(t *testing.T, started time.Time) {
	t.Helper()
	elapsed := time.Since(started)
	t.Logf("actual active-write lock refusal elapsed=%s common_cap=%s", elapsed, dp.LockTimeout)
	if elapsed > dp.LockTimeout+25*time.Millisecond {
		t.Fatal("lock refusal exceeded common budget plus 25ms scheduling observation tolerance")
	}
}
func preDecisionCase(t *testing.T, s *session) string {
	points, gap := mappedPoints(t, s, BeforeDecision)
	if gap != "" {
		return gap
	}
	for _, p := range points {
		t.Run(p.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			ticket := arm(t, local, p, DefiniteFailure)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, p, DefiniteFailure)
			r := awaitUpdate(t, local, done)
			// The injected pre-decision storage failure has a definite old
			// outcome. Ambiguity belongs exclusively to C16.
			if _, ok := r.err.(*dp.Error); !ok {
				t.Fatalf("pre-decision failure requires a closed storage_error, got %T", r.err)
			}
			code(t, r.err, dp.StorageError)
			zeroCommit(t, r.c)
			local.unchanged(old)
			local.reopen()
			local.unchanged(old)
			local.reopen()
			local.unchanged(old)
		})
	}
	return ""
}
func postDecisionCase(t *testing.T, s *session) string {
	points, gap := mappedPoints(t, s, AfterDecision)
	if gap != "" {
		return gap
	}
	recoveries, rgap := mappedPoints(t, s, DuringRecovery)
	for _, p := range points {
		t.Run(p.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			want, events := expectedEnvelope(t, local, old)
			ticket := arm(t, local, p, Kill)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, p, Kill)
			r := awaitUpdate(t, local, done)
			if r.err == nil {
				t.Fatal("killed command claimed success")
			}
			zeroCommit(t, r.c)
			local.restart()
			now := local.observe(local.scope())
			equal(t, now.snapshot, want)
			equal(t, now.events, events)
			local.reopen()
			local.unchanged(now)
		})
	}
	if rgap != "" {
		return rgap
	}
	for _, p := range recoveries {
		t.Run("recovery-"+p.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			want, events := expectedEnvelope(t, local, old)
			decision := points[0]
			ticket := arm(t, local, decision, Kill)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, decision, Kill)
			r := awaitUpdate(t, local, done)
			if r.err == nil {
				t.Fatal("killed command acknowledged")
			}
			recovery := arm(t, local, p, Kill)
			finished := make(chan error, 1)
			go func() { _, _, err := local.instance.Restart(local.ctx); finished <- err }()
			must(t, recovery.WaitReached(local.ctx))
			trigger(t, local, recovery, p, Kill)
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("killed recovery claimed successful reopen")
				}
			case <-local.ctx.Done():
				t.Fatal("recovery kill did not return")
			}
			local.restart()
			now := local.observe(local.scope())
			equal(t, now.snapshot, want)
			equal(t, now.events, events)
			local.reopen()
			local.unchanged(now)
		})
	}
	return ""
}
func ambiguousCase(t *testing.T, s *session) string {
	points, gap := mappedPoints(t, s, AfterDecision)
	if gap != "" {
		return gap
	}
	for _, p := range points {
		t.Run(p.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			want, events := expectedEnvelope(t, local, old)
			ticket := arm(t, local, p, AmbiguousFailure)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, p, AmbiguousFailure)
			r := awaitUpdate(t, local, done)
			code(t, r.err, dp.CommitOutcomeUnknown)
			zeroCommit(t, r.c)
			v, err := local.p.View(local.ctx, local.scope())
			code(t, err, dp.Unavailable)
			equal(t, v, dp.Snapshot{})
			code(t, local.p.Ready(local.ctx), dp.Unavailable)
			calls := 0
			c, err := local.p.Update(local.ctx, local.scope(), local.rev(1), local.actor, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
			code(t, err, dp.Unavailable)
			zeroCommit(t, c)
			equal(t, calls, 0)
			local.restart()
			now := local.observe(local.scope())
			if dp.EqualCanonicalSnapshot(now.snapshot, old.snapshot) {
				equal(t, now.events, old.events)
			} else {
				equal(t, now.snapshot, want)
				equal(t, now.events, events)
			}
			local.reopen()
			local.unchanged(now)
		})
	}
	return "Recovery old/new checked; durable-decision justification for ambiguous fsync requires adapter evidence and independent review"
}
func historyCase(t *testing.T, s *session) string {
	t.Run("concurrent-contiguous-immutable-history", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		done := make(chan error, 1)
		go func() {
			for n := uint64(1); n <= 32; n++ {
				text := fmt.Sprintf("history-%02d", n)
				_, err := local.update(n, text, text)
				if err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		check := func() {
			cp, err := local.p.Checkpoint(local.ctx, local.scope())
			closeOnRefusal(err, cp)
			must(t, err)
			defer cp.Close()
			snap := cp.Snapshot()
			events := cp.Events()
			_, err = dp.EncodeCheckpoint(local.ctx, io.Discard, cp)
			must(t, err)
			equal(t, uint64(len(events)), snap.Revision.Counter)
			want := dp.CloneSnapshot(observationSeed(local))
			want.Revision = snap.Revision
			if snap.Revision.Counter > 1 {
				want.Elements[2].Notes = fmt.Sprintf("history-%02d", snap.Revision.Counter-1)
			}
			equal(t, snap, want)
			for i, event := range events {
				actor := local.actor
				ids := []string{"TASK-1"}
				if i == 0 {
					actor.RequestID = "seed"
					actor.Operation = "create"
					ids = []string{"EPIC-1", "STORY-1", "TASK-1"}
				} else {
					actor.RequestID = fmt.Sprintf("history-%02d", i)
				}
				equal(t, event, dp.Event{FormatVersion: 1, Scope: local.scope(), Revision: local.rev(uint64(i) + 1), Actor: actor, ChangedIDs: ids, ChangedResourceIDs: []string{}})
			}
		}
		for {
			select {
			case err := <-done:
				must(t, err)
				check()
				return
			case <-local.ctx.Done():
				t.Fatal("concurrent history writer exceeded bound")
			default:
				check()
			}
		}
	})
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	for _, corrupt := range []Corruption{HistoryGap, HistoryDuplicate, HistoryTail, HistoryScope, HistoryRevision, HistoryOrdering} {
		t.Run(string(corrupt), func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			_, err := local.update(1, "one", "changed")
			must(t, err)
			must(t, local.p.Close())
			must(t, local.faults.Corrupt(local.ctx, local.root, local.scope(), corrupt))
			p, pr, err := local.instance.Restart(local.ctx)
			if err != nil {
				code(t, err, dp.StorageError)
				return
			}
			local.p = p
			local.provision = pr
			cp, err := p.Checkpoint(local.ctx, local.scope())
			if cp != nil {
				_ = cp.Close()
			}
			code(t, err, dp.StorageError)
			if cp != nil {
				t.Fatal("corrupted stream returned a checkpoint")
			}
		})
	}
	return ""
}
func observationSeed(s *session) dp.Snapshot {
	return dp.Snapshot{Board: s.boards[0], Revision: s.rev(1), Elements: cloneSeed(s.seed), Provenance: []dp.ProvenanceFile{}, Resources: []dp.ResourceInfo{}}
}
func schemaCase(t *testing.T, s *session) string {
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	for _, corrupt := range []Corruption{UnknownSchema, MissingSchema, ScopeMetadata, RecoveryEvidence} {
		t.Run(string(corrupt), func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			must(t, local.p.Close())
			must(t, local.faults.Corrupt(local.ctx, local.root, local.scope(), corrupt))
			p, pr, err := local.instance.Restart(local.ctx)
			if err != nil {
				c, ok := dp.CodeOf(err)
				if !ok || c != dp.UnsupportedSchema && c != dp.StorageError && c != dp.Unavailable {
					t.Fatal(err)
				}
				return
			}
			local.p = p
			local.provision = pr
			if p.Ready(local.ctx) == nil {
				t.Fatal("corrupt schema/recovery reported ready")
			}
		})
	}
	return "Schema refusal checked; unchanged physical evidence retention needs adapter custody evidence"
}
