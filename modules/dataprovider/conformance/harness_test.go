package conformance

// This is a test-only behavioral control, with no disk or durability claim.
// It exercises the oracle and deliberately broken variants. It is never a FS
// or DB factory result and must not be cited as backend parity or crash evidence.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type controlState struct {
	snapshot  dp.Snapshot
	events    []dp.Event
	resources map[string][]byte
}
type controlProvider struct {
	mu     sync.Mutex
	states map[dp.Scope]*controlState
	closed bool
	leases sync.WaitGroup
	broken string
}

func controlFactory(broken string) Factory {
	return Factory{Name: "harness-control", Open: func(context.Context, string) (*Instance, error) {
		p := &controlProvider{states: map[dp.Scope]*controlState{}, broken: broken}
		instance := &Instance{Provider: p, Provisioner: p, Cleanup: func() error { return nil }}
		instance.Restart = func(context.Context) (dp.Provider, dp.Provisioner, error) {
			p.mu.Lock()
			p.closed = false
			p.mu.Unlock()
			return p, p, nil
		}
		return instance, nil
	}}
}
func (p *controlProvider) Ready(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return dp.NewError(dp.Unavailable)
	}
	if ctx.Err() != nil {
		return dp.ContextError(ctx.Err())
	}
	return nil
}
func (p *controlProvider) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.leases.Wait()
	return nil
}
func (p *controlProvider) admit(ctx context.Context, scope dp.Scope) error {
	if err := dp.ValidateScope(scope); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return dp.ContextError(ctx.Err())
	}
	if p.closed {
		return dp.NewError(dp.Unavailable)
	}
	return nil
}
func (p *controlProvider) EnsureEmpty(ctx context.Context, b dp.Board) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admit(ctx, b.Scope); err != nil {
		return err
	}
	if old, ok := p.states[b.Scope]; ok {
		if old.snapshot.Board != b {
			return dp.NewError(dp.ScopeConflict)
		}
		return nil
	}
	p.states[b.Scope] = &controlState{snapshot: dp.Snapshot{Board: b, Revision: dp.Revision{Scope: b.Scope}, Elements: []dp.Element{}, Provenance: []dp.ProvenanceFile{}, Resources: []dp.ResourceInfo{}}, events: []dp.Event{}, resources: map[string][]byte{}}
	return nil
}
func (p *controlProvider) View(ctx context.Context, scope dp.Scope) (dp.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admit(ctx, scope); err != nil {
		return dp.Snapshot{}, err
	}
	s, ok := p.states[scope]
	if !ok {
		return dp.Snapshot{}, dp.NewError(dp.NotFound)
	}
	if p.broken == "view_alias" {
		return s.snapshot, nil
	}
	return dp.CloneSnapshot(s.snapshot), nil
}
func (p *controlProvider) Update(ctx context.Context, scope dp.Scope, expected dp.Revision, a dp.Actor, cb func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var zero dp.Commit
	if err := p.admit(ctx, scope); err != nil {
		return zero, err
	}
	if cb == nil {
		return zero, dp.NewError(dp.InvalidRequest)
	}
	if err := dp.ValidateActor(a); err != nil {
		return zero, err
	}
	if expected.Scope != scope {
		return zero, dp.NewError(dp.InvalidRequest)
	}
	old, ok := p.states[scope]
	if !ok {
		return zero, dp.NewError(dp.NotFound)
	}
	exp := expected
	if p.broken == "stale_cas" {
		exp = old.snapshot.Revision
	}
	next, err := dp.NextRevision(scope, old.snapshot.Revision, exp)
	if err != nil {
		return zero, err
	}
	snap := dp.CloneSnapshot(old.snapshot)
	c, err := cb(snap)
	if err != nil {
		if p.broken == "rollback_leak" {
			old.snapshot.Elements = snap.Elements
		}
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, dp.ContextError(ctx.Err())
	}
	if err := dp.ValidateChangeSet(c); err != nil {
		return zero, err
	}
	c = dp.CloneChangeSet(c)
	byID := map[string]dp.Element{}
	for _, e := range old.snapshot.Elements {
		byID[strings.ToLower(e.ID)] = dp.CloneElement(e)
	}
	changed := map[string]bool{}
	for _, e := range c.Upserts {
		k := strings.ToLower(e.ID)
		prev, exists := byID[k]
		if exists && prev.ID != e.ID {
			return zero, dp.NewError(dp.InvalidSnapshot)
		}
		if !exists || !reflect.DeepEqual(prev, e) {
			changed[e.ID] = true
		}
		byID[k] = dp.CloneElement(e)
	}
	for _, id := range c.Deletes {
		k := strings.ToLower(id)
		prev, exists := byID[k]
		if !exists {
			return zero, dp.NewError(dp.NotFound)
		}
		changed[prev.ID] = true
		delete(byID, k)
	}
	resources := map[string][]byte{}
	for d, b := range old.resources {
		resources[d] = bytes.Clone(b)
	}
	changedResources := map[string]bool{}
	for _, in := range c.PutResources {
		var buf bytes.Buffer
		if p.broken != "skip_inputs" {
			if _, err := dp.CopyResource(ctx, &buf, in); err != nil {
				return zero, err
			}
		}
		if old, exists := resources[in.Info.SHA256]; exists {
			if !bytes.Equal(old, buf.Bytes()) {
				return zero, dp.NewError(dp.InvalidSnapshot)
			}
		} else {
			resources[in.Info.SHA256] = bytes.Clone(buf.Bytes())
			changedResources[in.Info.SHA256] = true
		}
	}
	for _, d := range c.DeleteResources {
		if _, exists := resources[d]; !exists {
			return zero, dp.NewError(dp.NotFound)
		}
		delete(resources, d)
		changedResources[d] = true
	}
	result := dp.CloneSnapshot(old.snapshot)
	result.Revision = next
	result.Elements = []dp.Element{}
	for _, e := range byID {
		result.Elements = append(result.Elements, e)
	}
	sort.Slice(result.Elements, func(i, j int) bool { return result.Elements[i].ID < result.Elements[j].ID })
	if c.Provenance != nil {
		result.Provenance = c.Provenance
	}
	sort.Slice(result.Provenance, func(i, j int) bool { return result.Provenance[i].Path < result.Provenance[j].Path })
	result.Resources = []dp.ResourceInfo{}
	for d, b := range resources {
		result.Resources = append(result.Resources, dp.ResourceInfo{SHA256: d, Size: int64(len(b))})
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].SHA256 < result.Resources[j].SHA256 })
	if err := dp.ValidateSnapshot(result); err != nil {
		return zero, err
	}
	ids := []string{}
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	digests := []string{}
	for d := range changedResources {
		digests = append(digests, d)
	}
	sort.Strings(digests)
	events := append(dp.CloneEvents(old.events), dp.Event{FormatVersion: 1, Scope: scope, Revision: next, Actor: a, ChangedIDs: ids, ChangedResourceIDs: digests})
	if err := dp.ValidateEvents(result, events); err != nil {
		return zero, err
	}
	p.states[scope] = &controlState{snapshot: dp.CloneSnapshot(result), events: dp.CloneEvents(events), resources: resources}
	return dp.Commit{Revision: next, ChangedIDs: ids, ChangedResourceIDs: digests}, nil
}

type controlReader struct {
	mu     sync.Mutex
	r      *bytes.Reader
	ctx    context.Context
	closed bool
	finish func()
}

func (r *controlReader) Read(buf []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, dp.NewError(dp.Unavailable)
	}
	if r.ctx.Err() != nil {
		return 0, dp.ContextError(r.ctx.Err())
	}
	return r.r.Read(buf)
}
func (r *controlReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	finish := r.finish
	r.mu.Unlock()
	if finish != nil {
		finish()
	}
	return nil
}
func (p *controlProvider) ReadResource(ctx context.Context, scope dp.Scope, d string) (dp.ResourceReader, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var zero dp.ResourceReader
	if err := p.admit(ctx, scope); err != nil {
		return zero, err
	}
	if err := dp.ValidateDigest(d); err != nil {
		return zero, err
	}
	s, ok := p.states[scope]
	if !ok {
		return zero, dp.NewError(dp.NotFound)
	}
	raw, ok := s.resources[d]
	if !ok {
		return zero, dp.NewError(dp.NotFound)
	}
	ctx, cancel := dp.BoundedContext(ctx, dp.ReadTimeout)
	p.leases.Add(1)
	r := &controlReader{r: bytes.NewReader(bytes.Clone(raw)), ctx: ctx, finish: func() { cancel(); p.leases.Done() }}
	return dp.ResourceReader{Info: dp.ResourceInfo{SHA256: d, Size: int64(len(raw))}, Reader: r}, nil
}

type controlCheckpoint struct {
	mu        sync.Mutex
	snapshot  dp.Snapshot
	events    []dp.Event
	resources map[string][]byte
	ctx       context.Context
	closed    bool
	child     *controlReader
	finish    func()
}

func (c *controlCheckpoint) Snapshot() dp.Snapshot { return dp.CloneSnapshot(c.snapshot) }
func (c *controlCheckpoint) Events() []dp.Event    { return dp.CloneEvents(c.events) }
func (c *controlCheckpoint) OpenResource(d string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, dp.NewError(dp.Unavailable)
	}
	if c.ctx.Err() != nil {
		return nil, dp.ContextError(c.ctx.Err())
	}
	if c.child != nil {
		return nil, dp.NewError(dp.InvalidRequest)
	}
	raw, ok := c.resources[d]
	if !ok {
		return nil, dp.NewError(dp.NotFound)
	}
	r := &controlReader{r: bytes.NewReader(raw), ctx: c.ctx}
	r.finish = func() { c.mu.Lock(); c.child = nil; c.mu.Unlock() }
	c.child = r
	return r, nil
}
func (c *controlCheckpoint) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	child := c.child
	finish := c.finish
	c.mu.Unlock()
	if child != nil {
		_ = child.Close()
	}
	if finish != nil {
		finish()
	}
	return nil
}
func (p *controlProvider) Checkpoint(ctx context.Context, scope dp.Scope) (dp.Checkpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admit(ctx, scope); err != nil {
		return nil, err
	}
	s, ok := p.states[scope]
	if !ok {
		return nil, dp.NewError(dp.NotFound)
	}
	ctx, cancel := dp.BoundedContext(ctx, dp.CheckpointTimeout)
	p.leases.Add(1)
	resources := map[string][]byte{}
	for d, b := range s.resources {
		resources[d] = bytes.Clone(b)
	}
	return &controlCheckpoint{snapshot: dp.CloneSnapshot(s.snapshot), events: dp.CloneEvents(s.events), resources: resources, ctx: ctx, finish: func() { cancel(); p.leases.Done() }}, nil
}

func TestHarnessVerificationControlProvider(t *testing.T) {
	for _, id := range []string{"C01", "C02", "C03", "C04", "C05", "C06", "C07", "C08", "C09", "C10", "C11", "C17", "C18", "C33", "C34", "C35", "C36", "C40", "C41"} {
		t.Run(id, func(t *testing.T) {
			factory := controlFactory("")
			if id == "C41" {
				factory = commonCheckpointFactory()
			}
			s := newSession(t, factory, nil)
			gap := providerCases[id](t, s)
			if gap != "" {
				t.Log("Harness verification only; remaining contract gap:", gap)
			}
		})
	}
}
func TestHarnessRejectsBrokenProviders(t *testing.T) {
	for _, x := range []struct{ mode, id, needle string }{{"stale_cas", "C05", "error code"}, {"view_alias", "C08", "invalid_snapshot"}, {"rollback_leak", "C07", "invalid_snapshot"}, {"skip_inputs", "C33", "invalid_snapshot"}} {
		t.Run(x.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			exe, err := os.Executable()
			must(t, err)
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestBrokenProviderChild$", "-test.v")
			cmd.Env = append(os.Environ(), "CONFORMANCE_BROKEN_MODE="+x.mode, "CONFORMANCE_BROKEN_CASE="+x.id)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), x.needle) {
				t.Fatalf("broken provider was not rejected by intended assertion: exit=%v output=%s", err, output)
			}
			t.Logf("Harness verification rejected %s at %s (child exit 1)", x.mode, x.id)
		})
	}
}
func TestBrokenProviderChild(t *testing.T) {
	mode := os.Getenv("CONFORMANCE_BROKEN_MODE")
	if mode == "" {
		return
	}
	id := os.Getenv("CONFORMANCE_BROKEN_CASE")
	fn := providerCases[id]
	if fn == nil {
		t.Fatal("unknown child case")
	}
	s := newSession(t, controlFactory(mode), nil)
	fn(t, s)
}
func TestFrozenFiftyCasesAndUnexecutedReport(t *testing.T) {
	v, err := Vectors()
	must(t, err)
	equal(t, len(v.Cases), 50)
	r := Run(t, Factory{}, nil)
	equal(t, len(r.Results), 50)
	for _, c := range r.Results {
		equal(t, c.Status, NotVerified)
	}
	if r.AllVerified() {
		t.Fatal("missing backend reported conformance")
	}
}
func TestHarnessCombinedEnvelopeResourceReplacement(t *testing.T) {
	s := newSession(t, controlFactory(""), nil)
	s.resourceReplacement = true
	s.setup()
	old := s.observe(s.scope())
	want, events := expectedEnvelope(t, s, old)
	c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(dp.Snapshot) (dp.ChangeSet, error) { return envelopeChange(s), nil })
	must(t, err)
	equal(t, c.Revision, s.rev(2))
	now := s.observe(s.scope())
	equal(t, now.snapshot, want)
	equal(t, now.events, events)
	r, err := s.p.ReadResource(s.ctx, s.scope(), info([]byte("old")).SHA256)
	code(t, err, dp.NotFound)
	equal(t, r, dp.ResourceReader{})
	equal(t, s.read(s.scope(), abcDigest), []byte("abc"))
}
