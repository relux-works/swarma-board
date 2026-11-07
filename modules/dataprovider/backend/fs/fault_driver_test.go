//go:build linux

package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

var physicalPoints = []cf.FaultPoint{
	{ID: "journal_sync", Stage: cf.BeforeDecision, FSPhase: "journal_sync", Physical: "durable intent file and transactions directory synced before generation creation"},
	{ID: "first_write", Stage: cf.BeforeDecision, FSPhase: "first_write", Physical: "new generation snapshot file synced, before resource staging"},
	{ID: "resource_payload", Stage: cf.BeforeDecision, FSPhase: "resource_payload", Physical: "all new and retained payload files and resource directory synced"},
	{ID: "resource_membership", Stage: cf.BeforeDecision, FSPhase: "resource_membership", Physical: "snapshot membership staged and canonical command stream accounted from private files"},
	{ID: "revision_event", Stage: cf.BeforeDecision, FSPhase: "revision_event", Physical: "complete contiguous events file synced in unpublished generation"},
	{ID: "last_write", Stage: cf.BeforeDecision, FSPhase: "last_write", Physical: "envelope file and complete unpublished generation directory synced"},
	{ID: "before_rename", Stage: cf.BeforeDecision, FSPhase: "rename", Physical: "next CURRENT file and parent directory synced immediately before atomic CURRENT rename"},
	{ID: "parent_sync", Stage: cf.AfterDecision, FSPhase: "parent_sync", Physical: "CURRENT renamed and scope parent fsync completed; acknowledgement not sent"},
	{ID: "cleanup", Stage: cf.AfterDecision, FSPhase: "cleanup", Physical: "durable CURRENT selected, before intent removal and old generation reclamation"},
	{ID: "recovery", Stage: cf.DuringRecovery, Physical: "all retained intents and committed generations validated, before recovery modifies evidence"},
	{ID: "resource_read", Stage: cf.DuringResourceRead, Physical: "read-only pinned inode section reader immediately before exposed Read"},
	{ID: "resource_close", Stage: cf.DuringResourceClose, Physical: "exposed pinned section reader close, before backing lease release"},
	{ID: "provider_close", Stage: cf.DuringProviderClose, Physical: "admission stopped and leases drained, before lifetime owner descriptor close"},
}

type driver struct {
	mu           sync.Mutex
	instances    map[string]*controlled
	tickets      map[string]*ticket
	foreignLocks []*os.File
}
type controlled struct {
	mu      sync.Mutex
	root    string
	d       *driver
	p       *Provider
	pending *ticket
}
type ticket struct {
	mu            sync.Mutex
	point         cf.FaultPoint
	mode          cf.FaultMode
	root, control string
	reached       chan struct{}
	released      chan struct{}
	reachedAt     time.Time
	triggeredAt   time.Time
	once          sync.Once
	reachOnce     sync.Once
	proc          *exec.Cmd
	stderr        *os.File
	stdin         io.WriteCloser
	exited        chan struct{}
	exit          int
	closed        bool
}

func newDriver() *driver {
	return &driver{instances: map[string]*controlled{}, tickets: map[string]*ticket{}}
}
func (d *driver) Points() []cf.FaultPoint { return append([]cf.FaultPoint{}, physicalPoints...) }
func (d *driver) factory() cf.Factory {
	return cf.Factory{Name: "filesystem", Open: func(ctx context.Context, root string) (*cf.Instance, error) {
		c := &controlled{root: root, d: d}
		p, err := open(ctx, root, d.hook(root))
		if err != nil {
			return nil, err
		}
		c.p = p
		d.mu.Lock()
		d.instances[root] = c
		d.mu.Unlock()
		return &cf.Instance{Provider: c, Provisioner: c, Restart: c.restart, OpenPeer: func(ctx context.Context) (dp.Provider, dp.Provisioner, func() error, error) {
			p, err := Open(ctx, root)
			if err != nil {
				return nil, nil, nil, err
			}
			return p, p, p.Close, nil
		}, Cleanup: func() error {
			d.mu.Lock()
			ticket := d.tickets[root]
			delete(d.instances, root)
			locks := d.foreignLocks
			d.foreignLocks = nil
			d.mu.Unlock()
			for _, f := range locks {
				_ = f.Close()
			}
			if ticket != nil {
				_ = ticket.Close()
			}
			c.mu.Lock()
			p := c.p
			c.mu.Unlock()
			if p != nil {
				_ = p.Close()
			}
			return nil
		}}, nil
	}}
}
func (d *driver) hook(root string) faultHook {
	return func(ctx context.Context, id string) error {
		d.mu.Lock()
		t := d.tickets[root]
		d.mu.Unlock()
		if t == nil || t.point.ID != id || t.mode == cf.Kill {
			return nil
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil
		}
		t.closed = true
		t.mu.Unlock()
		t.reachOnce.Do(func() { t.mu.Lock(); t.reachedAt = time.Now().UTC(); t.mu.Unlock(); close(t.reached) })
		// A test pause intentionally models uninterruptible physical I/O. The
		// ordinary operation context is checked immediately after its release.
		<-t.released
		switch t.mode {
		case cf.Pause:
			return nil
		case cf.AmbiguousFailure:
			return fail(dp.CommitOutcomeUnknown)
		case cf.DiskFull:
			return syscall.ENOSPC
		default:
			return fail(dp.StorageError)
		}
	}
}
func (d *driver) Arm(ctx context.Context, root string, point cf.FaultPoint, mode cf.FaultMode) (cf.FaultTicket, error) {
	known := false
	for _, p := range physicalPoints {
		if p == point {
			known = true
		}
	}
	if !known {
		return nil, fail(dp.InvalidRequest)
	}
	control, err := os.MkdirTemp("", "physical-fault-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(control, 0700); err != nil {
		return nil, err
	}
	t := &ticket{point: point, mode: mode, root: root, control: control, reached: make(chan struct{}), released: make(chan struct{}), exited: make(chan struct{})}
	d.mu.Lock()
	old := d.tickets[root]
	c := d.instances[root]
	d.tickets[root] = t
	d.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if mode == cf.Kill {
		if c == nil {
			return nil, fail(dp.InvalidRequest)
		}
		c.mu.Lock()
		if c.p != nil {
			if err := c.p.Close(); err != nil {
				c.mu.Unlock()
				return nil, err
			}
		}
		c.p = nil
		c.pending = t
		c.mu.Unlock()
	}
	return t, nil
}
func (t *ticket) WaitReached(ctx context.Context) error {
	if t.mode != cf.Kill {
		select {
		case <-t.reached:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		b, err := os.ReadFile(filepath.Join(t.control, "reached.json"))
		if err == nil {
			var at time.Time
			if json.Unmarshal(b, &at) != nil {
				return fail(dp.StorageError)
			}
			t.mu.Lock()
			t.reachedAt = at
			t.mu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.exited:
			return fail(dp.Unavailable)
		case <-ticker.C:
		}
	}
}
func (t *ticket) Trigger(ctx context.Context) (cf.FaultReceipt, error) {
	t.mu.Lock()
	t.triggeredAt = time.Now().UTC()
	r := cf.FaultReceipt{PointID: t.point.ID, Mode: t.mode, ReachedAt: t.reachedAt, TriggeredAt: t.triggeredAt, EvidenceRetained: t.mode != cf.Pause}
	cmd := t.proc
	t.mu.Unlock()
	if t.mode == cf.Kill {
		if cmd == nil || cmd.Process == nil {
			return r, fail(dp.Unavailable)
		}
		if err := cmd.Process.Kill(); err != nil {
			return r, err
		}
		select {
		case <-t.exited:
		case <-ctx.Done():
			return r, ctx.Err()
		}
		t.mu.Lock()
		exit := t.exit
		t.mu.Unlock()
		r.ProcessExit = &exit
	} else {
		t.once.Do(func() { close(t.released) })
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(t.control, "receipt.json"), b, 0600); err != nil {
		return r, err
	}
	return r, nil
}
func (t *ticket) Close() error {
	t.once.Do(func() { close(t.released) })
	t.mu.Lock()
	cmd := t.proc
	t.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		select {
		case <-t.exited:
		default:
			_ = cmd.Process.Kill()
			select {
			case <-t.exited:
			case <-time.After(2 * time.Second):
				return fail(dp.Unavailable)
			}
		}
	}
	return nil
}
func (c *controlled) current() (*Provider, *ticket) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.p, c.pending
}
func (c *controlled) View(ctx context.Context, s dp.Scope) (dp.Snapshot, error) {
	p, _ := c.current()
	if p == nil {
		return dp.Snapshot{}, fail(dp.Unavailable)
	}
	return p.View(ctx, s)
}
func (c *controlled) ReadResource(ctx context.Context, s dp.Scope, d string) (dp.ResourceReader, error) {
	p, _ := c.current()
	if p == nil {
		return dp.ResourceReader{}, fail(dp.Unavailable)
	}
	return p.ReadResource(ctx, s, d)
}
func (c *controlled) Checkpoint(ctx context.Context, s dp.Scope) (dp.Checkpoint, error) {
	p, _ := c.current()
	if p == nil {
		return nil, fail(dp.Unavailable)
	}
	return p.Checkpoint(ctx, s)
}
func (c *controlled) Ready(ctx context.Context) error {
	p, _ := c.current()
	if p == nil {
		return fail(dp.Unavailable)
	}
	return p.Ready(ctx)
}
func (c *controlled) EnsureEmpty(ctx context.Context, b dp.Board) error {
	p, _ := c.current()
	if p == nil {
		return fail(dp.Unavailable)
	}
	return p.EnsureEmpty(ctx, b)
}
func (c *controlled) Close() error {
	p, t := c.current()
	if p != nil {
		return p.Close()
	}
	if t != nil {
		return t.Close()
	}
	return nil
}
func (c *controlled) Update(ctx context.Context, s dp.Scope, r dp.Revision, a dp.Actor, cb func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	p, t := c.current()
	if p != nil {
		return p.Update(ctx, s, r, a, cb)
	}
	if t == nil {
		return dp.Commit{}, fail(dp.Unavailable)
	}
	return c.remoteUpdate(ctx, t, s, r, a, cb)
}
func (c *controlled) restart(ctx context.Context) (dp.Provider, dp.Provisioner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p != nil {
		_ = c.p.Close()
		c.p = nil
	}
	c.d.mu.Lock()
	t := c.d.tickets[c.root]
	c.d.mu.Unlock()
	if t != nil && t.mode == cf.Kill && t.point.Stage == cf.DuringRecovery && t.proc == nil {
		if err := c.remoteOpen(ctx, t); err != nil {
			return nil, nil, err
		}
	}
	p, err := open(ctx, c.root, c.d.hook(c.root))
	if err != nil {
		return nil, nil, err
	}
	c.p = p
	c.pending = nil
	return c, c, nil
}

type workerRequest struct {
	Scope         dp.Scope
	Expected      dp.Revision
	Actor         dp.Actor
	Changes       dp.ChangeSet
	Infos         []dp.ResourceInfo
	CallbackError bool
}
type workerMessage struct {
	Kind     string
	Snapshot dp.Snapshot
	Commit   dp.Commit
	Code     dp.Code
}

func workerHook(control, point string) faultHook {
	return func(ctx context.Context, id string) error {
		if id != point {
			return nil
		}
		at, _ := json.Marshal(time.Now().UTC())
		tmp := filepath.Join(control, "reached.tmp")
		if err := os.WriteFile(tmp, at, 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(control, "reached.json")); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}
}
func (t *ticket) start(action string) (*json.Decoder, *json.Encoder, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(binary, "-test.run=^TestPhysicalWorker$")
	cmd.Env = append(os.Environ(), "FS_WORKER=1", "FS_WORKER_ROOT="+t.root, "FS_WORKER_CONTROL="+t.control, "FS_WORKER_POINT="+t.point.ID, "FS_WORKER_ACTION="+action)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := os.OpenFile(filepath.Join(t.control, "worker.stderr"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stderr.Close()
		return nil, nil, err
	}
	t.mu.Lock()
	t.proc = cmd
	t.stderr = stderr
	t.stdin = stdin
	t.mu.Unlock()
	// Stdout is drained by the caller before Wait: avoid Wait closing the pipe
	// before the last protocol message is received.
	go func() { <-t.released; _ = stdin.Close() }()
	return json.NewDecoder(stdout), json.NewEncoder(stdin), nil
}
func (t *ticket) wait() {
	t.mu.Lock()
	cmd := t.proc
	t.mu.Unlock()
	err := cmd.Wait()
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	if t.stderr != nil {
		_ = t.stderr.Close()
	}
	exit := 0
	if err != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	t.mu.Lock()
	t.exit = exit
	t.mu.Unlock()
	close(t.exited)
}
func (c *controlled) remoteOpen(ctx context.Context, t *ticket) error {
	decoder, _, err := t.start("open")
	if err != nil {
		return err
	}
	var msg workerMessage
	err = decoder.Decode(&msg)
	t.wait()
	if err != nil {
		return fail(dp.Unavailable)
	}
	if msg.Code != "" {
		return fail(msg.Code)
	}
	return nil
}
func (c *controlled) remoteUpdate(ctx context.Context, t *ticket, s dp.Scope, r dp.Revision, a dp.Actor, cb func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	decoder, encoder, err := t.start("update")
	if err != nil {
		return dp.Commit{}, err
	}
	if err := encoder.Encode(workerRequest{Scope: s, Expected: r, Actor: a}); err != nil {
		_ = t.proc.Process.Kill()
		t.wait()
		return dp.Commit{}, err
	}
	var msg workerMessage
	if err := decoder.Decode(&msg); err != nil {
		t.wait()
		return dp.Commit{}, fail(dp.CommitOutcomeUnknown)
	}
	if msg.Kind != "callback" {
		t.wait()
		return dp.Commit{}, fail(msg.Code)
	}
	cs, callbackErr := cb(msg.Snapshot)
	request := workerRequest{Changes: cs, CallbackError: callbackErr != nil}
	if callbackErr == nil {
		for i, r := range cs.PutResources {
			name := filepath.Join(t.control, fmt.Sprintf("input-%d", i))
			f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				_ = t.proc.Process.Kill()
				t.wait()
				return dp.Commit{}, err
			}
			_, err = dp.CopyResource(ctx, f, r)
			ce := f.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				_ = t.proc.Process.Kill()
				t.wait()
				return dp.Commit{}, err
			}
			request.Infos = append(request.Infos, r.Info)
		}
	}
	if err := encoder.Encode(request); err != nil {
		_ = t.proc.Process.Kill()
		t.wait()
		return dp.Commit{}, err
	}
	err = decoder.Decode(&msg)
	t.wait()
	if callbackErr != nil {
		return dp.Commit{}, callbackErr
	}
	if err != nil {
		return dp.Commit{}, fail(dp.CommitOutcomeUnknown)
	}
	if msg.Code != "" {
		return dp.Commit{}, fail(msg.Code)
	}
	return msg.Commit, nil
}
func TestPhysicalWorker(t *testing.T) {
	if os.Getenv("FS_WORKER") != "1" {
		return
	}
	root, control, point, action := os.Getenv("FS_WORKER_ROOT"), os.Getenv("FS_WORKER_CONTROL"), os.Getenv("FS_WORKER_POINT"), os.Getenv("FS_WORKER_ACTION")
	ctx, cancel := context.WithTimeout(context.Background(), dp.RecoveryTimeout)
	defer cancel()
	encoder := json.NewEncoder(os.Stdout)
	decoder := json.NewDecoder(os.Stdin)
	respond := func(commit dp.Commit, err error) {
		code, _ := dp.CodeOf(err)
		_ = encoder.Encode(workerMessage{Kind: "done", Commit: commit, Code: code})
	}
	p, err := open(ctx, root, workerHook(control, point))
	if err != nil {
		respond(dp.Commit{}, err)
		return
	}
	defer p.Close()
	if action == "open" {
		respond(dp.Commit{}, nil)
		return
	}
	var request workerRequest
	if err := decoder.Decode(&request); err != nil {
		respond(dp.Commit{}, fail(dp.InvalidRequest))
		return
	}
	var inputs []*os.File
	defer func() {
		for _, f := range inputs {
			_ = f.Close()
		}
	}()
	commit, err := p.Update(ctx, request.Scope, request.Expected, request.Actor, func(snapshot dp.Snapshot) (dp.ChangeSet, error) {
		if err := encoder.Encode(workerMessage{Kind: "callback", Snapshot: snapshot}); err != nil {
			return dp.ChangeSet{}, err
		}
		var changes workerRequest
		if err := decoder.Decode(&changes); err != nil {
			return dp.ChangeSet{}, err
		}
		if changes.CallbackError {
			return dp.ChangeSet{}, errors.New("callback refused")
		}
		for i, info := range changes.Infos {
			f, err := os.Open(filepath.Join(control, fmt.Sprintf("input-%d", i)))
			if err != nil {
				return dp.ChangeSet{}, err
			}
			inputs = append(inputs, f)
			changes.Changes.PutResources = append(changes.Changes.PutResources, dp.ResourceInput{Info: info, Reader: f})
		}
		return changes.Changes, nil
	})
	respond(commit, err)
}

var _ cf.FaultDriver = (*driver)(nil)
var _ io.Reader = (*os.File)(nil)
