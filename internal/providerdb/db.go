// Package providerdb implements new, explicitly scoped provider state using
// Storm's retained bbolt engine. It opens no legacy database or board tree.
package providerdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asdine/storm/v3"
	dp "github.com/relux-works/swarma-board/dataprovider"
	bolt "go.etcd.io/bbolt"
)

var schema = []byte("scoped-provider/1")
var scopes = []byte("scopes")

const chunkSize = 64 << 10

// DB supports one private local-directory owner. Writers are globally serial;
// successful reads are coherent transactions privately spooled before exposure.
// It does not claim bounded total RAM: bbolt retains dirty transaction pages.
type DB struct {
	finalOnce                sync.Once
	finalDone                chan struct{}
	db                       *storm.DB
	root                     string
	writers                  chan struct{}
	mu                       sync.Mutex
	stopped, closed, latched bool
	active                   int
	drained                  chan struct{}
	closeErr                 error
	// Only same-package tests can install physical fault hooks.
	hook func(context.Context, string) error
}

func safe(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := dp.CodeOf(err); ok {
		return err
	}
	return dp.NewError(dp.StorageError)
}
func checkContext(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return dp.ContextError(e)
	}
	return nil
}
func key(s dp.Scope) []byte  { return []byte(s.ProductID + "\x00" + s.BoardID) }
func number(n uint64) []byte { p := make([]byte, 8); binary.BigEndian.PutUint64(p, n); return p }
func privatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return dp.NewError(dp.InvalidRequest)
	}
	cur := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(path, cur), cur) {
		cur = filepath.Join(cur, part)
		st, e := os.Lstat(cur)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return dp.NewError(dp.Unavailable)
		}
	}
	st, e := os.Stat(path)
	if e != nil || st.Mode().Perm() != 0700 {
		return dp.NewError(dp.Unavailable)
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return dp.NewError(dp.Unavailable)
	}
	return nil
}

// Open admits an empty private root or this exact provider schema. Existing
// unknown files/formats are refused and retained. Durable sync is always on.
func Open(ctx context.Context, root string) (*DB, error) { return open(ctx, root, nil) }
func open(ctx context.Context, root string, hook func(context.Context, string) error) (*DB, error) {
	ctx, cancel := dp.BoundedContext(ctx, dp.RecoveryTimeout)
	defer cancel()
	if e := checkContext(ctx); e != nil {
		return nil, e
	}
	if e := privatePath(root); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return nil, dp.NewError(dp.Unavailable)
	}
	fresh := true
	for _, entry := range entries {
		st, e := os.Lstat(filepath.Join(root, entry.Name()))
		if e != nil {
			return nil, dp.NewError(dp.Unavailable)
		}
		if entry.Name() == "provider.db" {
			if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Sys().(*syscall.Stat_t).Nlink != 1 {
				return nil, dp.NewError(dp.Unavailable)
			}
			fresh = false
		} else if strings.HasPrefix(entry.Name(), "stage-") || strings.HasPrefix(entry.Name(), "pin-") {
			// Disposable predecision staging and read pins never authorize recovery.
			// Retain orphan directories after death for custody; do not follow aliases.
			if !st.IsDir() || st.Mode().Perm() != 0700 {
				return nil, dp.NewError(dp.StorageError)
			}
		} else {
			return nil, dp.NewError(dp.UnsupportedSchema)
		}
	}
	path := filepath.Join(root, "provider.db")
	if fresh {
		if len(entries) != 0 {
			return nil, dp.NewError(dp.UnsupportedSchema)
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, dp.NewError(dp.Unavailable)
		}
		if e = f.Close(); e != nil {
			return nil, safe(e)
		}
	}
	timeout := dp.LockTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, dp.ContextError(context.DeadlineExceeded)
	}
	raw, e := bolt.Open(path, 0600, &bolt.Options{Timeout: timeout})
	if e != nil {
		if checkContext(ctx) != nil {
			return nil, checkContext(ctx)
		}
		return nil, dp.NewError(dp.Unavailable)
	}
	d := &DB{db: &storm.DB{Bolt: raw}, root: root, writers: make(chan struct{}, 1), drained: make(chan struct{}), finalDone: make(chan struct{}), hook: hook}
	fail := func(err error) (*DB, error) { _ = raw.Close(); return nil, safe(err) }
	if fresh {
		if e = checkContext(ctx); e != nil {
			return fail(e)
		}
		e = raw.Update(func(tx *bolt.Tx) error {
			b, e := tx.CreateBucket(scopes)
			if e != nil {
				return e
			}
			return b.Put([]byte("schema"), schema)
		})
		if e != nil {
			return fail(e)
		}
		f, e := os.Open(root)
		if e != nil {
			return fail(e)
		}
		e = f.Sync()
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return fail(e)
		}
	}
	if e = d.point(ctx, "recovery-validate"); e != nil {
		return fail(e)
	}
	e = raw.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(scopes)
		if b == nil || !bytes.Equal(b.Get([]byte("schema")), schema) {
			return dp.NewError(dp.UnsupportedSchema)
		}
		if e := tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if !bytes.Equal(name, scopes) {
				return dp.NewError(dp.UnsupportedSchema)
			}
			return nil
		}); e != nil {
			return e
		}
		return b.ForEach(func(k, v []byte) error {
			if bytes.Equal(k, []byte("schema")) {
				return nil
			}
			if v != nil {
				return dp.NewError(dp.StorageError)
			}
			s, events, e := load(b.Bucket(k))
			if e != nil {
				return e
			}
			if !bytes.Equal(k, key(s.Board.Scope)) {
				return dp.NewError(dp.StorageError)
			}
			if e = dp.ValidateEvents(s, events); e != nil {
				return dp.NewError(dp.StorageError)
			}
			for _, r := range s.Resources {
				if e = verifyChunks(ctx, b.Bucket(k).Bucket([]byte("payloads")).Bucket([]byte(r.SHA256)), r); e != nil {
					return e
				}
			}
			return checkContext(ctx)
		})
	})
	if e != nil {
		return fail(e)
	}
	if e = checkContext(ctx); e != nil {
		return fail(e)
	}
	return d, nil
}
func (d *DB) point(ctx context.Context, p string) error {
	if d.hook != nil {
		return d.hook(ctx, p)
	}
	return nil
}
func (d *DB) admit() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.closed || d.latched {
		return dp.NewError(dp.Unavailable)
	}
	d.active++
	return nil
}
func (d *DB) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active--
	if d.stopped && d.active == 0 {
		select {
		case <-d.drained:
		default:
			close(d.drained)
		}
	}
}
func (d *DB) latch() { d.mu.Lock(); d.latched = true; d.mu.Unlock() }
func (d *DB) lock(ctx context.Context) error {
	timer := time.NewTimer(dp.LockTimeout)
	defer timer.Stop()
	select {
	case d.writers <- struct{}{}:
		if e := checkContext(ctx); e != nil {
			<-d.writers
			return e
		}
		// An admitted writer may have waited behind an ambiguous commit. Hold
		// the writer token while rechecking so no new transaction crosses it.
		d.mu.Lock()
		latched := d.latched
		d.mu.Unlock()
		if latched {
			<-d.writers
			return dp.NewError(dp.Unavailable)
		}
		return nil
	case <-ctx.Done():
		return dp.ContextError(ctx.Err())
	case <-timer.C:
		return dp.NewError(dp.Unavailable)
	}
}
func (d *DB) unlock() { <-d.writers }

// Each scope is one integrity envelope. The checksum detects accidental stopped
// metadata/history edits; it is neither a signature nor an authority proof.
func checksum(snapshot []byte, events *bolt.Bucket) ([]byte, error) {
	if events == nil {
		return nil, dp.NewError(dp.StorageError)
	}
	h := sha256.New()
	h.Write(snapshot)
	e := events.ForEach(func(k, v []byte) error {
		if len(k) != 8 || v == nil {
			return dp.NewError(dp.StorageError)
		}
		h.Write(k)
		h.Write(v)
		return nil
	})
	return h.Sum(nil), e
}
func load(b *bolt.Bucket) (dp.Snapshot, []dp.Event, error) {
	var s dp.Snapshot
	bad := func() (dp.Snapshot, []dp.Event, error) { return dp.Snapshot{}, nil, dp.NewError(dp.StorageError) }
	if b == nil {
		return bad()
	}
	if e := b.ForEach(func(k, v []byte) error {
		switch string(k) {
		case "snapshot", "integrity":
			if v == nil {
				return dp.NewError(dp.StorageError)
			}
		case "events", "payloads":
			if v != nil {
				return dp.NewError(dp.StorageError)
			}
		default:
			return dp.NewError(dp.UnsupportedSchema)
		}
		return nil
	}); e != nil {
		return dp.Snapshot{}, nil, e
	}
	raw := b.Get([]byte("snapshot"))
	if len(raw) == 0 || int64(len(raw)) > dp.MaxSnapshotBytes || strictJSON(raw, &s) != nil {
		return bad()
	}
	if dp.ValidateSnapshot(s) != nil {
		return bad()
	}
	eb := b.Bucket([]byte("events"))
	digest, e := checksum(raw, eb)
	if e != nil || !bytes.Equal(digest, b.Get([]byte("integrity"))) {
		return bad()
	}
	events := []dp.Event{}
	var total int64
	e = eb.ForEach(func(k, v []byte) error {
		total += int64(len(v)) + 1
		if total > dp.MaxEventNDJSONBytes || len(k) != 8 || binary.BigEndian.Uint64(k) != uint64(len(events))+1 {
			return dp.NewError(dp.StorageError)
		}
		var event dp.Event
		if strictJSON(v, &event) != nil {
			return dp.NewError(dp.StorageError)
		}
		events = append(events, event)
		return nil
	})
	if e != nil || dp.ValidateEvents(s, events) != nil {
		return bad()
	}
	pb := b.Bucket([]byte("payloads"))
	if pb == nil {
		return bad()
	}
	count := 0
	e = pb.ForEach(func(k, v []byte) error {
		if v != nil || dp.ValidateDigest(string(k)) != nil {
			return dp.NewError(dp.StorageError)
		}
		count++
		return nil
	})
	if e != nil || count != len(s.Resources) {
		return bad()
	}
	for _, r := range s.Resources {
		if pb.Bucket([]byte(r.SHA256)) == nil {
			return bad()
		}
	}
	return s, events, nil
}
func persist(b *bolt.Bucket, s dp.Snapshot, event *dp.Event) error {
	var buf bytes.Buffer
	if _, e := dp.EncodeSnapshot(&buf, s); e != nil {
		return e
	}
	if e := b.Put([]byte("snapshot"), buf.Bytes()); e != nil {
		return e
	}
	eb := b.Bucket([]byte("events"))
	if eb == nil {
		return dp.NewError(dp.StorageError)
	}
	if event != nil {
		var ev bytes.Buffer
		if _, e := dp.EncodeEvent(&ev, *event); e != nil {
			return e
		}
		var size int64
		e := eb.ForEach(func(_, v []byte) error { size += int64(len(v)) + 1; return nil })
		if e != nil {
			return e
		}
		if int64(ev.Len())+1 > dp.MaxEventNDJSONBytes-size {
			return dp.NewError(dp.PayloadTooLarge)
		}
		if e = eb.Put(number(event.Revision.Counter), ev.Bytes()); e != nil {
			return e
		}
	}
	h, e := checksum(buf.Bytes(), eb)
	if e != nil {
		return e
	}
	return b.Put([]byte("integrity"), h)
}
func verifyChunks(ctx context.Context, b *bolt.Bucket, r dp.ResourceInfo) error {
	if b == nil {
		return dp.NewError(dp.StorageError)
	}
	h := sha256.New()
	var n int64
	var index uint64
	e := b.ForEach(func(k, v []byte) error {
		if e := checkContext(ctx); e != nil {
			return e
		}
		if len(k) != 8 || binary.BigEndian.Uint64(k) != index || len(v) > chunkSize || v == nil {
			return dp.NewError(dp.StorageError)
		}
		index++
		n += int64(len(v))
		if n > r.Size {
			return dp.NewError(dp.StorageError)
		}
		h.Write(v)
		return nil
	})
	if e != nil {
		return safe(e)
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return dp.NewError(dp.StorageError)
	}
	return nil
}
func (d *DB) Ready(ctx context.Context) error {
	ctx, cancel := dp.BoundedContext(ctx, dp.ReadTimeout)
	defer cancel()
	if e := checkContext(ctx); e != nil {
		return e
	}
	if e := d.admit(); e != nil {
		return e
	}
	defer d.release()
	return nil
}
func (d *DB) EnsureEmpty(ctx context.Context, board dp.Board) error {
	ctx, cancel := dp.BoundedContext(ctx, dp.UpdateTimeout)
	defer cancel()
	if e := dp.ValidateScope(board.Scope); e != nil {
		return e
	}
	s := dp.Snapshot{Board: board, Revision: dp.Revision{Scope: board.Scope}, Elements: []dp.Element{}, Provenance: []dp.ProvenanceFile{}, Resources: []dp.ResourceInfo{}}
	if e := dp.ValidateSnapshot(s); e != nil {
		return dp.NewError(dp.InvalidRequest)
	}
	if e := checkContext(ctx); e != nil {
		return e
	}
	if e := d.admit(); e != nil {
		return e
	}
	defer d.release()
	if e := d.lock(ctx); e != nil {
		return e
	}
	defer d.unlock()
	tx, e := d.db.Bolt.Begin(true)
	if e != nil {
		return safe(e)
	}
	defer tx.Rollback()
	top := tx.Bucket(scopes)
	if b := top.Bucket(key(board.Scope)); b != nil {
		old, _, e := load(b)
		if e != nil {
			return e
		}
		if old.Board != board {
			return dp.NewError(dp.ScopeConflict)
		}
		return nil
	}
	b, e := top.CreateBucket(key(board.Scope))
	if e != nil {
		return safe(e)
	}
	if _, e = b.CreateBucket([]byte("events")); e != nil {
		return safe(e)
	}
	if _, e = b.CreateBucket([]byte("payloads")); e != nil {
		return safe(e)
	}
	if e = persist(b, s, nil); e != nil {
		return safe(e)
	}
	if e = checkContext(ctx); e != nil {
		return e
	}
	// Precommit refusals above roll back. Once Commit starts, its error cannot
	// establish that the new scope was not durably published.
	if e = tx.Commit(); e != nil {
		d.latch()
		return dp.NewError(dp.CommitOutcomeUnknown)
	}
	return nil
}
func (d *DB) View(ctx context.Context, scope dp.Scope) (dp.Snapshot, error) {
	ctx, cancel := dp.BoundedContext(ctx, dp.ReadTimeout)
	defer cancel()
	if e := dp.ValidateScope(scope); e != nil {
		return dp.Snapshot{}, e
	}
	if e := checkContext(ctx); e != nil {
		return dp.Snapshot{}, e
	}
	if e := d.admit(); e != nil {
		return dp.Snapshot{}, e
	}
	defer d.release()
	var s dp.Snapshot
	e := d.db.Bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(scopes).Bucket(key(scope))
		if b == nil {
			return dp.NewError(dp.NotFound)
		}
		var err error
		s, _, err = load(b)
		if err != nil {
			return err
		}
		return checkContext(ctx)
	})
	if e != nil {
		return dp.Snapshot{}, safe(e)
	}
	return s, nil
}
func equalElement(a, b dp.Element) bool {
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	return e == nil && bytes.Equal(x, y)
}
func strictJSON(raw []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(value); e != nil {
		return e
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return dp.NewError(dp.StorageError)
	}
	return nil
}
func changedElements(old, next []dp.Element) []string {
	before := map[string]dp.Element{}
	after := map[string]dp.Element{}
	for _, e := range old {
		before[e.ID] = e
	}
	for _, e := range next {
		after[e.ID] = e
	}
	ids := []string{}
	for id, a := range before {
		if b, ok := after[id]; !ok || !equalElement(a, b) {
			ids = append(ids, id)
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
func apply(s dp.Snapshot, c dp.ChangeSet) (dp.Snapshot, []string, error) {
	next := dp.CloneSnapshot(s)
	elems := map[string]dp.Element{}
	for _, e := range next.Elements {
		elems[strings.ToLower(e.ID)] = e
	}
	for _, id := range c.Deletes {
		k := strings.ToLower(id)
		if _, ok := elems[k]; !ok {
			return dp.Snapshot{}, nil, dp.NewError(dp.NotFound)
		}
		delete(elems, k)
	}
	for _, e := range c.Upserts {
		elems[strings.ToLower(e.ID)] = dp.CloneElement(e)
	}
	next.Elements = []dp.Element{}
	for _, e := range elems {
		next.Elements = append(next.Elements, e)
	}
	sort.Slice(next.Elements, func(i, j int) bool { return next.Elements[i].ID < next.Elements[j].ID })
	if c.Provenance != nil {
		next.Provenance = dp.CloneChangeSet(c).Provenance
		sort.Slice(next.Provenance, func(i, j int) bool { return next.Provenance[i].Path < next.Provenance[j].Path })
	}
	resources := map[string]dp.ResourceInfo{}
	for _, r := range next.Resources {
		resources[r.SHA256] = r
	}
	changed := []string{}
	for _, r := range c.DeleteResources {
		if _, ok := resources[r]; !ok {
			return dp.Snapshot{}, nil, dp.NewError(dp.NotFound)
		}
		delete(resources, r)
		changed = append(changed, r)
	}
	for _, r := range c.PutResources {
		if old, ok := resources[r.Info.SHA256]; ok {
			if old != r.Info {
				return dp.Snapshot{}, nil, dp.NewError(dp.InvalidSnapshot)
			}
		} else {
			resources[r.Info.SHA256] = r.Info
			changed = append(changed, r.Info.SHA256)
		}
	}
	next.Resources = []dp.ResourceInfo{}
	for _, r := range resources {
		next.Resources = append(next.Resources, r)
	}
	sort.Slice(next.Resources, func(i, j int) bool { return next.Resources[i].SHA256 < next.Resources[j].SHA256 })
	sort.Strings(changed)
	return next, changed, nil
}
func (d *DB) Update(ctx context.Context, scope dp.Scope, expected dp.Revision, actor dp.Actor, callback func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	ctx, cancel := dp.BoundedContext(ctx, dp.UpdateTimeout)
	defer cancel()
	if e := dp.ValidateScope(scope); e != nil {
		return dp.Commit{}, e
	}
	if expected.Scope != scope || callback == nil {
		return dp.Commit{}, dp.NewError(dp.InvalidRequest)
	}
	if e := dp.ValidateActor(actor); e != nil {
		return dp.Commit{}, e
	}
	if e := checkContext(ctx); e != nil {
		return dp.Commit{}, e
	}
	if e := d.admit(); e != nil {
		return dp.Commit{}, e
	}
	defer d.release()
	if e := d.lock(ctx); e != nil {
		return dp.Commit{}, e
	}
	defer d.unlock()
	tx, e := d.db.Bolt.Begin(true)
	if e != nil {
		return dp.Commit{}, safe(e)
	}
	defer tx.Rollback()
	b := tx.Bucket(scopes).Bucket(key(scope))
	if b == nil {
		return dp.Commit{}, dp.NewError(dp.NotFound)
	}
	old, _, e := load(b)
	if e != nil {
		return dp.Commit{}, e
	}
	revision, e := dp.NextRevision(scope, old.Revision, expected)
	if e != nil {
		return dp.Commit{}, e
	}
	c, e := callback(dp.CloneSnapshot(old))
	if e != nil {
		return dp.Commit{}, e
	}
	c = dp.CloneChangeSet(c)
	if e = checkContext(ctx); e != nil {
		return dp.Commit{}, e
	}
	if e = dp.ValidateChangeSet(c); e != nil {
		return dp.Commit{}, e
	}
	next, changed, e := apply(old, c)
	if e != nil {
		return dp.Commit{}, e
	}
	next.Revision = revision
	if e = dp.ValidateSnapshot(next); e != nil {
		return dp.Commit{}, e
	}
	staging, e := os.MkdirTemp(d.root, "stage-")
	if e != nil {
		return dp.Commit{}, safe(e)
	}
	defer os.RemoveAll(staging)
	paths := make([]string, len(c.PutResources))
	for i, input := range c.PutResources {
		paths[i] = filepath.Join(staging, hex.EncodeToString(number(uint64(i))))
		f, err := os.OpenFile(paths[i], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return dp.Commit{}, safe(err)
		}
		_, err = dp.CopyResource(ctx, f, input)
		ce := f.Close()
		if err == nil {
			err = ce
		}
		if err != nil {
			return dp.Commit{}, safe(err)
		}
	}
	_, e = dp.EncodeChangeSet(ctx, io.Discard, c, func(i int) (io.ReadCloser, error) { return os.Open(paths[i]) })
	if e != nil {
		return dp.Commit{}, safe(e)
	}
	payloads := b.Bucket([]byte("payloads"))
	for _, digest := range c.DeleteResources {
		if e = payloads.DeleteBucket([]byte(digest)); e != nil {
			return dp.Commit{}, safe(e)
		}
	}
	for i, input := range c.PutResources {
		if existing := payloads.Bucket([]byte(input.Info.SHA256)); existing != nil {
			// Digest and size alone do not establish equality in the contract's
			// hypothetical collision case. Compare the actual immutable bytes too.
			if e = verifyChunks(ctx, existing, input.Info); e != nil {
				return dp.Commit{}, e
			}
			f, err := os.Open(paths[i])
			if err != nil {
				return dp.Commit{}, safe(err)
			}
			buf := make([]byte, chunkSize)
			err = existing.ForEach(func(_, v []byte) error {
				if e := checkContext(ctx); e != nil {
					return e
				}
				n, e := io.ReadFull(f, buf[:len(v)])
				if e != nil || n != len(v) || !bytes.Equal(buf[:n], v) {
					return dp.NewError(dp.InvalidSnapshot)
				}
				return nil
			})
			if err == nil {
				var tail [1]byte
				n, re := f.Read(tail[:])
				if n != 0 || re != io.EOF {
					err = dp.NewError(dp.InvalidSnapshot)
				}
			}
			ce := f.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				return dp.Commit{}, safe(err)
			}
			continue
		}
		pb, err := payloads.CreateBucket([]byte(input.Info.SHA256))
		if err != nil {
			return dp.Commit{}, safe(err)
		}
		f, err := os.Open(paths[i])
		if err != nil {
			return dp.Commit{}, safe(err)
		}
		// Fixed chunks avoid a single 256MiB serialization. bbolt still retains all
		// dirty values/pages until Commit; qualification must measure that memory.
		var index uint64
		for {
			buf := make([]byte, chunkSize)
			n, re := io.ReadFull(f, buf)
			if n > 0 {
				err = pb.Put(number(index), buf[:n])
				index++
			}
			if err != nil || re != nil {
				if re != nil && re != io.EOF && re != io.ErrUnexpectedEOF {
					err = re
				}
				break
			}
			if err = checkContext(ctx); err != nil {
				break
			}
		}
		ce := f.Close()
		if err == nil {
			err = ce
		}
		if err != nil {
			return dp.Commit{}, safe(err)
		}
	}
	if e = d.point(ctx, "payload-membership"); e != nil {
		return dp.Commit{}, safe(e)
	}
	ids := changedElements(old.Elements, next.Elements)
	event := dp.Event{FormatVersion: dp.EventFormatVersion, Scope: scope, Revision: revision, Actor: actor, ChangedIDs: ids, ChangedResourceIDs: changed}
	if e = persist(b, next, &event); e != nil {
		return dp.Commit{}, safe(e)
	}
	if e = d.point(ctx, "before-commit"); e != nil {
		return dp.Commit{}, safe(e)
	}
	if e = checkContext(ctx); e != nil {
		return dp.Commit{}, e
	}
	// bbolt Commit performs dirty-page writes and fdatasync, then the metadata
	// page write and fdatasync. Any engine commit error is conservatively unknown.
	if e = tx.Commit(); e != nil {
		d.latch()
		return dp.Commit{}, dp.NewError(dp.CommitOutcomeUnknown)
	}
	if e = d.point(ctx, "after-commit"); e != nil {
		d.latch()
		return dp.Commit{}, dp.NewError(dp.CommitOutcomeUnknown)
	}
	return dp.Commit{Revision: revision, ChangedIDs: ids, ChangedResourceIDs: changed}, nil
}

// pin copies one coherent transaction into private files, then ends the read
// transaction. A caller-held stream cannot pin bbolt's mmap or block writers.
func (d *DB) pin(ctx context.Context, scope dp.Scope, only string) (dp.Snapshot, []dp.Event, dp.ResourceBacking, error) {
	var s dp.Snapshot
	var events []dp.Event
	dir, e := os.MkdirTemp(d.root, "pin-")
	if e != nil {
		return s, nil, dp.ResourceBacking{}, safe(e)
	}
	var releaseOnce sync.Once
	var releaseErr error
	backing := dp.ResourceBacking{Open: func(digest string) (io.ReadCloser, error) {
		if e := checkContext(ctx); e != nil {
			return nil, e
		}
		if dp.ValidateDigest(digest) != nil {
			return nil, dp.NewError(dp.InvalidRequest)
		}
		f, err := os.Open(filepath.Join(dir, digest))
		if err != nil {
			return nil, err
		}
		return &pinnedReader{ReadCloser: f, ctx: ctx}, nil
	}, Release: func() error {
		releaseOnce.Do(func() { releaseErr = os.RemoveAll(dir); d.release() })
		return releaseErr
	}}
	e = d.db.Bolt.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(scopes).Bucket(key(scope))
		if b == nil {
			return dp.NewError(dp.NotFound)
		}
		var err error
		s, events, err = load(b)
		if err != nil {
			return err
		}
		found := only == ""
		for _, r := range s.Resources {
			if only != "" && only != r.SHA256 {
				continue
			}
			found = true
			pb := b.Bucket([]byte("payloads")).Bucket([]byte(r.SHA256))
			if err = verifyChunks(ctx, pb, r); err != nil {
				return err
			}
			f, err := os.OpenFile(filepath.Join(dir, r.SHA256), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			err = pb.ForEach(func(_, v []byte) error {
				if e := checkContext(ctx); e != nil {
					return e
				}
				_, e := f.Write(v)
				return e
			})
			ce := f.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				return err
			}
		}
		if !found {
			return dp.NewError(dp.NotFound)
		}
		return checkContext(ctx)
	})
	if e != nil {
		_ = os.RemoveAll(dir)
		return dp.Snapshot{}, nil, dp.ResourceBacking{}, safe(e)
	}
	return s, events, backing, nil
}
func (d *DB) ReadResource(ctx context.Context, scope dp.Scope, digest string) (dp.ResourceReader, error) {
	ctx, finish := admitStream(ctx, dp.ReadTimeout)
	if e := dp.ValidateScope(scope); e != nil {
		finish()
		return dp.ResourceReader{}, e
	}
	if e := dp.ValidateDigest(digest); e != nil {
		finish()
		return dp.ResourceReader{}, e
	}
	if e := checkContext(ctx); e != nil {
		finish()
		return dp.ResourceReader{}, e
	}
	if e := d.admit(); e != nil {
		finish()
		return dp.ResourceReader{}, e
	}
	s, _, backing, e := d.pin(ctx, scope, digest)
	if e != nil {
		finish()
		d.release()
		return dp.ResourceReader{}, e
	}
	release := backing.Release
	backing.Release = func() error { defer finish(); return release() }
	for _, info := range s.Resources {
		if info.SHA256 == digest {
			return dp.OpenResourceReader(ctx, info, backing)
		}
	}
	_ = backing.Release()
	return dp.ResourceReader{}, dp.NewError(dp.StorageError)
}
func (d *DB) Checkpoint(ctx context.Context, scope dp.Scope) (dp.Checkpoint, error) {
	ctx, finish := admitStream(ctx, dp.CheckpointTimeout)
	if e := dp.ValidateScope(scope); e != nil {
		finish()
		return nil, e
	}
	if e := checkContext(ctx); e != nil {
		finish()
		return nil, e
	}
	if e := d.admit(); e != nil {
		finish()
		return nil, e
	}
	s, events, backing, e := d.pin(ctx, scope, "")
	if e != nil {
		finish()
		d.release()
		return nil, e
	}
	release := backing.Release
	backing.Release = func() error { defer finish(); return release() }
	return dp.OpenCheckpoint(ctx, s, events, backing)
}
func (d *DB) Close() error {
	timer := time.NewTimer(dp.CloseTimeout)
	defer timer.Stop()
	d.mu.Lock()
	if d.closed {
		e := d.closeErr
		d.mu.Unlock()
		return e
	}
	d.stopped = true
	if d.active == 0 {
		select {
		case <-d.drained:
		default:
			close(d.drained)
		}
	}
	d.mu.Unlock()
	select {
	case <-d.drained:
	case <-timer.C:
		return dp.NewError(dp.Unavailable)
	}
	d.finalOnce.Do(func() {
		go func() {
			e := d.point(context.Background(), "provider-close")
			ce := d.db.Close()
			if e == nil {
				e = ce
			}
			d.mu.Lock()
			d.closed = true
			d.closeErr = safe(e)
			d.mu.Unlock()
			close(d.finalDone)
		}()
	})
	select {
	case <-d.finalDone:
		d.mu.Lock()
		e := d.closeErr
		d.mu.Unlock()
		return e
	case <-timer.C:
		return dp.NewError(dp.Unavailable)
	}
}

var _ dp.Provider = (*DB)(nil)
var _ dp.Provisioner = (*DB)(nil)
