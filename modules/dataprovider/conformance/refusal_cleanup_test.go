package conformance

import (
	"context"
	"errors"
	"fmt"
	dp "github.com/relux-works/swarma-board/dataprovider"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type refusedLease struct {
	once    sync.Once
	closes  atomic.Int32
	release func()
}

func (r *refusedLease) Read([]byte) (int, error) { return 0, io.EOF }
func (r *refusedLease) Close() error             { r.once.Do(func() { r.closes.Add(1); r.release() }); return nil }

type refusedCheckpoint struct{ lease *refusedLease }

func (r *refusedCheckpoint) Snapshot() dp.Snapshot { return dp.Snapshot{} }
func (r *refusedCheckpoint) Events() []dp.Event    { return nil }
func (r *refusedCheckpoint) OpenResource(string) (io.ReadCloser, error) {
	return nil, dp.NewError(dp.Unavailable)
}
func (r *refusedCheckpoint) Close() error { return r.lease.Close() }

type refusalProvider struct {
	*controlProvider
	mode      string
	wrong     bool
	lease     *refusedLease
	triggered bool
}

func (p *refusalProvider) failure(want dp.Code) error {
	if p.wrong {
		return dp.NewError(dp.StorageError)
	}
	return dp.NewError(want)
}
func (p *refusalProvider) newLease() *refusedLease {
	p.leases.Add(1)
	p.lease = &refusedLease{release: p.leases.Done}
	p.triggered = true
	return p.lease
}
func (p *refusalProvider) ReadResource(ctx context.Context, s dp.Scope, d string) (dp.ResourceReader, error) {
	if p.mode == "scope-reader" && dp.ValidateScope(s) != nil {
		return dp.ResourceReader{Reader: p.newLease()}, p.failure(dp.InvalidRequest)
	}
	r, err := p.controlProvider.ReadResource(ctx, s, d)
	if err != nil && p.mode == "isolation-reader" {
		return dp.ResourceReader{Reader: p.newLease()}, p.failure(dp.NotFound)
	}
	if err != nil && p.mode == "removed-reader" && s.BoardID == "work" {
		return dp.ResourceReader{Reader: p.newLease()}, p.failure(dp.NotFound)
	}
	return r, err
}
func (p *refusalProvider) Checkpoint(ctx context.Context, s dp.Scope) (dp.Checkpoint, error) {
	if p.mode == "scope-checkpoint" && dp.ValidateScope(s) != nil {
		return &refusedCheckpoint{lease: p.newLease()}, p.failure(dp.InvalidRequest)
	}
	if p.mode == "unknown-checkpoint" && s.ProductID == "unknown" {
		return &refusedCheckpoint{lease: p.newLease()}, p.failure(dp.NotFound)
	}
	return p.controlProvider.Checkpoint(ctx, s)
}
func (p *refusalProvider) Close() error {
	if p.triggered {
		fmt.Printf("refused lease close count before provider shutdown: %d\n", p.lease.closes.Load())
		if p.lease.closes.Load() == 1 {
			fmt.Println("refusal cleanup verified before shutdown")
		}
	}
	// No emergency release: a leaked lease really blocks this drain. The parent
	// process bounds the intentionally rejecting child and records any timeout.
	return p.controlProvider.Close()
}

func TestRefusalOwnedHandlesCloseBeforeFatalAssertions(t *testing.T) {
	for _, mode := range []string{"scope-reader", "scope-checkpoint", "unknown-checkpoint", "isolation-reader", "removed-reader"} {
		for _, wrong := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrong-code-%t", mode, wrong), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				exe, err := os.Executable()
				must(t, err)
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRefusalCleanupChild$", "-test.v")
				cmd.Env = append(os.Environ(), "REFUSAL_MODE="+mode, fmt.Sprintf("REFUSAL_WRONG=%t", wrong))
				out, err := cmd.CombinedOutput()
				var status *exec.ExitError
				t.Logf("bound=3s child_exit=%v timed_out=%t\n%s", err, ctx.Err() != nil, out)
				if ctx.Err() != nil || !errors.As(err, &status) || status.ExitCode() != 1 || !strings.Contains(string(out), "refusal cleanup verified before shutdown") {
					t.Fatal("refusal did not reject and release its lease before shutdown")
				}
			})
		}
	}
}
func TestRefusalCleanupChild(t *testing.T) {
	mode := os.Getenv("REFUSAL_MODE")
	if mode == "" {
		return
	}
	f := controlFactory("")
	open := f.Open
	f.Open = func(ctx context.Context, root string) (*Instance, error) {
		i, e := open(ctx, root)
		if e != nil {
			return nil, e
		}
		i.Provider = &refusalProvider{controlProvider: i.Provider.(*controlProvider), mode: mode, wrong: os.Getenv("REFUSAL_WRONG") == "true"}
		return i, nil
	}
	s := newSession(t, f, nil)
	switch mode {
	case "unknown-checkpoint":
		unknownCase(t, s)
	case "isolation-reader", "removed-reader":
		resourceIsolationCase(t, s)
	default:
		scopeCase(t, s)
	}
	t.Fatal("malformed provider was accepted")
}

func TestRefusalCasesAcceptCorrectOwnership(t *testing.T) {
	for name, run := range map[string]providerCase{"unknown": unknownCase, "scope": scopeCase, "resource-isolation": resourceIsolationCase, "checkpoint": checkpointCase} {
		t.Run(name, func(t *testing.T) { s := newSession(t, controlFactory(""), nil); run(t, s) })
	}
}
