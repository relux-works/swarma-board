// Package store implements schema-v1 project storage using Storm's bbolt handle.
package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

var projectID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidProjectID(id string) bool { return projectID.MatchString(id) }

var meta = []byte("board-v1")
var records = []byte("elements")
var provenance = []byte("provenance")
var audits = []byte("audit")

func failure(code string) error {
	return &contract.Error{Code: code, Message: strings.ReplaceAll(code, "_", " ")}
}

// beforeCommit is an unexported fault hook used only by same-package crash tests.
// Constructors leave it nil; no request, fixture or public API can enable it.
type DB struct {
	db           *storm.DB
	project      contract.Project
	beforeCommit func() error
}
type AuditRecord struct {
	Revision   contract.Revision
	Context    contract.AuditContext
	ChangedIDs []string
}

// Open never initializes an existing file. A legacy or incomplete file refuses.
func Open(path string, project contract.Project, create bool) (*DB, error) {
	if !ValidProjectID(project.ID) {
		return nil, failure("invalid_request")
	}
	st, err := os.Lstat(path)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return nil, err
	}
	if fresh && !create {
		return nil, failure("not_found")
	}
	if !fresh && (!st.Mode().IsRegular() || st.Mode().Perm() != 0600) {
		return nil, failure("unavailable")
	}
	// Reserve a new private file with O_EXCL, so opening can never adopt a raced file.
	if fresh {
		f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	raw, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	// Validate our schema before Storm can initialize its own library metadata.
	d := &DB{db: &storm.DB{Bolt: raw}, project: project}
	if fresh {
		err = raw.Update(func(tx *bolt.Tx) error {
			for _, name := range [][]byte{meta, records, provenance, audits} {
				if _, e := tx.CreateBucket(name); e != nil {
					return e
				}
			}
			b := tx.Bucket(meta)
			if e := b.Put([]byte("schema"), u64(1)); e != nil {
				return e
			}
			p, e := json.Marshal(project)
			if e != nil {
				return e
			}
			if e = b.Put([]byte("project"), p); e != nil {
				return e
			}
			return b.Put([]byte("revision"), u64(0))
		})
	}
	if err == nil {
		err = d.Ready(context.Background())
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	db, err := storm.Open(path, storm.UseDB(raw))
	if err != nil {
		raw.Close()
		return nil, err
	}
	d.db = db
	return d, nil
}
func u64(n uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, n); return b }
func (d *DB) metadata(tx *bolt.Tx) (contract.Project, contract.Revision, error) {
	var p contract.Project
	b := tx.Bucket(meta)
	if b == nil || len(b.Get([]byte("schema"))) != 8 || binary.BigEndian.Uint64(b.Get([]byte("schema"))) != 1 {
		return p, 0, failure("unavailable")
	}
	r := b.Get([]byte("revision"))
	if len(r) != 8 {
		return p, 0, failure("unavailable")
	}
	if json.Unmarshal(b.Get([]byte("project")), &p) != nil || p.ID != d.project.ID {
		return p, 0, failure("unavailable")
	}
	for _, name := range [][]byte{records, provenance, audits} {
		if tx.Bucket(name) == nil {
			return p, 0, failure("unavailable")
		}
	}
	return p, contract.Revision(binary.BigEndian.Uint64(r)), nil
}
func (d *DB) snapshot(tx *bolt.Tx) (contract.Snapshot, error) {
	p, r, e := d.metadata(tx)
	s := contract.Snapshot{Project: p, Revision: r, Elements: []contract.Element{}, Provenance: []contract.ProvenanceFile{}}
	if e != nil {
		return s, e
	}
	e = tx.Bucket(records).ForEach(func(k, v []byte) error {
		var x contract.Element
		if json.Unmarshal(v, &x) != nil || x.ID != string(k) {
			return failure("storage_error")
		}
		s.Elements = append(s.Elements, x)
		return nil
	})
	if e != nil {
		return s, e
	}
	e = tx.Bucket(provenance).ForEach(func(k, v []byte) error {
		var x contract.ProvenanceFile
		if json.Unmarshal(v, &x) != nil || x.Path != string(k) {
			return failure("storage_error")
		}
		s.Provenance = append(s.Provenance, x)
		return nil
	})
	return s, e
}
func (d *DB) View(ctx context.Context, id string) (s contract.Snapshot, err error) {
	if id != d.project.ID {
		return s, failure("not_found")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	err = d.db.Bolt.View(func(tx *bolt.Tx) error {
		var e error
		s, e = d.snapshot(tx)
		if e != nil {
			return e
		}
		return ctx.Err()
	})
	return
}
func (d *DB) Update(ctx context.Context, id string, expected contract.Revision, audit contract.AuditContext, fn func(contract.Snapshot) (contract.ChangeSet, error)) (out contract.Commit, err error) {
	if id != d.project.ID {
		return out, failure("not_found")
	}
	if fn == nil {
		return out, failure("invalid_request")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	err = d.db.Bolt.Update(func(tx *bolt.Tx) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		s, e := d.snapshot(tx)
		if e != nil {
			return e
		}
		if s.Revision != expected {
			return &contract.Error{Code: "revision_conflict", Message: "revision conflict", Details: map[string]any{"current_revision": s.Revision}}
		}
		if s.Revision == contract.Revision(math.MaxUint64) {
			return failure("unavailable")
		}
		if e = appendPosition(tx, s.Revision); e != nil {
			return e
		}
		// JSON decoding detached every nested slice and pointer from the database.
		existing := map[string]string{}
		for _, x := range s.Elements {
			existing[strings.ToLower(x.ID)] = x.ID
		}
		originalCount := len(s.Elements)
		changes, e := fn(s)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		ids := []string{}
		for _, x := range changes.Upserts {
			key := strings.ToLower(x.ID)
			if x.ID == "" || seen[key] || (existing[key] != "" && existing[key] != x.ID) {
				return failure("invalid_request")
			}
			seen[key] = true
			ids = append(ids, x.ID)
		}
		for _, id := range changes.Deletes {
			key := strings.ToLower(id)
			if id == "" || seen[key] {
				return failure("invalid_request")
			}
			if existing[key] != id {
				return failure("not_found")
			}
			seen[key] = true
			ids = append(ids, id)
		}
		n := originalCount - len(changes.Deletes)
		for _, x := range changes.Upserts {
			if existing[strings.ToLower(x.ID)] == "" {
				n++
			}
		}
		if n > 10000 {
			return failure("invalid_snapshot")
		}
		for _, x := range changes.Upserts {
			v, e := json.Marshal(x)
			if e != nil {
				return e
			}
			if e = tx.Bucket(records).Put([]byte(x.ID), v); e != nil {
				return e
			}
		}
		for _, id := range changes.Deletes {
			if e := tx.Bucket(records).Delete([]byte(id)); e != nil {
				return e
			}
		}
		if changes.Provenance != nil {
			seenPaths := map[string]bool{}
			for _, p := range changes.Provenance {
				if p.Path == "" || seenPaths[p.Path] {
					return failure("invalid_snapshot")
				}
				seenPaths[p.Path] = true
			}
			if e = tx.DeleteBucket(provenance); e != nil {
				return e
			}
			b, e := tx.CreateBucket(provenance)
			if e != nil {
				return e
			}
			for _, p := range changes.Provenance {
				v, e := json.Marshal(p)
				if e != nil {
					return e
				}
				if e = b.Put([]byte(p.Path), v); e != nil {
					return e
				}
			}
		}
		sort.Strings(ids)
		next := s.Revision + 1
		v, e := json.Marshal(AuditRecord{next, audit, ids})
		if e != nil {
			return e
		}
		if e = tx.Bucket(audits).Put(u64(uint64(next)), v); e != nil {
			return e
		}
		if e = tx.Bucket(meta).Put([]byte("revision"), u64(uint64(next))); e != nil {
			return e
		}
		if d.beforeCommit != nil {
			if e = d.beforeCommit(); e != nil {
				return e
			}
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		out = contract.Commit{Revision: next, ChangedIDs: ids}
		return nil
	})
	if err != nil {
		return contract.Commit{}, err
	}
	return out, nil
}
func (d *DB) Ready(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return d.db.Bolt.View(func(tx *bolt.Tx) error { _, _, e := d.metadata(tx); return e })
}
func (d *DB) Close() error { return d.db.Close() }
func (d *DB) Audit(ctx context.Context) (out []AuditRecord, err error) {
	err = d.db.Bolt.View(func(tx *bolt.Tx) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		return tx.Bucket(audits).ForEach(func(_, v []byte) error {
			var a AuditRecord
			if e := json.Unmarshal(v, &a); e != nil {
				return e
			}
			out = append(out, a)
			return nil
		})
	})
	return
}

// Backup copies a single committed read transaction, including metadata and audit.
func (d *DB) Backup(ctx context.Context, w io.Writer) error {
	return d.db.Bolt.View(func(tx *bolt.Tx) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		_, e := tx.WriteTo(w)
		return e
	})
}

// Restore creates a new destination only; callers must use an isolated offline directory.
func Restore(ctx context.Context, path string, p contract.Project, r io.Reader) error {
	st, e := os.Lstat(filepath.Dir(path))
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return failure("unavailable")
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	lock, e := LockDirectory(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer lock.Close()
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, e = io.Copy(f, r); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	d, e := Open(path, p, false)
	if e != nil {
		return fmt.Errorf("restore validation: %w", e)
	}
	e = d.Close()
	if e != nil {
		return e
	}
	ok = true
	return nil
}

var _ contract.Repository = (*DB)(nil)
