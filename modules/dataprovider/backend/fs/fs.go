//go:build linux

// Package fs implements a scoped, single-owner filesystem data provider.
// Storage is explicitly provisioned; no legacy tree or ambient path is opened.
package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type faultHook func(context.Context, string) error

type Provider struct {
	root            *os.Root
	owner           *os.File
	hook            faultHook
	mu              sync.Mutex
	closeMu         sync.Mutex
	locks           map[dp.Scope]chan struct{}
	active          int
	idle            chan struct{}
	closing, closed bool
	latched         map[dp.Scope]bool
	closeErr        error
}

var _ dp.Provider = (*Provider)(nil)
var _ dp.Provisioner = (*Provider)(nil)

func fail(c dp.Code) error { return dp.NewError(c) }

// Reject lossy text before handing values to encoding/json. Each field must be
// valid independently; concatenated partial UTF-8 sequences are not valid facts.
func textFields(s dp.Snapshot) error {
	valid := func(values ...string) bool {
		for _, value := range values {
			if !utf8.ValidString(value) {
				return false
			}
		}
		return true
	}
	if !valid(s.Board.Name, s.Board.Description) {
		return fail(dp.InvalidSnapshot)
	}
	for _, e := range s.Elements {
		if !valid(e.ID, e.Type, e.Name, e.ParentID, e.Title, e.Description, e.Scope, e.AcceptanceCriteria, e.Status, e.AssignedTo, e.Notes, e.Review, e.TaskClass) || !valid(e.BlockedBy...) || !valid(e.ManualBlockedBy...) {
			return fail(dp.InvalidSnapshot)
		}
		for _, item := range e.Checklist {
			if !valid(item.Text) {
				return fail(dp.InvalidSnapshot)
			}
		}
		for _, ref := range e.ResourceRefs {
			if !valid(ref.Name, ref.URI, ref.Description, ref.Type) {
				return fail(dp.InvalidSnapshot)
			}
		}
	}
	return nil
}
func storage(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return dp.ContextError(err)
	}
	if _, ok := dp.CodeOf(err); ok {
		return err
	}
	return fail(dp.StorageError)
}

// Open requires an existing canonical private directory. Only an empty root may
// acquire the initial schema; any nonempty unrecognized tree is refused intact.
// The OS owner lock is held until all admitted handles have drained at Close.
func Open(ctx context.Context, path string) (*Provider, error) { return open(ctx, path, nil) }
func open(ctx context.Context, path string, hook faultHook) (_ *Provider, result error) {
	ctx, cancel := dp.BoundedContext(ctx, dp.RecoveryTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, dp.ContextError(err)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fail(dp.InvalidRequest)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, fail(dp.StorageError)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fail(dp.StorageError)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fail(dp.StorageError)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, storage(err)
	}
	p := &Provider{root: root, hook: hook, locks: map[dp.Scope]chan struct{}{}, idle: make(chan struct{})}
	close(p.idle)
	defer func() {
		if result != nil {
			if p.owner != nil {
				_ = p.owner.Close()
			}
			_ = root.Close()
		}
	}()
	entries, err := p.entries(".")
	if err != nil {
		return nil, err
	}
	fresh := len(entries) == 0
	if !fresh {
		if err := p.schema(); err != nil {
			return nil, err
		}
	}
	fd, err := root.OpenFile("owner.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, storage(err)
	}
	p.owner = fd
	if err := privateRegular(fd, 0600); err != nil {
		return nil, err
	}
	lockctx, lockcancel := context.WithTimeout(ctx, dp.LockTimeout)
	defer lockcancel()
	for {
		err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, storage(err)
		}
		select {
		case <-lockctx.Done():
			return nil, dp.ContextError(lockctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if fresh {
		if err := p.write("schema.json", []byte(schemaBytes)); err != nil {
			return nil, err
		}
		for _, dir := range []string{"scopes", "transactions", "staging"} {
			if err := root.Mkdir(dir, 0700); err != nil {
				return nil, storage(err)
			}
		}
		if err := p.syncDir("."); err != nil {
			return nil, err
		}
	}
	if err := p.recover(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Provider) hit(ctx context.Context, id string) error {
	if p.hook != nil {
		if err := p.hook(ctx, id); err != nil {
			return storage(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return dp.ContextError(err)
	}
	return nil
}
func (p *Provider) latch(s dp.Scope) {
	p.mu.Lock()
	if p.latched == nil {
		p.latched = map[dp.Scope]bool{}
	}
	p.latched[s] = true
	p.mu.Unlock()
}
func (p *Provider) begin(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, dp.ContextError(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing || p.closed {
		return nil, fail(dp.Unavailable)
	}
	if p.active == 0 {
		p.idle = make(chan struct{})
	}
	p.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.active--
			if p.active == 0 {
				close(p.idle)
			}
		})
	}, nil
}
func (p *Provider) lock(ctx context.Context, s dp.Scope) (func(), error) {
	p.mu.Lock()
	ch := p.locks[s]
	if ch == nil {
		ch = make(chan struct{}, 1)
		p.locks[s] = ch
	}
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, dp.LockTimeout)
	defer cancel()
	select {
	case ch <- struct{}{}:
		p.mu.Lock()
		blocked := p.latched[s]
		p.mu.Unlock()
		if blocked {
			<-ch
			return nil, fail(dp.Unavailable)
		}
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, dp.ContextError(ctx.Err())
	}
}
func (p *Provider) enter(ctx context.Context, s dp.Scope) (func(), error) {
	if err := dp.ValidateScope(s); err != nil {
		return nil, err
	}
	end, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	unlock, err := p.lock(ctx, s)
	if err != nil {
		end()
		return nil, err
	}
	p.mu.Lock()
	latched := p.latched[s]
	p.mu.Unlock()
	if latched {
		unlock()
		end()
		return nil, fail(dp.Unavailable)
	}
	return func() { unlock(); end() }, nil
}
func (p *Provider) Ready(ctx context.Context) error {
	end, err := p.begin(ctx)
	if err != nil {
		return err
	}
	end()
	p.mu.Lock()
	blocked := len(p.latched) != 0
	p.mu.Unlock()
	if blocked {
		return fail(dp.Unavailable)
	}
	return nil
}
func (p *Provider) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	p.mu.Lock()
	if p.closed {
		err := p.closeErr
		p.mu.Unlock()
		return err
	}
	p.closing = true
	idle := p.idle
	p.mu.Unlock()
	timer := time.NewTimer(dp.CloseTimeout)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
		return fail(dp.Unavailable)
	}
	err := p.hit(context.Background(), "provider_close")
	if ce := p.owner.Close(); err == nil {
		err = storage(ce)
	}
	if ce := p.root.Close(); err == nil {
		err = storage(ce)
	}
	p.mu.Lock()
	p.closed = true
	p.closeErr = err
	p.mu.Unlock()
	return err
}

func (p *Provider) View(parent context.Context, s dp.Scope) (dp.Snapshot, error) {
	ctx, cancel := dp.BoundedContext(parent, dp.ReadTimeout)
	defer cancel()
	end, err := p.enter(ctx, s)
	if err != nil {
		return dp.Snapshot{}, err
	}
	defer end()
	state, err := p.load(ctx, s, true)
	if err != nil {
		return dp.Snapshot{}, err
	}
	return dp.CloneSnapshot(state.Snapshot), nil
}

func (p *Provider) EnsureEmpty(parent context.Context, b dp.Board) error {
	ctx, cancel := dp.BoundedContext(parent, dp.UpdateTimeout)
	defer cancel()
	end, err := p.enter(ctx, b.Scope)
	if err != nil {
		return err
	}
	defer end()
	state, err := p.load(ctx, b.Scope, true)
	if err == nil {
		if state.Snapshot.Board != b {
			return fail(dp.ScopeConflict)
		}
		return nil
	}
	if c, ok := dp.CodeOf(err); !ok || c != dp.NotFound {
		return err
	}
	snap := dp.Snapshot{Board: b, Revision: dp.Revision{Scope: b.Scope}, Elements: []dp.Element{}, Provenance: []dp.ProvenanceFile{}, Resources: []dp.ResourceInfo{}}
	if err := textFields(snap); err != nil {
		return err
	}
	if err := dp.ValidateSnapshot(snap); err != nil {
		return err
	}
	return p.provision(ctx, snap)
}
