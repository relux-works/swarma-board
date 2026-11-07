package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// Run visits all fifty vectors, with fresh state per implemented case. It
// returns explicit NOT_VERIFIED results for incomplete actions and later shared
// integration/native qualification. It never t.Skip's a case or labels partial
// coverage VERIFIED. Behavioral assertion failures fail the calling Go test.
// A green Go test alone is not conformance: call RequireAll for a release gate.
func Run(t *testing.T, factory Factory, faults FaultDriver) Report {
	t.Helper()
	v, err := Vectors()
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Provider: factory.Name, SpecSHA256: SpecSHA256, VectorsSHA256: VectorsSHA256, Results: []Result{}}
	for _, c := range v.Cases {
		r := Result{ID: c.ID, Layer: c.Layer, Status: NotVerified, Faults: []FaultReceipt{}}
		if factory.Name != "filesystem" && factory.Name != "database" || factory.Open == nil {
			r.Detail = "No real adapter factory supplied"
			report.Results = append(report.Results, r)
			continue
		}
		if c.Layer != "provider" {
			r.Detail = "Requires later shared server/domain integration or native/workload qualification; frozen action and oracle retained"
			report.Results = append(report.Results, r)
			continue
		}
		ok := t.Run(c.ID, func(t *testing.T) {
			r.Started = time.Now().UTC()
			defer func() { r.Elapsed = time.Since(r.Started) }()
			fn := providerCases[c.ID]
			if fn == nil {
				r.Detail = "Provider scenario not yet implemented; no case pass claimed"
				return
			}
			s := newSession(t, factory, faults)
			r.Detail = fn(t, s)
			r.Faults = append(r.Faults, s.receipts...)
			for _, o := range s.observations {
				r.Observations = append(r.Observations, o)
			}
			sort.Slice(r.Observations, func(i, j int) bool {
				a, b := r.Observations[i], r.Observations[j]
				if a.Scope.ProductID != b.Scope.ProductID {
					return a.Scope.ProductID < b.Scope.ProductID
				}
				if a.Scope.BoardID != b.Scope.BoardID {
					return a.Scope.BoardID < b.Scope.BoardID
				}
				return a.Revision.Counter < b.Revision.Counter
			})
			if r.Detail == "" {
				r.Status = Verified
			} else {
				t.Log("NOT_VERIFIED:", r.Detail)
			}
		})
		if !ok {
			r.Status = Failed
			r.Detail = "Black-box behavioral assertion failed; see Go test output"
		}
		report.Results = append(report.Results, r)
	}
	return report
}

// RequireAll is the integrated release gate; missing factories/hooks/integration
// and unmeasured native/workload qualification cannot silently pass it.
func (r Report) RequireAll(t *testing.T) {
	t.Helper()
	for _, c := range r.Results {
		if c.Status != Verified {
			t.Errorf("%s %s: %s", c.ID, c.Status, c.Detail)
		}
	}
	if len(r.Results) != 50 {
		t.Error("incomplete fifty-case report")
	}
}

type session struct {
	t                   *testing.T
	ctx                 context.Context
	root                string
	name                string
	factory             Factory
	instance            *Instance
	p                   dp.Provider
	provision           dp.Provisioner
	faults              FaultDriver
	receipts            []FaultReceipt
	parent              *session
	observations        map[string]CanonicalObservation
	resourceReplacement bool
	boards              []dp.Board
	seed                []dp.Element
	actor               dp.Actor
}

func newSession(t *testing.T, f Factory, d FaultDriver) *session {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), dp.RecoveryTimeout+dp.CloseTimeout)
	t.Cleanup(cancel)
	i, err := f.Open(ctx, root)
	if err != nil {
		t.Fatal("factory open:", err)
	}
	if i == nil || i.Provider == nil || i.Provisioner == nil || i.Cleanup == nil {
		t.Fatal("factory must return provider, provisioner and cleanup")
	}
	s := &session{t: t, ctx: ctx, root: root, name: f.Name, factory: f, instance: i, p: i.Provider, provision: i.Provisioner, faults: d, observations: map[string]CanonicalObservation{}}
	t.Cleanup(func() {
		if s.p != nil {
			if err := s.p.Close(); err != nil {
				t.Errorf("final provider close: %v", err)
			}
		}
		if err := i.Cleanup(); err != nil {
			t.Errorf("factory cleanup: %v", err)
		}
	})
	v, err := Vectors()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range v.Fixture.Scopes {
		s.boards = append(s.boards, dp.Board{Scope: dp.Scope{ProductID: b.ProductID, BoardID: b.BoardID}, Name: b.Name})
	}
	s.seed = v.Fixture.Elements
	for j := range s.seed {
		e := &s.seed[j]
		e.BlockedBy = []string{}
		e.ManualBlockedBy = []string{}
		e.Checklist = []dp.ChecklistItem{}
		e.ResourceRefs = []dp.ResourceRef{}
	}
	s.actor = v.Fixture.Actor
	if err := s.p.Ready(ctx); err != nil {
		t.Fatal("new provider not ready:", err)
	}
	return s
}
func (s *session) scope() dp.Scope { return s.boards[0].Scope }
func (s *session) fresh(t *testing.T) *session {
	child := newSession(t, s.factory, s.faults)
	child.parent = s
	child.resourceReplacement = s.resourceReplacement
	return child
}
func (s *session) rev(n uint64) dp.Revision { return dp.Revision{Scope: s.scope(), Counter: n} }
func (s *session) setup() {
	s.t.Helper()
	for _, b := range s.boards {
		must(s.t, s.provision.EnsureEmpty(s.ctx, b))
		a := s.actor
		a.RequestID = "seed"
		a.Operation = "create"
		seed := cloneSeed(s.seed)
		resources := []dp.ResourceInfo{}
		changedResources := []string{}
		if s.resourceReplacement {
			seed[2].ResourceRefs = []dp.ResourceRef{ref(info([]byte("old")).SHA256)}
			resources = []dp.ResourceInfo{info([]byte("old"))}
			changedResources = []string{resources[0].SHA256}
		}
		c, err := s.p.Update(s.ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
			cs := dp.ChangeSet{Upserts: seed}
			if s.resourceReplacement {
				cs.PutResources = []dp.ResourceInput{input("old")}
			}
			return cs, nil
		})
		must(s.t, err)
		equal(s.t, c, dp.Commit{Revision: dp.Revision{Scope: b.Scope, Counter: 1}, ChangedIDs: []string{"EPIC-1", "STORY-1", "TASK-1"}, ChangedResourceIDs: changedResources})
		o := s.observe(b.Scope)
		equal(s.t, o.snapshot, dp.Snapshot{Board: b, Revision: dp.Revision{Scope: b.Scope, Counter: 1}, Elements: seed, Provenance: []dp.ProvenanceFile{}, Resources: resources})
		equal(s.t, o.events, []dp.Event{{FormatVersion: 1, Scope: b.Scope, Revision: dp.Revision{Scope: b.Scope, Counter: 1}, Actor: a, ChangedIDs: []string{"EPIC-1", "STORY-1", "TASK-1"}, ChangedResourceIDs: changedResources}})
	}
}
func cloneSeed(in []dp.Element) []dp.Element {
	out := make([]dp.Element, len(in))
	for i, e := range in {
		out[i] = dp.CloneElement(e)
	}
	return out
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func equal(t *testing.T, a, b any) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		t.Fatalf("observable values differ\ngot: %s\nwant: %s", x, y)
	}
}
func code(t *testing.T, err error, want dp.Code) {
	t.Helper()
	got, ok := dp.CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error code got %v, want %s", err, want)
	}
}
func zeroCommit(t *testing.T, c dp.Commit) { t.Helper(); equal(t, c, dp.Commit{}) }

type observation struct {
	snapshot dp.Snapshot
	events   []dp.Event
	digest   string
	bytes    int64
}

func (s *session) observe(scope dp.Scope) observation {
	s.t.Helper()
	cp, err := s.p.Checkpoint(s.ctx, scope)
	must(s.t, err)
	if cp == nil {
		s.t.Fatal("successful checkpoint is nil")
	}
	o := observation{snapshot: cp.Snapshot(), events: cp.Events()}
	h := sha256.New()
	o.bytes, err = dp.EncodeCheckpoint(s.ctx, h, cp)
	must(s.t, err)
	o.digest = hex.EncodeToString(h.Sum(nil))
	v, err := s.p.View(s.ctx, scope)
	must(s.t, err)
	equal(s.t, v, o.snapshot)
	key := fmt.Sprintf("%s/%s/%d", scope.ProductID, scope.BoardID, o.snapshot.Revision.Counter)
	value := CanonicalObservation{Scope: scope, Revision: o.snapshot.Revision, SHA256: o.digest, Bytes: o.bytes}
	if prev, ok := s.observations[key]; ok {
		equal(s.t, value, prev)
	}
	s.observations[key] = value
	return o
}
func (s *session) unchanged(old observation) {
	s.t.Helper()
	equal(s.t, s.observe(old.snapshot.Board.Scope), old)
}
func (s *session) update(expected uint64, request, notes string) (dp.Commit, error) {
	a := s.actor
	a.RequestID = request
	return s.p.Update(s.ctx, s.scope(), s.rev(expected), a, func(snap dp.Snapshot) (dp.ChangeSet, error) {
		e := dp.CloneElement(snap.Elements[2])
		e.Notes = notes
		return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
	})
}
func (s *session) expectNotes(old observation, request, notes string) observation {
	s.t.Helper()
	now := s.observe(s.scope())
	want := dp.CloneSnapshot(old.snapshot)
	want.Revision.Counter++
	want.Elements[2].Notes = notes
	equal(s.t, now.snapshot, want)
	a := s.actor
	a.RequestID = request
	ids := []string{"TASK-1"}
	if old.snapshot.Elements[2].Notes == notes {
		ids = []string{}
	}
	wantEvents := append(dp.CloneEvents(old.events), dp.Event{FormatVersion: 1, Scope: s.scope(), Revision: want.Revision, Actor: a, ChangedIDs: ids, ChangedResourceIDs: []string{}})
	equal(s.t, now.events, wantEvents)
	return now
}
func (s *session) restart() {
	s.t.Helper()
	if s.instance.Restart == nil {
		s.t.Fatal("restart function required")
	}
	p, pr, err := s.instance.Restart(s.ctx)
	must(s.t, err)
	if p == nil || pr == nil {
		s.t.Fatal("restart returned missing handles")
	}
	s.p = p
	s.provision = pr
	must(s.t, p.Ready(s.ctx))
}
func (s *session) reopen() { s.t.Helper(); must(s.t, s.p.Close()); s.restart() }
func (s *session) refuse(change dp.ChangeSet, want dp.Code) {
	s.t.Helper()
	old := s.observe(s.scope())
	c, err := s.p.Update(s.ctx, s.scope(), old.snapshot.Revision, s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { return change, nil })
	code(s.t, err, want)
	zeroCommit(s.t, c)
	s.unchanged(old)
}
func readAll(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	if r == nil {
		t.Fatal("missing successful reader")
	}
	data, err := io.ReadAll(r)
	ce := r.Close()
	must(t, err)
	must(t, ce)
	return data
}
func missingFaults(s *session) string {
	if s.faults == nil {
		return "Real fault/restart driver not supplied; fault subcases NOT_VERIFIED"
	}
	return ""
}
func caseID(n int) string { return fmt.Sprintf("C%02d", n) }
