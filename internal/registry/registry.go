// Package registry owns private per-project handles and drains leases on close.
package registry

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
)

type Registry struct {
	mu              sync.Mutex
	cond            *sync.Cond
	dir             string
	dbs             map[string]*store.DB
	leases          int
	closing, closed bool
	closeErr        error
	lock            *store.DirectoryLock
}

func unavailable() error { return &contract.Error{Code: "unavailable", Message: "storage unavailable"} }
func New(dir string) (*Registry, error) {
	if dir == "" {
		return nil, unavailable()
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	st, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return nil, unavailable()
	}
	// The separate lock file prevents a second registry provisioning disjoint projects
	// in the same directory; the database itself retains bbolt's finite lock.
	lock, e := store.LockDirectory(dir)
	if e != nil {
		return nil, e
	}
	r := &Registry{dir: dir, dbs: map[string]*store.DB{}, lock: lock}
	r.cond = sync.NewCond(&r.mu)
	entries, e := os.ReadDir(dir)
	if e != nil {
		r.Close()
		return nil, e
	}
	for _, entry := range entries {
		if entry.Name() == ".owner.lock" {
			continue
		}
		if filepath.Ext(entry.Name()) != ".db" {
			r.Close()
			return nil, unavailable()
		}
		id := entry.Name()[:len(entry.Name())-3]
		if !store.ValidProjectID(id) {
			r.Close()
			return nil, unavailable()
		}
		d, e := store.Open(filepath.Join(dir, entry.Name()), contract.Project{ID: id}, false)
		if e != nil {
			r.Close()
			return nil, e
		}
		r.dbs[id] = d
	}
	return r, nil
}

// Provision only creates missing empty databases, never rewrites metadata or facts.
func (r *Registry) Provision(ctx context.Context, projects []contract.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return unavailable()
	}
	for _, p := range projects {
		if e := ctx.Err(); e != nil {
			return e
		}
		if !store.ValidProjectID(p.ID) {
			return &contract.Error{Code: "invalid_request", Message: "invalid project"}
		}
		if r.dbs[p.ID] != nil {
			continue
		}
		d, e := store.Open(filepath.Join(r.dir, p.ID+".db"), p, true)
		if e != nil {
			return e
		}
		r.dbs[p.ID] = d
	}
	return nil
}
func (r *Registry) acquire(id string) (*store.DB, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, nil, unavailable()
	}
	d := r.dbs[id]
	if d == nil {
		return nil, nil, &contract.Error{Code: "not_found", Message: "project not found"}
	}
	r.leases++
	var once sync.Once
	return d, func() { once.Do(func() { r.mu.Lock(); r.leases--; r.cond.Broadcast(); r.mu.Unlock() }) }, nil
}
func (r *Registry) View(ctx context.Context, id string) (contract.Snapshot, error) {
	d, done, e := r.acquire(id)
	if e != nil {
		return contract.Snapshot{}, e
	}
	defer done()
	return d.View(ctx, id)
}

// ExportView holds one registry lease for the coherent store read, so Close
// cannot drain the handle between snapshot and journal extraction.
func (r *Registry) ExportView(ctx context.Context, id string) (contract.Snapshot, []store.AuditEvent, error) {
	d, done, e := r.acquire(id)
	if e != nil {
		return contract.Snapshot{}, nil, e
	}
	defer done()
	return d.ExportView(ctx, id)
}
func (r *Registry) Update(ctx context.Context, id string, v contract.Revision, a contract.AuditContext, fn func(contract.Snapshot) (contract.ChangeSet, error)) (contract.Commit, error) {
	d, done, e := r.acquire(id)
	if e != nil {
		return contract.Commit{}, e
	}
	defer done()
	return d.Update(ctx, id, v, a, fn)
}
func (r *Registry) Backup(ctx context.Context, id string, w io.Writer) error {
	d, done, e := r.acquire(id)
	if e != nil {
		return e
	}
	defer done()
	return d.Backup(ctx, w)
}
func (r *Registry) Ready(ctx context.Context) error {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return unavailable()
	}
	r.leases++
	dbs := make([]*store.DB, 0, len(r.dbs))
	for _, d := range r.dbs {
		dbs = append(dbs, d)
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.leases--; r.cond.Broadcast(); r.mu.Unlock() }()
	if e := ctx.Err(); e != nil {
		return e
	}
	for _, d := range dbs {
		if e := d.Ready(ctx); e != nil {
			return e
		}
	}
	return nil
}
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		for !r.closed {
			r.cond.Wait()
		}
		return r.closeErr
	}
	r.closing = true
	for r.leases > 0 {
		r.cond.Wait()
	}
	for _, d := range r.dbs {
		if e := d.Close(); e != nil && r.closeErr == nil {
			r.closeErr = e
		}
	}
	if r.lock != nil {
		if e := r.lock.Close(); e != nil && r.closeErr == nil {
			r.closeErr = e
		}
	}
	r.closed = true
	r.cond.Broadcast()
	return r.closeErr
}

var _ contract.Repository = (*Registry)(nil)
