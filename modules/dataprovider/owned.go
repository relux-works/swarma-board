package dataprovider

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ResourceBacking opens verified, immutable, fixed-version private backing.
// The adapter must pin or privately spool it before calling these constructors;
// Open must never resolve a digest through current live membership. It supports
// sequential reopens, finite/context-cooperative I/O and concurrent Close/Read
// for cancellation. Release frees all backing and the provider's in-flight lease.
// Constructors take ownership of Release even when acquisition fails.
type ResourceBacking struct {
	Open    func(string) (io.ReadCloser, error)
	Release func() error
}

func committedError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ContextError(err)
	}
	return NewError(StorageError)
}
func releaseBacking(backing ResourceBacking) error {
	if backing.Release == nil {
		return nil
	}
	return committedError(backing.Release())
}
func verifyBacking(ctx context.Context, info ResourceInfo, backing ResourceBacking) error {
	if err := ctx.Err(); err != nil {
		return ContextError(err)
	}
	if backing.Open == nil {
		return NewError(StorageError)
	}
	r, err := backing.Open(info.SHA256)
	if err != nil {
		if r != nil {
			_ = r.Close()
		}
		return committedError(err)
	}
	if r == nil {
		return NewError(StorageError)
	}
	_, err = CopyResource(ctx, io.Discard, ResourceInput{Info: info, Reader: r})
	ce := r.Close()
	if err != nil {
		return committedError(err)
	}
	if ce != nil {
		return committedError(ce)
	}
	return nil
}

// OpenResourceReader verifies all stored bytes BEFORE exposing a reader, with
// constant payload working memory, then opens that same pinned immutable backing.
// ctx MUST be the original read admission context, capped at ReadTimeout; a
// provider must not restart the budget after staging or return. Release owns its
// in-flight lease through successful Close or context cancellation/expiry.
func OpenResourceReader(ctx context.Context, info ResourceInfo, backing ResourceBacking) (result ResourceReader, err error) {
	ctx, budgetCancel := BoundedContext(ctx, ReadTimeout)
	owned := false
	defer func() {
		if !owned {
			budgetCancel()
			if e := releaseBacking(backing); err == nil && e != nil {
				err = e
			}
		}
	}()
	if backing.Release == nil {
		return ResourceReader{}, NewError(StorageError)
	}
	if err = ValidateResourceInfo(info); err != nil {
		return ResourceReader{}, committedError(err)
	}
	if err = verifyBacking(ctx, info, backing); err != nil {
		return ResourceReader{}, err
	}
	r, err := backing.Open(info.SHA256)
	if err != nil {
		if r != nil {
			_ = r.Close()
		}
		return ResourceReader{}, committedError(err)
	}
	if r == nil {
		return ResourceReader{}, NewError(StorageError)
	}
	if err = ctx.Err(); err != nil {
		_ = r.Close()
		return ResourceReader{}, ContextError(err)
	}
	reader := newOwnedReader(ctx, r, nil, func(error) error { budgetCancel(); return releaseBacking(backing) })
	owned = true
	return ResourceReader{Info: info, Reader: reader}, nil
}

// OpenCheckpoint verifies complete detached metadata/history and every stored
// payload before returning an owned fixed-revision handle. Backing is pinned by
// the adapter, not by a fresh View/ReadResource loop. ctx MUST retain the original
// Checkpoint admission budget, including all preparation and stream lifetime.
func OpenCheckpoint(ctx context.Context, snapshot Snapshot, events []Event, backing ResourceBacking) (result Checkpoint, err error) {
	ctx, budgetCancel := BoundedContext(ctx, CheckpointTimeout)
	owned := false
	defer func() {
		if !owned {
			budgetCancel()
			if e := releaseBacking(backing); err == nil && e != nil {
				err = e
			}
		}
	}()
	if backing.Release == nil {
		return nil, NewError(StorageError)
	}
	if err = ValidateSnapshot(snapshot); err != nil {
		return nil, committedError(err)
	}
	if err = ValidateEvents(snapshot, events); err != nil {
		return nil, committedError(err)
	}
	for _, info := range snapshot.Resources {
		if err = verifyBacking(ctx, info, backing); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, ContextError(err)
	}
	cp := &ownedCheckpoint{ctx: ctx, cancel: budgetCancel, snapshot: CloneSnapshot(snapshot), events: CloneEvents(events), backing: backing, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = cp.Close()
		case <-cp.done:
		}
	}()
	owned = true
	return cp, nil
}

type ownedReader struct {
	ctx      context.Context
	r        io.ReadCloser
	mu       sync.Mutex
	closed   bool
	closeErr error
	onError  func(error)
	onClose  func(error) error
	once     sync.Once
	done     chan struct{}
}

func newOwnedReader(ctx context.Context, r io.ReadCloser, onError func(error), onClose func(error) error) *ownedReader {
	x := &ownedReader{ctx: ctx, r: r, onError: onError, onClose: onClose, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = x.Close()
		case <-x.done:
		}
	}()
	return x
}
func (r *ownedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, ContextError(err)
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return 0, NewError(Unavailable)
	}
	n, err := r.r.Read(p)
	if ce := r.ctx.Err(); ce != nil {
		err = ContextError(ce)
	} else if err != nil && !errors.Is(err, io.EOF) {
		err = committedError(err)
	}
	if err != nil && !errors.Is(err, io.EOF) && r.onError != nil {
		r.onError(err)
	}
	return n, err
}
func (r *ownedReader) Close() error {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		err := committedError(r.r.Close())
		if r.onClose != nil {
			if e := r.onClose(err); err == nil {
				err = e
			}
		}
		r.mu.Lock()
		r.closeErr = err
		r.mu.Unlock()
		close(r.done)
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeErr
}

type ownedCheckpoint struct {
	ctx      context.Context
	cancel   context.CancelFunc
	snapshot Snapshot
	events   []Event
	backing  ResourceBacking
	mu       sync.Mutex
	closed   bool
	child    *ownedReader
	failure  error
	closeErr error
	once     sync.Once
	done     chan struct{}
}

func (c *ownedCheckpoint) Snapshot() Snapshot { return CloneSnapshot(c.snapshot) }
func (c *ownedCheckpoint) Events() []Event    { return CloneEvents(c.events) }
func (c *ownedCheckpoint) OpenResource(digest string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return nil, ContextError(err)
	}
	if c.closed {
		return nil, NewError(Unavailable)
	}
	if c.failure != nil {
		return nil, c.failure
	}
	if c.child != nil {
		return nil, NewError(InvalidRequest)
	}
	found := false
	for _, info := range c.snapshot.Resources {
		if info.SHA256 == digest {
			found = true
			break
		}
	}
	if !found {
		return nil, NewError(NotFound)
	}
	r, err := c.backing.Open(digest)
	if err != nil {
		if r != nil {
			_ = r.Close()
		}
		c.failure = committedError(err)
		return nil, c.failure
	}
	if r == nil {
		c.failure = NewError(StorageError)
		return nil, c.failure
	}
	childCtx, cancel := context.WithCancel(c.ctx)
	child := newOwnedReader(childCtx, r, func(err error) {
		c.mu.Lock()
		if c.failure == nil {
			c.failure = err
		}
		c.mu.Unlock()
	}, func(err error) error {
		cancel()
		c.mu.Lock()
		c.child = nil
		if err != nil && c.failure == nil {
			c.failure = err
		}
		c.mu.Unlock()
		return nil
	})
	c.child = child
	return child, nil
}
func (c *ownedCheckpoint) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		child := c.child
		c.mu.Unlock()
		c.cancel()
		var err error
		if child != nil {
			err = child.Close()
		}
		if e := releaseBacking(c.backing); err == nil {
			err = e
		}
		c.mu.Lock()
		if err == nil {
			err = c.failure
		}
		c.closeErr = err
		c.mu.Unlock()
		close(c.done)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}
