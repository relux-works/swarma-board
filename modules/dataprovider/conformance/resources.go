package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

const abcDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type trackedInput struct {
	data            []byte
	pos, closed     int
	eof             bool
	readAfterReturn bool
	returned        bool
	reads           int
	record          func(string)
	name            string
}

func (r *trackedInput) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 && r.record != nil {
		r.record(r.name + ":start")
	}
	if r.returned {
		r.readAfterReturn = true
		return 0, errors.New("input retained after Update")
	}
	if r.pos == len(r.data) {
		r.eof = true
		if r.record != nil {
			r.record(r.name + ":eof")
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
func (r *trackedInput) Close() error { r.closed++; return nil }
func info(data []byte) dp.ResourceInfo {
	h := sha256.Sum256(data)
	return dp.ResourceInfo{SHA256: hex.EncodeToString(h[:]), Size: int64(len(data))}
}
func input(data string) dp.ResourceInput {
	p := []byte(data)
	return dp.ResourceInput{Info: info(p), Reader: strings.NewReader(data)}
}
func ref(d string) dp.ResourceRef {
	return dp.ResourceRef{Name: d, URI: "urn:sha256:" + d, Type: "outcome"}
}
func (s *session) read(scope dp.Scope, d string) []byte {
	s.t.Helper()
	r, err := s.p.ReadResource(s.ctx, scope, d)
	closeOnRefusal(err, r.Reader)
	must(s.t, err)
	data := readAll(s.t, r.Reader)
	equal(s.t, r.Info, info(data))
	equal(s.t, r.Info.SHA256, d)
	return data
}
func (s *session) apply(cs dp.ChangeSet, request string) (dp.Commit, observation) {
	s.t.Helper()
	old := s.observe(s.scope())
	a := s.actor
	a.RequestID = request
	c, err := s.p.Update(s.ctx, s.scope(), old.snapshot.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) { return cs, nil })
	must(s.t, err)
	now := s.observe(s.scope())
	equal(s.t, c.Revision, dp.Revision{Scope: s.scope(), Counter: old.snapshot.Revision.Counter + 1})
	equal(s.t, now.events, append(dp.CloneEvents(old.events), dp.Event{FormatVersion: 1, Scope: s.scope(), Revision: c.Revision, Actor: a, ChangedIDs: c.ChangedIDs, ChangedResourceIDs: c.ChangedResourceIDs}))
	return c, now
}
func verifyOneShot(t *testing.T, s *session) observation {
	old := s.observe(s.scope())
	var orderMu sync.Mutex
	order := []string{}
	record := func(event string) { orderMu.Lock(); order = append(order, event); orderMu.Unlock() }
	empty := &trackedInput{data: []byte{}, name: "empty", record: record}
	abc := &trackedInput{data: []byte("abc"), name: "abc", record: record}
	e := dp.CloneElement(old.snapshot.Elements[len(old.snapshot.Elements)-1])
	e.ResourceRefs = []dp.ResourceRef{ref(emptyDigest), ref(abcDigest)}
	c, now := s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, PutResources: []dp.ResourceInput{{Info: dp.ResourceInfo{SHA256: emptyDigest}, Reader: empty}, {Info: dp.ResourceInfo{SHA256: abcDigest, Size: 3}, Reader: abc}}}, "resource-one")
	equal(t, c.ChangedIDs, []string{e.ID})
	equal(t, c.ChangedResourceIDs, []string{abcDigest, emptyDigest})
	equal(t, order, []string{"empty:start", "empty:eof", "abc:start", "abc:eof"})
	for _, r := range []*trackedInput{empty, abc} {
		equal(t, r.closed, 0)
		if !r.eof {
			t.Fatal("input not consumed through EOF before Update returned")
		}
		r.returned = true
		must(t, r.Close())
	}
	abc.data[0] = 'X'
	equal(t, s.read(s.scope(), abcDigest), []byte("abc"))
	equal(t, s.read(s.scope(), emptyDigest), []byte{})
	want := dp.CloneSnapshot(old.snapshot)
	want.Revision = c.Revision
	want.Elements[len(want.Elements)-1] = e
	want.Resources = []dp.ResourceInfo{{SHA256: abcDigest, Size: 3}, {SHA256: emptyDigest}}
	equal(t, now.snapshot, want)
	if empty.readAfterReturn || abc.readAfterReturn {
		t.Fatal("retained caller reader")
	}
	return now
}
func streamCase(t *testing.T, s *session) string {
	s.setup()
	now := verifyOneShot(t, s)
	s.reopen()
	s.unchanged(now)
	c, identical := s.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc"), input("")}}, "identical")
	equal(t, c.ChangedIDs, []string{})
	equal(t, c.ChangedResourceIDs, []string{})
	want := dp.CloneSnapshot(now.snapshot)
	want.Revision = c.Revision
	equal(t, identical.snapshot, want)
	r, err := s.p.ReadResource(s.ctx, s.scope(), abcDigest)
	closeOnRefusal(err, r.Reader)
	must(t, err)
	defer r.Reader.Close()
	buf := make([]byte, 1)
	n, err := r.Reader.Read(buf)
	must(t, err)
	equal(t, n, 1)
	equal(t, buf, []byte("a"))
	must(t, r.Reader.Close())
	must(t, r.Reader.Close())
	equal(t, s.read(s.scope(), abcDigest), []byte("abc"))
	return ""
}
func resourceRefusalCase(t *testing.T, s *session) string {
	s.setup()
	for _, d := range []string{"", "abc", strings.ToUpper(abcDigest), strings.Repeat("g", 64)} {
		s.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{{Info: dp.ResourceInfo{SHA256: d, Size: 3}, Reader: strings.NewReader("abc")}}}, dp.InvalidRequest)
	}
	for _, x := range []struct {
		ri   dp.ResourceInfo
		data string
	}{{dp.ResourceInfo{SHA256: abcDigest, Size: 2}, "abc"}, {dp.ResourceInfo{SHA256: abcDigest, Size: 4}, "abc"}, {dp.ResourceInfo{SHA256: emptyDigest, Size: 3}, "abc"}} {
		s.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{{Info: x.ri, Reader: strings.NewReader(x.data)}}}, dp.InvalidSnapshot)
	}
	s.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc"), input("abc")}}, dp.InvalidRequest)
	s.refuse(dp.ChangeSet{DeleteResources: []string{abcDigest, abcDigest}}, dp.InvalidRequest)
	s.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}, DeleteResources: []string{abcDigest}}, dp.InvalidRequest)
	s.refuse(dp.ChangeSet{DeleteResources: []string{abcDigest}}, dp.NotFound)
	e := dp.CloneElement(s.seed[2])
	e.ResourceRefs = []dp.ResourceRef{ref(abcDigest)}
	s.refuse(dp.ChangeSet{Upserts: []dp.Element{e}}, dp.InvalidSnapshot)
	// Reserved variants cannot fall through the ordinary external-URN allowlist.
	for _, uri := range []string{"URN:sha256:" + abcDigest, "urn:SHA256:" + abcDigest, "UrN:sHa256:" + abcDigest} {
		e.ResourceRefs[0].URI = uri
		s.refuse(dp.ChangeSet{Upserts: []dp.Element{e}}, dp.InvalidSnapshot)
	}
	for _, uri := range []string{"urn:sha256:", "urn:sha256:abc", "URN:SHA256:" + strings.ToUpper(abcDigest), "urn:sha256:" + strings.Repeat("g", 64), "urn:sha256", "urn:sha256:" + abcDigest + ":extra", "urn:sha256:" + abcDigest + "?q", "urn:sha256:" + abcDigest + "#f", "sha256:" + abcDigest} {
		e.ResourceRefs[0].URI = uri
		s.refuse(dp.ChangeSet{Upserts: []dp.Element{e}, PutResources: []dp.ResourceInput{input("abc")}}, dp.InvalidSnapshot)
	}
	for _, uri := range []string{"URN:sha256:" + abcDigest, "urn:SHA256:" + abcDigest, "UrN:sHa256:" + abcDigest} {
		e.ResourceRefs[0].URI = uri
		_, now := s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, PutResources: []dp.ResourceInput{input("abc")}}, "case-variant")
		equal(t, now.snapshot.Elements[2].ResourceRefs[0].URI, uri)
		equal(t, s.read(s.scope(), abcDigest), []byte("abc"))
	}
	s.refuse(dp.ChangeSet{DeleteResources: []string{abcDigest}}, dp.InvalidSnapshot)
	e.ResourceRefs = []dp.ResourceRef{}
	c, now := s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, DeleteResources: []string{abcDigest}}, "dereference")
	equal(t, c.ChangedResourceIDs, []string{abcDigest})
	equal(t, now.snapshot.Resources, []dp.ResourceInfo{})
	return ""
}
func resourceIsolationCase(t *testing.T, s *session) string {
	s.setup()
	aOld := s.observe(s.scope())
	otherOld := s.observe(s.boards[2].Scope)
	for _, b := range s.boards[:2] {
		_, err := s.p.Update(s.ctx, b.Scope, dp.Revision{Scope: b.Scope, Counter: 1}, s.actor, func(dp.Snapshot) (dp.ChangeSet, error) {
			return dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, nil
		})
		must(t, err)
	}
	bOld := s.observe(s.boards[1].Scope)
	r, err := s.p.ReadResource(s.ctx, s.boards[2].Scope, abcDigest)
	if r.Reader != nil {
		_ = r.Reader.Close()
	}
	code(t, err, dp.NotFound)
	equal(t, r, dp.ResourceReader{})
	r, err = s.p.ReadResource(s.ctx, s.scope(), abcDigest)
	closeOnRefusal(err, r.Reader)
	must(t, err)
	defer r.Reader.Close()
	cp, err := s.p.Checkpoint(s.ctx, s.scope())
	closeOnRefusal(err, cp)
	must(t, err)
	defer cp.Close()
	pinned := cp.Snapshot()
	_, now := s.apply(dp.ChangeSet{DeleteResources: []string{abcDigest}}, "remove")
	equal(t, readAll(t, r.Reader), []byte("abc"))
	equal(t, cp.Snapshot(), pinned)
	child, err := cp.OpenResource(abcDigest)
	closeOnRefusal(err, child)
	must(t, err)
	equal(t, readAll(t, child), []byte("abc"))
	must(t, cp.Close())
	r, err = s.p.ReadResource(s.ctx, s.scope(), abcDigest)
	if r.Reader != nil {
		_ = r.Reader.Close()
	}
	code(t, err, dp.NotFound)
	equal(t, r, dp.ResourceReader{})
	s.unchanged(bOld)
	s.unchanged(otherOld)
	want := dp.CloneSnapshot(aOld.snapshot)
	want.Revision = now.snapshot.Revision
	equal(t, now.snapshot, want)
	s.reopen()
	s.unchanged(now)
	s.unchanged(bOld)
	return ""
}

type errorInput struct{ returned bool }

func (r *errorInput) Read(p []byte) (int, error) {
	if !r.returned {
		r.returned = true
		copy(p, "a")
		return 1, nil
	}
	return 0, errors.New("synthetic input fault")
}
func checkpointCase(t *testing.T, s *session) string {
	s.setup()
	verifyOneShot(t, s)
	s.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{{Info: dp.ResourceInfo{SHA256: abcDigest, Size: 3}, Reader: &errorInput{}}}}, dp.StorageError)
	cp, err := s.p.Checkpoint(s.ctx, s.scope())
	closeOnRefusal(err, cp)
	must(t, err)
	defer cp.Close()
	before := cp.Snapshot()
	events := cp.Events()
	child, err := cp.OpenResource(abcDigest)
	closeOnRefusal(err, child)
	must(t, err)
	second, err := cp.OpenResource(emptyDigest)
	if second != nil {
		_ = second.Close()
	}
	code(t, err, dp.InvalidRequest)
	if second != nil {
		t.Fatal("second active child returned")
	}
	must(t, child.Close())
	must(t, child.Close())
	absent, err := cp.OpenResource(strings.Repeat("0", 64))
	if absent != nil {
		_ = absent.Close()
	}
	code(t, err, dp.NotFound)
	if absent != nil {
		t.Fatal("absent catalog member opened")
	}
	e := dp.CloneElement(before.Elements[2])
	e.ResourceRefs = []dp.ResourceRef{}
	s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, DeleteResources: []string{abcDigest, emptyDigest}}, "concurrent-delete")
	for _, d := range []string{abcDigest, emptyDigest, abcDigest} {
		r, err := cp.OpenResource(d)
		closeOnRefusal(err, r)
		must(t, err)
		expected := []byte("abc")
		if d == emptyDigest {
			expected = []byte{}
		}
		equal(t, readAll(t, r), expected)
	}
	equal(t, cp.Snapshot(), before)
	equal(t, cp.Events(), events)
	child, err = cp.OpenResource(abcDigest)
	closeOnRefusal(err, child)
	must(t, err)
	must(t, cp.Close())
	must(t, cp.Close())
	buf := make([]byte, 1)
	_, err = child.Read(buf)
	code(t, err, dp.Unavailable)
	must(t, child.Close())
	closed, err := cp.OpenResource(abcDigest)
	if closed != nil {
		_ = closed.Close()
	}
	code(t, err, dp.Unavailable)
	if closed != nil {
		t.Fatal("closed checkpoint opened resource")
	}
	equal(t, cp.Snapshot(), before)
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	combined := s.fresh(t)
	combined.resourceReplacement = true
	gaps := []string{}
	for _, scenario := range []struct {
		name string
		run  providerCase
	}{{"coherent-combined", concurrentFaultCase}, {"pre-decision-combined", preDecisionCase}, {"post-decision-combined", postDecisionCase}, {"ambiguous-combined", ambiguousCase}} {
		t.Run(scenario.name, func(t *testing.T) {
			if gap := scenario.run(t, combined); gap != "" {
				gaps = append(gaps, gap)
			}
		})
	}
	return strings.Join(gaps, "; ")
}
func resourceChangesCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	c, resourceOnly := s.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "resource-only")
	equal(t, c.ChangedIDs, []string{})
	equal(t, c.ChangedResourceIDs, []string{abcDigest})
	want := dp.CloneSnapshot(old.snapshot)
	want.Revision = c.Revision
	want.Resources = []dp.ResourceInfo{{SHA256: abcDigest, Size: 3}}
	equal(t, resourceOnly.snapshot, want)
	e := dp.CloneElement(s.seed[2])
	e.Notes = "changed"
	e.ResourceRefs = []dp.ResourceRef{{Name: "external", URI: "urn:example:inert", Type: "precondition"}, ref(abcDigest)}
	c, mixed := s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, PutResources: []dp.ResourceInput{input("")}}, "mixed")
	equal(t, c.ChangedIDs, []string{"TASK-1"})
	equal(t, c.ChangedResourceIDs, []string{emptyDigest})
	equal(t, mixed.snapshot.Elements[2], e)
	c, idem := s.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "idempotent")
	equal(t, c.ChangedIDs, []string{})
	equal(t, c.ChangedResourceIDs, []string{})
	equal(t, s.read(s.scope(), abcDigest), []byte("abc"))
	equal(t, s.read(s.scope(), emptyDigest), []byte{})
	for _, event := range idem.events {
		encoded, err := json.Marshal(event)
		must(t, err)
		if bytes.Contains(encoded, []byte(`"data"`)) {
			t.Fatal("resource bytes in event")
		}
	}
	return ""
}
