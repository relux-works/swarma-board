package providerdb

// The test controller talks to an actual disposable worker through inherited
// anonymous pipes. No socket, listener, HTTP server or host service is used.
// Hooks exist only in this test binary and return mechanisms, never verdicts.
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

type message struct {
	ID       uint64            `json:"id"`
	Kind     string            `json:"kind"`
	Op       string            `json:"op,omitempty"`
	Deadline int64             `json:"deadline,omitempty"`
	Scope    dp.Scope          `json:"scope"`
	Board    dp.Board          `json:"board"`
	Revision dp.Revision       `json:"revision"`
	Actor    dp.Actor          `json:"actor"`
	Digest   string            `json:"digest,omitempty"`
	Snapshot dp.Snapshot       `json:"snapshot"`
	Events   []dp.Event        `json:"events"`
	Changes  dp.ChangeSet      `json:"changes"`
	Infos    []dp.ResourceInfo `json:"infos"`
	Paths    []string          `json:"paths"`
	Commit   dp.Commit         `json:"commit"`
	Info     dp.ResourceInfo   `json:"info"`
	Handle   uint64            `json:"handle,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	N        int               `json:"n,omitempty"`
	Code     dp.Code           `json:"code,omitempty"`
	Context  string            `json:"context,omitempty"`
	EOF      bool              `json:"eof,omitempty"`
	Point    string            `json:"point,omitempty"`
}

func setError(m *message, e error) {
	if e == nil {
		return
	}
	if errors.Is(e, io.EOF) {
		m.EOF = true
		return
	}
	if errors.Is(e, context.Canceled) {
		m.Context = "canceled"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		m.Context = "deadline"
	}
	if c, ok := dp.CodeOf(e); ok {
		m.Code = c
	} else {
		m.Code = dp.StorageError
	}
}
func messageError(m message) error {
	if m.Context == "canceled" {
		return dp.ContextError(context.Canceled)
	}
	if m.Context == "deadline" {
		return dp.ContextError(context.DeadlineExceeded)
	}
	if m.Code != "" {
		return dp.NewError(m.Code)
	}
	if m.EOF {
		return io.EOF
	}
	return nil
}

type wire struct {
	enc *json.Encoder
	mu  sync.Mutex
}

func (w *wire) send(m message) error { w.mu.Lock(); defer w.mu.Unlock(); return w.enc.Encode(m) }

type workerServer struct {
	wire
	db          *DB
	mu          sync.Mutex
	seq         atomic.Uint64
	waits       map[uint64]chan message
	cancels     map[uint64]context.CancelFunc
	readers     map[uint64]io.ReadCloser
	checkpoints map[uint64]dp.Checkpoint
}

func (s *workerServer) roundtrip(kind, point string, snap dp.Snapshot) (message, error) {
	id := s.seq.Add(1)
	ch := make(chan message, 1)
	s.mu.Lock()
	s.waits[id] = ch
	s.mu.Unlock()
	if e := s.send(message{ID: id, Kind: kind, Point: point, Snapshot: snap}); e != nil {
		return message{}, e
	}
	m := <-ch
	s.mu.Lock()
	delete(s.waits, id)
	s.mu.Unlock()
	return m, messageError(m)
}
func TestWorkerProcess(t *testing.T) {
	root := os.Getenv("PROVIDERDB_TEST_ROOT")
	if root == "" {
		return
	}
	s := &workerServer{wire: wire{enc: json.NewEncoder(os.Stdout)}, waits: map[uint64]chan message{}, cancels: map[uint64]context.CancelFunc{}, readers: map[uint64]io.ReadCloser{}, checkpoints: map[uint64]dp.Checkpoint{}}
	// The decoder remains active while Open/recovery is paused by the controller.
	opened := make(chan struct{})
	go func() {
		ctx := context.Background()
		if deadline, _ := strconv.ParseInt(os.Getenv("PROVIDERDB_TEST_DEADLINE"), 10, 64); deadline != 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, time.Unix(0, deadline))
			defer cancel()
		}
		d, e := open(ctx, root, func(_ context.Context, p string) error { _, e := s.roundtrip("hook", p, dp.Snapshot{}); return e })
		s.db = d
		m := message{Kind: "opened"}
		setError(&m, e)
		_ = s.send(m)
		close(opened)
	}()
	dec := json.NewDecoder(bufio.NewReaderSize(os.Stdin, 128<<10))
	for {
		var m message
		if e := dec.Decode(&m); e != nil {
			os.Exit(0)
		}
		if m.Kind == "resume" || m.Kind == "callback-result" {
			s.mu.Lock()
			ch := s.waits[m.ID]
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Kind == "cancel" {
			s.mu.Lock()
			cancel := s.cancels[m.ID]
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			continue
		}
		go func(m message) { <-opened; s.handle(m) }(m)
	}
}
func (s *workerServer) handle(m message) {
	result := message{ID: m.ID, Kind: "response"}
	ctx := context.Background()
	var cancel context.CancelFunc
	if m.Deadline != 0 {
		ctx, cancel = context.WithDeadline(ctx, time.Unix(0, m.Deadline))
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	s.mu.Lock()
	s.cancels[m.ID] = cancel
	s.mu.Unlock()
	keep := false
	defer func() {
		if !keep {
			cancel()
			s.mu.Lock()
			delete(s.cancels, m.ID)
			s.mu.Unlock()
		}
		_ = s.send(result)
	}()
	var e error
	if s.db == nil {
		setError(&result, dp.NewError(dp.Unavailable))
		return
	}
	switch m.Op {
	case "view":
		result.Snapshot, e = s.db.View(ctx, m.Scope)
	case "ready":
		e = s.db.Ready(ctx)
	case "ensure":
		e = s.db.EnsureEmpty(ctx, m.Board)
	case "close":
		e = s.db.Close()
	case "update":
		var inputs []*os.File
		defer func() {
			for _, f := range inputs {
				_ = f.Close()
			}
		}()
		result.Commit, e = s.db.Update(ctx, m.Scope, m.Revision, m.Actor, func(snap dp.Snapshot) (dp.ChangeSet, error) {
			id := s.seq.Add(1)
			ch := make(chan message, 1)
			s.mu.Lock()
			s.waits[id] = ch
			s.mu.Unlock()
			_ = s.send(message{ID: id, Kind: "callback", Handle: m.ID, Snapshot: snap})
			response := <-ch
			s.mu.Lock()
			delete(s.waits, id)
			s.mu.Unlock()
			if e := messageError(response); e != nil {
				return dp.ChangeSet{}, e
			}
			c := response.Changes
			handles := []*os.File{}
			for i, info := range response.Infos {
				// The controller's paths are inside its private staging, never user paths.
				f, e := os.Open(response.Paths[i])
				if e != nil {
					for _, h := range handles {
						_ = h.Close()
					}
					return dp.ChangeSet{}, dp.NewError(dp.StorageError)
				}
				handles = append(handles, f)
				c.PutResources = append(c.PutResources, dp.ResourceInput{Info: info, Reader: f})
			}
			// Update owns consumption; close controller staging handles after the whole
			// request using a request-local list, never by a resource input callback.
			inputs = handles
			return c, nil
		})
	case "read-resource":
		var r dp.ResourceReader
		r, e = s.db.ReadResource(ctx, m.Scope, m.Digest)
		if e == nil {
			keep = true
			result.Info = r.Info
			result.Handle = s.seq.Add(1)
			s.mu.Lock()
			s.readers[result.Handle] = r.Reader
			s.mu.Unlock()
		}
	case "checkpoint":
		var cp dp.Checkpoint
		cp, e = s.db.Checkpoint(ctx, m.Scope)
		if e == nil {
			keep = true
			result.Snapshot = cp.Snapshot()
			result.Events = cp.Events()
			result.Handle = s.seq.Add(1)
			s.mu.Lock()
			s.checkpoints[result.Handle] = cp
			s.mu.Unlock()
		}
	case "open-resource":
		s.mu.Lock()
		cp := s.checkpoints[m.Handle]
		s.mu.Unlock()
		if cp == nil {
			e = dp.NewError(dp.Unavailable)
			break
		}
		var r io.ReadCloser
		r, e = cp.OpenResource(m.Digest)
		if e == nil {
			result.Handle = s.seq.Add(1)
			s.mu.Lock()
			s.readers[result.Handle] = r
			s.mu.Unlock()
		}
	case "read":
		s.mu.Lock()
		r := s.readers[m.Handle]
		s.mu.Unlock()
		if r == nil {
			e = dp.NewError(dp.Unavailable)
			break
		}
		if _, e = s.roundtrip("hook", "exposed-read", dp.Snapshot{}); e != nil {
			break
		}
		n := m.N
		if n > 128<<10 {
			n = 128 << 10
		}
		if n < 0 {
			e = dp.NewError(dp.InvalidRequest)
			break
		}
		result.Data = make([]byte, n)
		result.N, e = r.Read(result.Data)
		result.Data = result.Data[:result.N]
	case "reader-close":
		s.mu.Lock()
		r := s.readers[m.Handle]
		s.mu.Unlock()
		if r == nil {
			e = dp.NewError(dp.Unavailable)
			break
		}
		_, e = s.roundtrip("hook", "exposed-close", dp.Snapshot{})
		ce := r.Close()
		if e == nil {
			e = ce
		}
	case "checkpoint-close":
		s.mu.Lock()
		cp := s.checkpoints[m.Handle]
		s.mu.Unlock()
		if cp != nil {
			e = cp.Close()
		}
	default:
		e = dp.NewError(dp.InvalidRequest)
	}
	setError(&result, e)
}

type process struct {
	wire
	cmd       *exec.Cmd
	root      string
	mu        sync.Mutex
	seq       atomic.Uint64
	pending   map[uint64]chan message
	callbacks map[uint64]func(dp.Snapshot) (dp.ChangeSet, error)
	contexts  map[uint64]context.Context
	errors    map[uint64]error
	stage     map[uint64]string
	done      chan struct{}
	opened    chan message
	exit      int
	driver    *physicalDriver
}

func startProcess(ctx context.Context, root string, driver *physicalDriver) (*process, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	command := exec.Command(exe, "-test.run=^TestWorkerProcess$")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.Getenv("TMPDIR"), "PROVIDERDB_TEST_ROOT=" + root}
	if deadline, ok := ctx.Deadline(); ok {
		command.Env = append(command.Env, "PROVIDERDB_TEST_DEADLINE="+strconv.FormatInt(deadline.UnixNano(), 10))
	}
	in, e := command.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := command.StdoutPipe()
	if e != nil {
		return nil, e
	}
	command.Stderr = os.Stderr
	p := &process{wire: wire{enc: json.NewEncoder(in)}, cmd: command, root: root, contexts: map[uint64]context.Context{}, pending: map[uint64]chan message{}, callbacks: map[uint64]func(dp.Snapshot) (dp.ChangeSet, error){}, errors: map[uint64]error{}, stage: map[uint64]string{}, done: make(chan struct{}), opened: make(chan message, 1), driver: driver}
	if e = command.Start(); e != nil {
		return nil, e
	}
	go func() {
		decoder := json.NewDecoder(bufio.NewReaderSize(out, 128<<10))
		for {
			var m message
			if e := decoder.Decode(&m); e != nil {
				break
			}
			switch m.Kind {
			case "opened":
				p.opened <- m
			case "hook":
				go driver.reached(p, m)
			case "callback":
				go p.callback(m)
			case "response":
				p.mu.Lock()
				ch := p.pending[m.ID]
				p.mu.Unlock()
				if ch != nil {
					ch <- m
				}
			}
		}
		_ = command.Wait()
		p.exit = command.ProcessState.ExitCode()
		close(p.done)
	}()
	select {
	case m := <-p.opened:
		if e := messageError(m); e != nil {
			_ = p.kill()
			return nil, e
		}
		return p, nil
	case <-p.done:
		return nil, dp.NewError(dp.Unavailable)
	case <-ctx.Done():
		_ = p.kill()
		return nil, dp.ContextError(ctx.Err())
	}
}
func (p *process) kill() int { _ = p.cmd.Process.Kill(); <-p.done; return p.exit }
func (p *process) call(ctx context.Context, m message, callback func(dp.Snapshot) (dp.ChangeSet, error)) (message, error) {
	if e := checkContext(ctx); e != nil {
		return message{}, e
	}
	m.ID = p.seq.Add(1)
	m.Kind = "request"
	if deadline, ok := ctx.Deadline(); ok {
		m.Deadline = deadline.UnixNano()
	}
	ch := make(chan message, 1)
	p.mu.Lock()
	p.pending[m.ID] = ch
	p.contexts[m.ID] = ctx
	if callback != nil {
		p.callbacks[m.ID] = callback
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		dir := p.stage[m.ID]
		delete(p.stage, m.ID)
		delete(p.pending, m.ID)
		delete(p.callbacks, m.ID)
		delete(p.contexts, m.ID)
		delete(p.errors, m.ID)
		p.mu.Unlock()
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()
	if e := p.send(m); e != nil {
		return message{}, dp.NewError(dp.Unavailable)
	}
	var r message
	select {
	case r = <-ch:
	case <-p.done:
		return message{}, dp.NewError(dp.Unavailable)
	case <-ctx.Done():
		_ = p.send(message{ID: m.ID, Kind: "cancel"})
		select {
		case r = <-ch:
		case <-p.done:
			return message{}, dp.NewError(dp.Unavailable)
		}
	}
	p.mu.Lock()
	callbackErr := p.errors[m.ID]
	p.mu.Unlock()
	if callbackErr != nil {
		return message{}, callbackErr
	}
	return r, messageError(r)
}
func (p *process) callback(m message) {
	response := message{ID: m.ID, Kind: "callback-result"}
	p.mu.Lock()
	callback := p.callbacks[m.Handle]
	ctx := p.contexts[m.Handle]
	p.mu.Unlock()
	if callback == nil {
		setError(&response, dp.NewError(dp.InvalidRequest))
		_ = p.send(response)
		return
	}
	c, e := callback(dp.CloneSnapshot(m.Snapshot))
	if e != nil {
		p.mu.Lock()
		p.errors[m.Handle] = e
		p.mu.Unlock()
		setError(&response, e)
		_ = p.send(response)
		return
	}
	if ctx != nil && ctx.Err() != nil {
		setError(&response, dp.ContextError(ctx.Err()))
		_ = p.send(response)
		return
	}
	c = dp.CloneChangeSet(c)
	if e = dp.ValidateChangeSet(c); e == nil && len(c.PutResources) > 0 {
		var dir string
		dir, e = os.MkdirTemp("", "provider-input-")
		if e == nil {
			p.mu.Lock()
			p.stage[m.Handle] = dir
			p.mu.Unlock()
			for i, input := range c.PutResources {
				path := filepath.Join(dir, strconv.Itoa(i))
				var f *os.File
				f, e = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e != nil {
					break
				}
				_, e = dp.CopyResource(context.Background(), f, input)
				ce := f.Close()
				if e == nil {
					e = ce
				}
				if e != nil {
					break
				}
				response.Paths = append(response.Paths, path)
				response.Infos = append(response.Infos, input.Info)
			}
		}
	}
	c.PutResources = nil
	response.Changes = c
	setError(&response, e)
	_ = p.send(response)
}
func (p *process) View(ctx context.Context, s dp.Scope) (dp.Snapshot, error) {
	r, e := p.call(ctx, message{Op: "view", Scope: s}, nil)
	if e != nil {
		return dp.Snapshot{}, e
	}
	return r.Snapshot, nil
}
func (p *process) EnsureEmpty(ctx context.Context, b dp.Board) error {
	_, e := p.call(ctx, message{Op: "ensure", Board: b}, nil)
	return e
}
func (p *process) Ready(ctx context.Context) error {
	_, e := p.call(ctx, message{Op: "ready"}, nil)
	return e
}
func (p *process) Update(ctx context.Context, s dp.Scope, r dp.Revision, a dp.Actor, fn func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	if fn == nil {
		return dp.Commit{}, dp.NewError(dp.InvalidRequest)
	}
	reply, e := p.call(ctx, message{Op: "update", Scope: s, Revision: r, Actor: a}, fn)
	if e != nil {
		return dp.Commit{}, e
	}
	return reply.Commit, nil
}
func (p *process) Close() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	_, e := p.call(context.Background(), message{Op: "close"}, nil)
	return e
}
func (p *process) ReadResource(ctx context.Context, s dp.Scope, digest string) (dp.ResourceReader, error) {
	r, e := p.call(ctx, message{Op: "read-resource", Scope: s, Digest: digest}, nil)
	if e != nil {
		return dp.ResourceReader{}, e
	}
	return dp.ResourceReader{Info: r.Info, Reader: &remoteReader{p: p, ctx: ctx, id: r.Handle}}, nil
}
func (p *process) Checkpoint(ctx context.Context, s dp.Scope) (dp.Checkpoint, error) {
	r, e := p.call(ctx, message{Op: "checkpoint", Scope: s}, nil)
	if e != nil {
		return nil, e
	}
	return &remoteCheckpoint{p: p, ctx: ctx, id: r.Handle, s: r.Snapshot, events: r.Events}, nil
}

type remoteReader struct {
	p        *process
	ctx      context.Context
	id       uint64
	once     sync.Once
	closeErr error
}

func (r *remoteReader) Read(buf []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, dp.ContextError(e)
	}
	m, e := r.p.call(r.ctx, message{Op: "read", Handle: r.id, N: len(buf)}, nil)
	return copy(buf, m.Data), e
}
func (r *remoteReader) Close() error {
	r.once.Do(func() { _, r.closeErr = r.p.call(context.Background(), message{Op: "reader-close", Handle: r.id}, nil) })
	return r.closeErr
}

type remoteCheckpoint struct {
	p        *process
	ctx      context.Context
	id       uint64
	s        dp.Snapshot
	events   []dp.Event
	once     sync.Once
	closeErr error
	mu       sync.Mutex
	closed   bool
}

func (c *remoteCheckpoint) Snapshot() dp.Snapshot { return dp.CloneSnapshot(c.s) }
func (c *remoteCheckpoint) Events() []dp.Event    { return dp.CloneEvents(c.events) }
func (c *remoteCheckpoint) OpenResource(d string) (io.ReadCloser, error) {
	if e := c.ctx.Err(); e != nil {
		return nil, dp.ContextError(e)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, dp.NewError(dp.Unavailable)
	}
	m, e := c.p.call(c.ctx, message{Op: "open-resource", Handle: c.id, Digest: d}, nil)
	if e != nil {
		return nil, e
	}
	return &remoteReader{p: c.p, ctx: c.ctx, id: m.Handle}, nil
}
func (c *remoteCheckpoint) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		_, c.closeErr = c.p.call(context.Background(), message{Op: "checkpoint-close", Handle: c.id}, nil)
	})
	return c.closeErr
}

func factory(driver *physicalDriver) cf.Factory {
	return cf.Factory{Name: "database", Open: func(ctx context.Context, root string) (*cf.Instance, error) {
		p, e := startProcess(ctx, root, driver)
		if e != nil {
			return nil, e
		}
		var mu sync.Mutex
		current := p
		instance := &cf.Instance{Provider: p, Provisioner: p}
		instance.Restart = func(ctx context.Context) (dp.Provider, dp.Provisioner, error) {
			mu.Lock()
			defer mu.Unlock()
			_ = current.Close()
			_ = current.kill()
			next, e := startProcess(ctx, root, driver)
			if e != nil {
				return nil, nil, e
			}
			current = next
			return next, next, nil
		}
		instance.OpenPeer = func(ctx context.Context) (dp.Provider, dp.Provisioner, func() error, error) {
			peer, e := startProcess(ctx, root, driver)
			if e != nil {
				return nil, nil, nil, e
			}
			return peer, peer, func() error { _ = peer.Close(); peer.kill(); return nil }, nil
		}
		instance.Cleanup = func() error { mu.Lock(); defer mu.Unlock(); _ = current.Close(); current.kill(); return nil }
		return instance, nil
	}}
}
