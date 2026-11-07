package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// Keep ordinary fixture CRUD and deliberate corruption controls separate from
// the C41 lifetime oracle, which must exercise the actual common owned helper.
type commonCheckpointProvider struct {
	*controlProvider
	acquisitions atomic.Int32
	releases     atomic.Int32
	opens        atomic.Int32
	closes       atomic.Int32
}

type countedCheckpointReader struct {
	io.Reader
	closes *atomic.Int32
}

func (r countedCheckpointReader) Close() error { r.closes.Add(1); return nil }

// Forward only the frozen public interface, without package-private hooks.
type controlPublicCheckpoint struct{ dp.Checkpoint }

func (p *commonCheckpointProvider) Checkpoint(ctx context.Context, scope dp.Scope) (dp.Checkpoint, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.admit(ctx, scope); err != nil {
		return nil, err
	}
	s, ok := p.states[scope]
	if !ok {
		return nil, dp.NewError(dp.NotFound)
	}
	resources := map[string][]byte{}
	for d, b := range s.resources {
		resources[d] = bytes.Clone(b)
	}
	p.leases.Add(1)
	p.acquisitions.Add(1)
	cp, err := dp.OpenCheckpoint(ctx, s.snapshot, s.events, dp.ResourceBacking{
		Open: func(d string) (io.ReadCloser, error) {
			raw, ok := resources[d]
			if !ok {
				return nil, dp.NewError(dp.StorageError)
			}
			p.opens.Add(1)
			return countedCheckpointReader{bytes.NewReader(raw), &p.closes}, nil
		},
		// Release must not cancel the admission supplied to the common helper.
		Release: func() error { p.releases.Add(1); p.leases.Done(); return nil },
	})
	if err != nil {
		return nil, err
	}
	return controlPublicCheckpoint{cp}, nil
}

func commonCheckpointFactory() Factory {
	f := controlFactory("")
	open := f.Open
	f.Open = func(ctx context.Context, root string) (*Instance, error) {
		i, err := open(ctx, root)
		if err == nil {
			i.Provider = &commonCheckpointProvider{controlProvider: i.Provider.(*controlProvider)}
		}
		return i, err
	}
	return f
}

func TestDeadlineOracleUsesCommonPublicCheckpoint(t *testing.T) {
	s := newSession(t, commonCheckpointFactory(), nil)
	deadlinesCase(t, s)
	p := s.p.(*commonCheckpointProvider)
	// Every acquired checkpoint releases once, and every opened backing handle
	// closes once, including verification and children closed by the checkpoint.
	if p.acquisitions.Load() == 0 || p.releases.Load() != p.acquisitions.Load() || p.closes.Load() != p.opens.Load() {
		t.Fatalf("ownership: acquisitions=%d releases=%d opens=%d closes=%d", p.acquisitions.Load(), p.releases.Load(), p.opens.Load(), p.closes.Load())
	}
	t.Logf("common public checkpoint acquisitions=%d releases=%d opens=%d closes=%d", p.acquisitions.Load(), p.releases.Load(), p.opens.Load(), p.closes.Load())
}

type alteredCloseCheckpoint struct {
	dp.Checkpoint
	mode string
}

func (c alteredCloseCheckpoint) Close() error {
	err := c.Checkpoint.Close()
	if errors.Is(err, context.Canceled) {
		if c.mode == "hidden" {
			return nil
		}
		return dp.ContextError(context.Canceled)
	}
	return err
}

type alteredCloseProvider struct {
	dp.Provider
	mode string
}

func (p alteredCloseProvider) Checkpoint(ctx context.Context, scope dp.Scope) (dp.Checkpoint, error) {
	cp, err := p.Provider.Checkpoint(ctx, scope)
	if err != nil {
		return cp, err
	}
	return alteredCloseCheckpoint{cp, p.mode}, nil
}

func TestDeadlineOracleRejectsHiddenOrUnstableCancellation(t *testing.T) {
	for _, mode := range []string{"hidden", "unstable"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			exe, err := os.Executable()
			must(t, err)
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAlteredCheckpointCloseChild$", "-test.v")
			cmd.Env = append(os.Environ(), "CONFORMANCE_CHECKPOINT_CLOSE_MODE="+mode)
			output, err := cmd.CombinedOutput()
			needle := "error code"
			if mode == "unstable" {
				needle = "checkpoint cancellation Close outcome is not stable"
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), needle) {
				t.Fatalf("oracle missed %s Close: exit=%v output=%s", mode, err, output)
			}
			t.Logf("rejected %s public cancellation outcome at intended assertion (child exit 1):\n%s", mode, output)
		})
	}
}

func TestAlteredCheckpointCloseChild(t *testing.T) {
	mode := os.Getenv("CONFORMANCE_CHECKPOINT_CLOSE_MODE")
	if mode == "" {
		return
	}
	f := commonCheckpointFactory()
	open := f.Open
	f.Open = func(ctx context.Context, root string) (*Instance, error) {
		i, err := open(ctx, root)
		if err == nil {
			i.Provider = alteredCloseProvider{i.Provider, mode}
		}
		return i, err
	}
	deadlinesCase(t, newSession(t, f, nil))
}
