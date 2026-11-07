//go:build linux

package fs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"syscall"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

const schemaBytes = `{"format":"scoped-filesystem","version":1}`

var generationName = regexp.MustCompile(`^gen-[a-f0-9]{32}$`)
var transactionName = regexp.MustCompile(`^[a-f0-9]{32}\.json$`)

type pointer struct {
	Generation string `json:"generation"`
	SHA256     string `json:"sha256"`
}
type envelope struct {
	Scope          dp.Scope `json:"scope"`
	SnapshotSHA256 string   `json:"snapshot_sha256"`
	EventsSHA256   string   `json:"events_sha256"`
}
type diskState struct {
	Snapshot dp.Snapshot
	Events   []dp.Event
	Pointer  pointer
	Dir      string
}
type intent struct {
	Scope     dp.Scope `json:"scope"`
	Old       *pointer `json:"old"`
	New       string   `json:"new"`
	Provision bool     `json:"provision"`
}

func scopePath(s dp.Scope) string { return "scopes/" + s.ProductID + "/" + s.BoardID }
func token() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), storage(err)
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func privateRegular(f *os.File, mode os.FileMode) error {
	st, err := f.Stat()
	if err != nil {
		return storage(err)
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != mode || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 {
		return fail(dp.StorageError)
	}
	return nil
}
func (p *Provider) entries(dir string) ([]os.DirEntry, error) {
	f, err := p.root.Open(dir)
	if err != nil {
		return nil, storage(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.IsDir() {
		return nil, fail(dp.StorageError)
	}
	out, err := f.ReadDir(-1)
	return out, storage(err)
}
func (p *Provider) safeFile(name string) (*os.File, error) {
	st, err := p.root.Lstat(name)
	if err != nil {
		return nil, storage(err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0400 {
		return nil, fail(dp.StorageError)
	}
	f, err := p.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, storage(err)
	}
	if err := privateRegular(f, 0400); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (p *Provider) read(name string, max int64) ([]byte, error) {
	f, err := p.safeFile(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > max {
		return nil, fail(dp.StorageError)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, fail(dp.StorageError)
	}
	return b, nil
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fail(dp.StorageError)
	}
	if d.Decode(new(any)) != io.EOF {
		return fail(dp.StorageError)
	}
	// Canonical re-encoding also rejects duplicate keys, whitespace and aliases.
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(canonical, b) {
		return fail(dp.StorageError)
	}
	return nil
}
func (p *Provider) write(name string, b []byte) error {
	f, err := p.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return storage(err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0400)
	}
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	return storage(err)
}
func (p *Provider) writeJSON(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fail(dp.StorageError)
	}
	return p.write(name, b)
}
func (p *Provider) syncDir(name string) error {
	f, err := p.root.Open(name)
	if err != nil {
		return storage(err)
	}
	err = f.Sync()
	if ce := f.Close(); err == nil {
		err = ce
	}
	return storage(err)
}
func (p *Provider) schema() error {
	b, err := p.read("schema.json", 1024)
	if err != nil {
		return fail(dp.UnsupportedSchema)
	}
	if string(b) != schemaBytes {
		return fail(dp.UnsupportedSchema)
	}
	return nil
}
func (p *Provider) checkDir(name string) error {
	st, err := p.root.Lstat(name)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return fail(dp.StorageError)
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) {
		return fail(dp.StorageError)
	}
	return nil
}
func (p *Provider) load(ctx context.Context, s dp.Scope, verify bool) (diskState, error) {
	state, err := p.loadAt(ctx, s, scopePath(s), verify)
	if code, ok := dp.CodeOf(err); ok && (code == dp.StorageError || code == dp.UnsupportedSchema) {
		p.latch(s)
	}
	return state, err
}
func (p *Provider) loadAt(ctx context.Context, s dp.Scope, dir string, verify bool) (diskState, error) {
	return p.loadAtPointer(ctx, s, dir, verify, nil)
}
func (p *Provider) loadAtPointer(ctx context.Context, s dp.Scope, dir string, verify bool, selected *pointer) (diskState, error) {
	var out diskState
	if err := ctx.Err(); err != nil {
		return out, dp.ContextError(err)
	}
	if _, err := p.root.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return out, fail(dp.NotFound)
	}
	// No symlink in either scoped path component may participate in resolution.
	if strings.HasPrefix(dir, "scopes/") {
		if err := p.checkDir(path.Dir(dir)); err != nil {
			return out, err
		}
	}
	if err := p.checkDir(dir); err != nil {
		return out, err
	}
	b, err := p.read(dir+"/scope.json", dp.MaxSnapshotBytes)
	if err != nil {
		return out, err
	}
	var board dp.Board
	if err := decode(b, &board); err != nil {
		return out, err
	}
	if board.Scope != s {
		return out, fail(dp.StorageError)
	}
	b, err = p.read(dir+"/CURRENT", 1024)
	if err != nil {
		return out, err
	}
	if err := decode(b, &out.Pointer); err != nil {
		return out, err
	}
	if selected != nil {
		out.Pointer = *selected
	}
	if !generationName.MatchString(out.Pointer.Generation) || dp.ValidateDigest(out.Pointer.SHA256) != nil {
		return out, fail(dp.StorageError)
	}
	out.Dir = dir + "/" + out.Pointer.Generation
	if err := p.checkDir(out.Dir); err != nil {
		return out, err
	}
	b, err = p.read(out.Dir+"/envelope.json", 1024)
	if err != nil {
		return out, err
	}
	if hash(b) != out.Pointer.SHA256 {
		return out, fail(dp.StorageError)
	}
	var env envelope
	if err := decode(b, &env); err != nil {
		return out, err
	}
	if env.Scope != s {
		return out, fail(dp.StorageError)
	}
	b, err = p.read(out.Dir+"/snapshot.json", dp.MaxSnapshotBytes)
	if err != nil {
		return out, err
	}
	if hash(b) != env.SnapshotSHA256 {
		return out, fail(dp.StorageError)
	}
	if err := decode(b, &out.Snapshot); err != nil {
		return out, err
	}
	if out.Snapshot.Board != board || dp.ValidateSnapshot(out.Snapshot) != nil {
		return out, fail(dp.StorageError)
	}
	f, err := p.safeFile(out.Dir + "/events.ndjson")
	if err != nil {
		return out, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > dp.MaxEventNDJSONBytes {
		return out, fail(dp.StorageError)
	}
	if st.Size() > 0 {
		var tail [1]byte
		if _, err := f.ReadAt(tail[:], st.Size()-1); err != nil || tail[0] != '\n' {
			return out, fail(dp.StorageError)
		}
	}
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, h))
	scanner.Buffer(make([]byte, 4096), int(dp.MaxEventNDJSONBytes))
	out.Events = []dp.Event{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return out, dp.ContextError(err)
		}
		var e dp.Event
		if err := decode(scanner.Bytes(), &e); err != nil {
			return out, err
		}
		out.Events = append(out.Events, e)
	}
	if scanner.Err() != nil || hex.EncodeToString(h.Sum(nil)) != env.EventsSHA256 || dp.ValidateEvents(out.Snapshot, out.Events) != nil {
		return out, fail(dp.StorageError)
	}
	// Exact names prevent tolerant admission or silent dropping of extra facts.
	files, err := p.entries(out.Dir)
	if err != nil {
		return out, err
	}
	names := []string{}
	for _, e := range files {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"envelope.json", "events.ndjson", "resources", "snapshot.json"}) {
		return out, fail(dp.StorageError)
	}
	if err := p.checkDir(out.Dir + "/resources"); err != nil {
		return out, err
	}
	files, err = p.entries(out.Dir + "/resources")
	if err != nil {
		return out, err
	}
	if len(files) != len(out.Snapshot.Resources) {
		return out, fail(dp.StorageError)
	}
	catalog := map[string]dp.ResourceInfo{}
	for _, r := range out.Snapshot.Resources {
		catalog[r.SHA256] = r
	}
	for _, entry := range files {
		info, ok := catalog[entry.Name()]
		if !ok {
			return out, fail(dp.StorageError)
		}
		r, err := p.safeFile(out.Dir + "/resources/" + info.SHA256)
		if err != nil {
			return out, err
		}
		if verify {
			_, err = dp.CopyResource(ctx, io.Discard, dp.ResourceInput{Info: info, Reader: r})
		}
		if ce := r.Close(); err == nil {
			err = ce
		}
		if err != nil {
			return out, fail(dp.StorageError)
		}
	}
	return out, nil
}

func (p *Provider) recover(ctx context.Context) error {
	if err := p.schema(); err != nil {
		return err
	}
	entries, err := p.entries(".")
	if err != nil {
		return err
	}
	allowed := map[string]bool{"schema.json": true, "owner.lock": true, "scopes": true, "transactions": true, "staging": true}
	if len(entries) != len(allowed) {
		return fail(dp.StorageError)
	}
	for _, e := range entries {
		if !allowed[e.Name()] {
			return fail(dp.StorageError)
		}
	}
	for _, dir := range []string{"scopes", "transactions", "staging"} {
		if err := p.checkDir(dir); err != nil {
			return err
		}
	}
	journals, err := p.entries("transactions")
	if err != nil {
		return err
	}
	// Validate every recovery decision before touching any retained evidence.
	type recovery struct {
		name      string
		i         intent
		committed bool
	}
	plans := []recovery{}
	byScope := map[dp.Scope]intent{}
	byID := map[string]intent{}
	for _, entry := range journals {
		if !transactionName.MatchString(entry.Name()) {
			return fail(dp.StorageError)
		}
		b, err := p.read("transactions/"+entry.Name(), 4096)
		if err != nil {
			return err
		}
		var i intent
		if err := decode(b, &i); err != nil {
			return err
		}
		if dp.ValidateScope(i.Scope) != nil || !generationName.MatchString(i.New) {
			return fail(dp.StorageError)
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if i.New != "gen-"+id || i.Provision != (i.Old == nil) {
			return fail(dp.StorageError)
		}
		if i.Old != nil && (!generationName.MatchString(i.Old.Generation) || dp.ValidateDigest(i.Old.SHA256) != nil) {
			return fail(dp.StorageError)
		}
		if _, exists := byScope[i.Scope]; exists {
			return fail(dp.StorageError)
		}
		byScope[i.Scope], byID[id] = i, i
		dir := scopePath(i.Scope)
		b, err = p.read(dir+"/CURRENT", 1024)
		committed := false
		if err == nil {
			var current pointer
			if decode(b, &current) != nil {
				return fail(dp.StorageError)
			}
			if current.Generation == i.New {
				if _, err := p.load(ctx, i.Scope, true); err != nil {
					return err
				}
				committed = true
			} else if i.Old == nil || current != *i.Old {
				return fail(dp.StorageError)
			}
		} else if !i.Provision {
			return fail(dp.StorageError)
		} else if _, e := p.root.Lstat(dir); !errors.Is(e, os.ErrNotExist) {
			return fail(dp.StorageError)
		}
		plans = append(plans, recovery{entry.Name(), i, committed})
	}
	staging, err := p.entries("staging")
	if err != nil {
		return err
	}
	for _, entry := range staging {
		i, exists := byID[entry.Name()]
		if !exists || !i.Provision {
			return fail(dp.StorageError)
		}
		if err := p.safeScratch(ctx, "staging/"+entry.Name(), 0); err != nil {
			return err
		}
	}
	products, err := p.entries("scopes")
	if err != nil {
		return err
	}
	for _, product := range products {
		if dp.ValidateScope(dp.Scope{ProductID: product.Name(), BoardID: "valid"}) != nil {
			return fail(dp.StorageError)
		}
		pd := "scopes/" + product.Name()
		if err := p.checkDir(pd); err != nil {
			return err
		}
		boards, err := p.entries(pd)
		if err != nil {
			return err
		}
		for _, board := range boards {
			s := dp.Scope{ProductID: product.Name(), BoardID: board.Name()}
			if dp.ValidateScope(s) != nil {
				return fail(dp.StorageError)
			}
			state, err := p.load(ctx, s, true)
			if err != nil {
				return err
			}
			if err := p.auditScope(ctx, s, state.Pointer.Generation, byScope[s]); err != nil {
				return err
			}
		}
	}
	if err := p.hit(ctx, "recovery"); err != nil {
		return err
	}
	for _, plan := range plans {
		i := plan.i
		if !plan.committed && !i.Provision {
			if err := p.root.RemoveAll(scopePath(i.Scope) + "/" + i.New); err != nil {
				return storage(err)
			}
			if err := p.root.Remove(scopePath(i.Scope) + "/.next-" + strings.TrimSuffix(plan.name, ".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return storage(err)
			}
			if err := p.syncDir(scopePath(i.Scope)); err != nil {
				return err
			}
		}
		if err := p.root.RemoveAll("staging/" + strings.TrimSuffix(plan.name, ".json")); err != nil {
			return storage(err)
		}
		if err := p.syncDir("staging"); err != nil {
			return err
		}
		if err := p.root.Remove("transactions/" + plan.name); err != nil {
			return storage(err)
		}
	}
	if err := p.syncDir("transactions"); err != nil {
		return err
	}
	// A staging tree without its durable intent is ambiguous; retain it.
	staging, err = p.entries("staging")
	if err != nil {
		return err
	}
	if len(staging) != 0 {
		return fail(dp.StorageError)
	}
	for _, product := range products {
		boards, _ := p.entries("scopes/" + product.Name())
		for _, board := range boards {
			s := dp.Scope{ProductID: product.Name(), BoardID: board.Name()}
			state, err := p.load(ctx, s, true)
			if err != nil {
				return err
			}
			if err := p.sweep(scopePath(s), state.Pointer.Generation); err != nil {
				return err
			}
		}
	}
	return p.syncDir("staging")
}
func (p *Provider) sweep(dir, current string) error {
	entries, err := p.entries(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "scope.json" || name == "CURRENT" || name == current {
			continue
		}
		if !generationName.MatchString(name) && !strings.HasPrefix(name, ".next-") {
			return fail(dp.StorageError)
		}
		if err := p.root.RemoveAll(dir + "/" + name); err != nil {
			return storage(err)
		}
	}
	return p.syncDir(dir)
}

// Unpublished scratch is removable only with its exact durable intent. Refuse
// aliases/special files before cleanup; never follow a recovery-evidence link.
func (p *Provider) safeScratch(ctx context.Context, name string, depth int) error {
	if err := ctx.Err(); err != nil {
		return dp.ContextError(err)
	}
	if depth > 4 {
		return fail(dp.StorageError)
	}
	st, err := p.root.Lstat(name)
	if err != nil {
		return storage(err)
	}
	if st.IsDir() {
		if err := p.checkDir(name); err != nil {
			return err
		}
		entries, err := p.entries(name)
		if err != nil {
			return err
		}
		if len(entries) > dp.MaxResourceCount+8 {
			return fail(dp.StorageError)
		}
		for _, entry := range entries {
			if err := p.safeScratch(ctx, name+"/"+entry.Name(), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || s.Nlink != 1 || s.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0400 && st.Mode().Perm() != 0600 {
		return fail(dp.StorageError)
	}
	return nil
}
func (p *Provider) auditScope(ctx context.Context, s dp.Scope, current string, pending intent) error {
	dir := scopePath(s)
	entries, err := p.entries(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "CURRENT" || name == "scope.json" || name == current {
			continue
		}
		if name == pending.New && pending.New != "" {
			if err := p.safeScratch(ctx, dir+"/"+name, 0); err != nil {
				return err
			}
			continue
		}
		if pending.New != "" && name == ".next-"+strings.TrimPrefix(pending.New, "gen-") {
			if err := p.safeScratch(ctx, dir+"/"+name, 0); err != nil {
				return err
			}
			continue
		}
		if !generationName.MatchString(name) {
			return fail(dp.StorageError)
		}
		b, err := p.read(dir+"/"+name+"/envelope.json", 1024)
		if err != nil {
			return err
		}
		selected := pointer{Generation: name, SHA256: hash(b)}
		if _, err := p.loadAtPointer(ctx, s, dir, true, &selected); err != nil {
			return err
		}
	}
	return nil
}
