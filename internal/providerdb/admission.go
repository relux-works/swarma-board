package providerdb

import (
	"context"
	"io"
	"sync"
	"time"
)

// streamAdmission keeps the original caller and absolute budget observable
// through backing finalization. Releasing a stream stops its timer/subscription
// without manufacturing a cancellation; common owns cancellation of children.
type streamAdmission struct {
	context.Context
	deadline   time.Time
	done       chan struct{}
	mu         sync.Mutex
	err        error
	timer      *time.Timer
	stopParent func() bool
	finishOnce sync.Once
}

func admitStream(parent context.Context, cap time.Duration) (context.Context, func()) {
	deadline := time.Now().Add(cap)
	if earlier, ok := parent.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	c := &streamAdmission{Context: parent, deadline: deadline, done: make(chan struct{})}
	c.timer = time.AfterFunc(time.Until(deadline), func() { _ = c.Err() })
	c.stopParent = context.AfterFunc(parent, func() { _ = c.Err() })
	_ = c.Err()
	return c, c.finish
}

func (c *streamAdmission) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *streamAdmission) Done() <-chan struct{}       { return c.done }
func (c *streamAdmission) Err() error {
	// Consult the caller directly: propagation callbacks may still be queued.
	err := c.Context.Err()
	if err == nil && !time.Now().Before(c.deadline) {
		err = context.DeadlineExceeded
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil && err != nil {
		c.err = err
		close(c.done)
	}
	return c.err
}
func (c *streamAdmission) finish() {
	c.finishOnce.Do(func() {
		c.timer.Stop()
		c.stopParent()
		_ = c.Err()
	})
}

// A common owned reader has its own child context. Consult the original DB
// admission as well so an immediately preceding caller cancellation cannot be
// missed while propagation to that child is still queued.
type pinnedReader struct {
	io.ReadCloser
	ctx context.Context
}

func (r *pinnedReader) Read(p []byte) (int, error) {
	if err := checkContext(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.ReadCloser.Read(p)
	if e := checkContext(r.ctx); e != nil {
		return n, e
	}
	return n, err
}
