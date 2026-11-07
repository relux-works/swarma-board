//go:build linux

package fs

import (
	"context"
	"sync"
	"time"
)

// streamAdmission retains the caller's outcome through backing release. Normal
// release stops scheduling work without canceling this admission: common's
// checkpoint encoder checks it after Release. Child-operation cancellation is
// owned by the common handle. Err also checks the original caller and deadline
// directly, so scheduler delay cannot extend the admitted lifetime.
type streamAdmission struct {
	context.Context
	deadline   time.Time
	done       chan struct{}
	mu         sync.Mutex
	err        error
	timer      *time.Timer
	stopParent func() bool
}

func admitStream(parent context.Context, cap time.Duration) (context.Context, func()) {
	deadline := time.Now().Add(cap)
	if earlier, ok := parent.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	c := &streamAdmission{Context: parent, deadline: deadline, done: make(chan struct{})}
	c.timer = time.AfterFunc(time.Until(deadline), func() { c.mark(context.DeadlineExceeded) })
	c.stopParent = context.AfterFunc(parent, func() { c.mark(parent.Err()) })
	_ = c.Err()
	return c, c.finish
}
func (c *streamAdmission) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *streamAdmission) Done() <-chan struct{}       { return c.done }
func (c *streamAdmission) mark(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}
func (c *streamAdmission) Err() error {
	if err := c.Context.Err(); err != nil {
		c.mark(err)
	} else if !time.Now().Before(c.deadline) {
		c.mark(context.DeadlineExceeded)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
func (c *streamAdmission) finish() {
	c.timer.Stop()
	c.stopParent()
	_ = c.Err()
}
