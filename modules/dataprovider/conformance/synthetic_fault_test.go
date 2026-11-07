package conformance

import (
	"context"
	"errors"
	"fmt"
	dp "github.com/relux-works/swarma-board/dataprovider"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// These are synthetic harness probes only. They implement no filesystem or
// database adapter, persistence, process kill, or native qualification.
type syntheticTicket struct {
	point     FaultPoint
	mode      FaultMode
	reached   chan struct{}
	release   chan struct{}
	once      sync.Once
	reachedAt time.Time
}

func (x *syntheticTicket) WaitReached(ctx context.Context) error {
	select {
	case <-x.reached:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (x *syntheticTicket) Trigger(context.Context) (FaultReceipt, error) {
	x.once.Do(func() { close(x.release) })
	return FaultReceipt{PointID: x.point.ID, Mode: x.mode, ReachedAt: x.reachedAt, TriggeredAt: time.Now().UTC(), EvidenceRetained: true}, nil
}
func (x *syntheticTicket) Close() error { x.once.Do(func() { close(x.release) }); return nil }

type syntheticFaultDriver struct {
	mu          sync.Mutex
	tickets     map[string]*syntheticTicket
	failure     string
	invalidData string
}

func (d *syntheticFaultDriver) Points() []FaultPoint {
	phases := []string{"journal_sync", "first_write", "last_write", "rename", "resource_payload", "resource_membership", "revision_event", "parent_sync", "cleanup"}
	out := []FaultPoint{}
	for i, phase := range phases {
		out = append(out, FaultPoint{ID: fmt.Sprintf("synthetic-%d", i), Stage: BeforeDecision, Physical: "SYNTHETIC TEST ONLY: in-memory pause, no physical storage", FSPhase: phase})
	}
	return out
}
func (d *syntheticFaultDriver) Arm(_ context.Context, root string, p FaultPoint, m FaultMode) (FaultTicket, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	x := &syntheticTicket{point: p, mode: m, reached: make(chan struct{}), release: make(chan struct{})}
	d.tickets[root] = x
	return x, nil
}
func (d *syntheticFaultDriver) Corrupt(context.Context, string, dp.Scope, Corruption) error {
	return fmt.Errorf("not implemented by synthetic control")
}
func (d *syntheticFaultDriver) InstallRevision(context.Context, string, dp.Scope, uint64) error {
	return fmt.Errorf("not implemented by synthetic control")
}
func (d *syntheticFaultDriver) ticket(root string) *syntheticTicket {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tickets[root]
}
func (d *syntheticFaultDriver) active(root string) bool {
	x := d.ticket(root)
	if x == nil {
		return false
	}
	select {
	case <-x.release:
		return false
	default:
		return true
	}
}

type syntheticFaultProvider struct {
	*controlProvider
	driver *syntheticFaultDriver
	root   string
}

func (p *syntheticFaultProvider) Update(ctx context.Context, scope dp.Scope, r dp.Revision, a dp.Actor, cb func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	if x := p.driver.ticket(p.root); x != nil && p.driver.active(p.root) {
		x.reachedAt = time.Now().UTC()
		close(x.reached)
		select {
		case <-x.release:
		case <-ctx.Done():
			return dp.Commit{}, dp.ContextError(ctx.Err())
		}
		if x.mode == DefiniteFailure {
			switch p.driver.failure {
			case "unknown":
				return dp.Commit{}, dp.NewError(dp.CommitOutcomeUnknown)
			case "untyped":
				return dp.Commit{}, errors.New("synthetic untyped failure")
			case "wrapped":
				return dp.Commit{}, fmt.Errorf("synthetic diagnostic: %w", dp.NewError(dp.StorageError))
			default:
				return dp.Commit{}, dp.NewError(dp.StorageError)
			}
		}
	}
	return p.controlProvider.Update(ctx, scope, r, a, cb)
}
func (p *syntheticFaultProvider) View(ctx context.Context, scope dp.Scope) (dp.Snapshot, error) {
	if p.driver.invalidData == "view" && p.driver.active(p.root) {
		return dp.Snapshot{Board: dp.Board{Scope: scope, Name: "MUST-NOT-RETURN-ON-ERROR"}, Revision: dp.Revision{Scope: scope, Counter: 123}}, dp.NewError(dp.Unavailable)
	}
	return p.controlProvider.View(ctx, scope)
}
func (p *syntheticFaultProvider) Checkpoint(ctx context.Context, scope dp.Scope) (dp.Checkpoint, error) {
	if p.driver.invalidData == "checkpoint" && p.driver.active(p.root) {
		cp, err := p.controlProvider.Checkpoint(ctx, scope)
		if err != nil {
			return nil, err
		}
		return cp, dp.NewError(dp.Unavailable)
	}
	return p.controlProvider.Checkpoint(ctx, scope)
}
func (p *syntheticFaultProvider) ReadResource(ctx context.Context, scope dp.Scope, digest string) (dp.ResourceReader, error) {
	if p.driver.active(p.root) {
		if p.driver.invalidData == "resource-info" {
			return dp.ResourceReader{Info: info([]byte("unexpected"))}, dp.NewError(dp.Unavailable)
		}
		if p.driver.invalidData == "resource-handle" {
			p.leases.Add(1)
			return dp.ResourceReader{Reader: &syntheticLeaseReader{ReadCloser: io.NopCloser(strings.NewReader("unexpected")), release: p.leases.Done}}, dp.NewError(dp.Unavailable)
		}
	}

	return p.controlProvider.ReadResource(ctx, scope, digest)
}
func syntheticFaultFactory(d *syntheticFaultDriver) Factory {
	return Factory{Name: "filesystem", Open: func(ctx context.Context, root string) (*Instance, error) {
		i, err := controlFactory("").Open(ctx, root)
		if err != nil {
			return nil, err
		}
		p := &syntheticFaultProvider{controlProvider: i.Provider.(*controlProvider), driver: d, root: root}
		restart := i.Restart
		i.Provider = p
		i.Restart = func(ctx context.Context) (dp.Provider, dp.Provisioner, error) {
			_, pr, err := restart(ctx)
			return p, pr, err
		}
		return i, nil
	}}
}

type syntheticSortingProvider struct {
	*controlProvider
	mode string
}

func (p *syntheticSortingProvider) Update(ctx context.Context, scope dp.Scope, r dp.Revision, a dp.Actor, cb func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	return p.controlProvider.Update(ctx, scope, r, a, func(s dp.Snapshot) (dp.ChangeSet, error) {
		cs, err := cb(s)
		for i := range cs.Upserts {
			e := &cs.Upserts[i]
			leaf := e.Type == "task" || e.Type == "bug"
			if p.mode == "sort-edges" || p.mode == "sort-leaf-blocked" && leaf || p.mode == "sort-container-blocked" && !leaf {
				sort.Strings(e.BlockedBy)
			}
			if p.mode == "sort-edges" || p.mode == "sort-leaf-manual" && leaf || p.mode == "sort-container-manual" && !leaf {
				sort.Strings(e.ManualBlockedBy)
			}
		}
		return cs, err
	})
}
func syntheticSortingFactory() Factory {
	return Factory{Name: "harness-control", Open: func(ctx context.Context, root string) (*Instance, error) {
		i, err := controlFactory("").Open(ctx, root)
		if err != nil {
			return nil, err
		}
		p := &syntheticSortingProvider{i.Provider.(*controlProvider), os.Getenv("CONFORMANCE_REJECTION")}
		restart := i.Restart
		i.Provider = p
		i.Restart = func(ctx context.Context) (dp.Provider, dp.Provisioner, error) {
			_, pr, err := restart(ctx)
			return p, pr, err
		}
		return i, nil
	}}
}

type syntheticLeaseReader struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *syntheticLeaseReader) Close() error {
	r.once.Do(func() { r.release(); fmt.Println("unexpected refused reader closed") })
	return r.ReadCloser.Close()
}

func TestHarnessRejectsFalseAcceptControls(t *testing.T) {
	for _, x := range []struct{ mode, needle string }{
		{"view", "observable values differ"},
		{"checkpoint", "handle on refusal"},
		{"resource-info", "observable values differ"},
		{"resource-handle", "unexpected refused reader closed"},
		{"unknown", "want storage_error"},
		{"untyped", "requires a closed storage_error"},
		{"wrapped", "requires a closed storage_error"},
		{"sort-edges", "observable values differ"},
		{"sort-leaf-blocked", "observable values differ"},
		{"sort-leaf-manual", "observable values differ"},
		{"sort-container-blocked", "observable values differ"},
		{"sort-container-manual", "observable values differ"},
	} {
		t.Run(x.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			exe, err := os.Executable()
			must(t, err)
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestFalseAcceptControlChild$", "-test.v")
			cmd.Env = append(os.Environ(), "CONFORMANCE_REJECTION="+x.mode)
			out, err := cmd.CombinedOutput()
			var status *exec.ExitError
			if !errors.As(err, &status) || status.ExitCode() != 1 || !strings.Contains(string(out), x.needle) {
				t.Fatalf("control %s was not rejected at its oracle (exit=%v): %s", x.mode, err, out)
			}
			t.Logf("synthetic %s rejected: child exit=1; bounded output follows\n%s", x.mode, out)
		})
	}
}
func TestFalseAcceptControlChild(t *testing.T) {
	mode := os.Getenv("CONFORMANCE_REJECTION")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "sort-") {
		s := newSession(t, syntheticSortingFactory(), nil)
		orderingCase(t, s)
		return
	}
	d := &syntheticFaultDriver{tickets: map[string]*syntheticTicket{}}
	if mode == "unknown" || mode == "untyped" || mode == "wrapped" {
		d.failure = mode
		s := newSession(t, syntheticFaultFactory(d), d)
		preDecisionCase(t, s)
		return
	}
	d.invalidData = mode
	s := newSession(t, syntheticFaultFactory(d), d)
	concurrentFaultCase(t, s)
}
func TestHarnessAcceptsDefiniteStorageFailureControl(t *testing.T) {
	d := &syntheticFaultDriver{tickets: map[string]*syntheticTicket{}}
	s := newSession(t, syntheticFaultFactory(d), d)
	equal(t, preDecisionCase(t, s), "")
	// Complete child observations must survive the enclosing case's report.
	observations := s.canonicalObservations()
	if len(observations) != 3 {
		t.Fatalf("child observation union got %d, want three seeded scopes", len(observations))
	}
}
