//go:build linux

package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"syscall"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func (p *Provider) Update(parent context.Context, s dp.Scope, expected dp.Revision, actor dp.Actor, callback func(dp.Snapshot) (dp.ChangeSet, error)) (dp.Commit, error) {
	ctx, cancel := dp.BoundedContext(parent, dp.UpdateTimeout)
	defer cancel()
	if dp.ValidateScope(s) != nil || expected.Scope != s || callback == nil || dp.ValidateActor(actor) != nil {
		return dp.Commit{}, fail(dp.InvalidRequest)
	}
	end, err := p.enter(ctx, s)
	if err != nil {
		return dp.Commit{}, err
	}
	defer end()
	old, err := p.load(ctx, s, true)
	if err != nil {
		return dp.Commit{}, err
	}
	next, err := dp.NextRevision(s, old.Snapshot.Revision, expected)
	if err != nil {
		return dp.Commit{}, err
	}
	cs, err := callback(dp.CloneSnapshot(old.Snapshot))
	if err != nil {
		return dp.Commit{}, err
	}
	if err := ctx.Err(); err != nil {
		return dp.Commit{}, dp.ContextError(err)
	}
	if err := dp.ValidateChangeSet(cs); err != nil {
		return dp.Commit{}, err
	}
	cs = dp.CloneChangeSet(cs)
	snap := dp.CloneSnapshot(old.Snapshot)
	snap.Revision = next
	elements := map[string]dp.Element{}
	for _, e := range snap.Elements {
		elements[e.ID] = e
	}
	ids := []string{}
	for _, id := range cs.Deletes {
		key := strings.ToUpper(id)
		if _, ok := elements[key]; !ok {
			return dp.Commit{}, fail(dp.NotFound)
		}
		delete(elements, key)
		ids = append(ids, key)
	}
	for _, e := range cs.Upserts {
		previous, exists := elements[e.ID]
		a, _ := json.Marshal(previous)
		b, _ := json.Marshal(e)
		if !exists || !bytes.Equal(a, b) {
			ids = append(ids, e.ID)
		}
		elements[e.ID] = dp.CloneElement(e)
	}
	snap.Elements = []dp.Element{}
	for _, e := range elements {
		snap.Elements = append(snap.Elements, e)
	}
	sort.Slice(snap.Elements, func(i, j int) bool { return snap.Elements[i].ID < snap.Elements[j].ID })
	if cs.Provenance != nil {
		snap.Provenance = cs.Provenance
		slices.SortFunc(snap.Provenance, func(a, b dp.ProvenanceFile) int { return strings.Compare(a.Path, b.Path) })
	}
	resources := map[string]dp.ResourceInfo{}
	for _, r := range snap.Resources {
		resources[r.SHA256] = r
	}
	resourceIDs := []string{}
	for _, d := range cs.DeleteResources {
		if _, ok := resources[d]; !ok {
			return dp.Commit{}, fail(dp.NotFound)
		}
		delete(resources, d)
		resourceIDs = append(resourceIDs, d)
	}
	for _, r := range cs.PutResources {
		if oldInfo, ok := resources[r.Info.SHA256]; ok {
			if oldInfo != r.Info {
				return dp.Commit{}, fail(dp.InvalidSnapshot)
			}
		} else {
			resourceIDs = append(resourceIDs, r.Info.SHA256)
		}
		resources[r.Info.SHA256] = r.Info
	}
	snap.Resources = []dp.ResourceInfo{}
	for _, r := range resources {
		snap.Resources = append(snap.Resources, r)
	}
	sort.Slice(snap.Resources, func(i, j int) bool { return snap.Resources[i].SHA256 < snap.Resources[j].SHA256 })
	if err := textFields(snap); err != nil {
		return dp.Commit{}, err
	}
	if err := dp.ValidateSnapshot(snap); err != nil {
		return dp.Commit{}, err
	}
	slices.Sort(ids)
	slices.Sort(resourceIDs)
	commit := dp.Commit{Revision: next, ChangedIDs: ids, ChangedResourceIDs: resourceIDs}
	events := append(dp.CloneEvents(old.Events), dp.Event{FormatVersion: dp.EventFormatVersion, Scope: s, Revision: next, Actor: actor, ChangedIDs: ids, ChangedResourceIDs: resourceIDs})
	if err := dp.ValidateEvents(snap, events); err != nil {
		return dp.Commit{}, err
	}
	if err := p.commit(ctx, old, snap, events, cs); err != nil {
		return dp.Commit{}, err
	}
	return commit, nil
}

func (p *Provider) journal(id string, i intent) error {
	name := "transactions/" + id + ".json"
	err := p.writeJSON(name, i)
	if err == nil {
		err = p.syncDir("transactions")
	}
	if err == nil {
		return nil
	}
	// No generation write has started. A failed intent write can leave an
	// empty/truncated file (notably on ENOSPC); remove only this known attempt
	// and sync its removal before admitting a retry. Never discard other evidence.
	cleanup := p.root.Remove(name)
	if errors.Is(cleanup, os.ErrNotExist) {
		cleanup = nil
	}
	if cleanup == nil {
		cleanup = p.syncDir("transactions")
	}
	if cleanup != nil {
		p.latch(i.Scope)
	}
	return err
}
func (p *Provider) provision(ctx context.Context, snap dp.Snapshot) (result error) {
	id, err := token()
	if err != nil {
		return err
	}
	gen := "gen-" + id
	i := intent{Scope: snap.Board.Scope, New: gen, Provision: true}
	if err := p.journal(id, i); err != nil {
		return err
	}
	stage := "staging/" + id
	decided := false
	defer func() {
		if !decided {
			cleanup := p.root.RemoveAll(stage)
			if cleanup == nil {
				cleanup = p.syncDir("staging")
			}
			if cleanup == nil {
				cleanup = p.root.Remove("transactions/" + id + ".json")
			}
			if cleanup == nil {
				cleanup = p.syncDir("transactions")
			}
			if cleanup != nil {
				p.latch(snap.Board.Scope)
				if result == nil {
					result = storage(cleanup)
				}
			}
		}
	}()
	if err := p.root.Mkdir(stage, 0700); err != nil {
		return storage(err)
	}
	if err := p.writeJSON(stage+"/scope.json", snap.Board); err != nil {
		return err
	}
	ptr, err := p.generation(ctx, stage, gen, diskState{}, snap, []dp.Event{}, dp.ChangeSet{})
	if err != nil {
		return err
	}
	if err := p.writeJSON(stage+"/CURRENT", ptr); err != nil {
		return err
	}
	if err := p.syncDir(stage); err != nil {
		return err
	}
	product := "scopes/" + snap.Board.Scope.ProductID
	if err := p.root.Mkdir(product, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return storage(err)
	}
	if err := p.checkDir(product); err != nil {
		return err
	}
	if err := p.syncDir("scopes"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return dp.ContextError(err)
	}
	if err := p.root.Rename(stage, scopePath(snap.Board.Scope)); err != nil {
		return storage(err)
	}
	decided = true
	if err := p.syncDir(product); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.root.Remove("transactions/" + id + ".json"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.syncDir("transactions"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	return nil
}

func (p *Provider) commit(ctx context.Context, old diskState, snap dp.Snapshot, events []dp.Event, cs dp.ChangeSet) (result error) {
	id, err := token()
	if err != nil {
		return err
	}
	gen := "gen-" + id
	dir := scopePath(snap.Board.Scope)
	i := intent{Scope: snap.Board.Scope, Old: &old.Pointer, New: gen}
	if err := p.journal(id, i); err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			// A known predecision refusal removes only its private uncommitted generation.
			cleanup := p.root.RemoveAll(dir + "/" + gen)
			if e := p.root.Remove(dir + "/.next-" + id); cleanup == nil && !errors.Is(e, os.ErrNotExist) {
				cleanup = e
			}
			if cleanup == nil {
				cleanup = p.syncDir(dir)
			}
			if cleanup == nil {
				cleanup = p.root.Remove("transactions/" + id + ".json")
			}
			if cleanup == nil {
				cleanup = p.syncDir("transactions")
			}
			if cleanup != nil {
				p.latch(snap.Board.Scope)
				if result == nil {
					result = storage(cleanup)
				}
			}
		}
	}()
	if err := p.hit(ctx, "journal_sync"); err != nil {
		return err
	}
	ptr, err := p.generation(ctx, dir, gen, old, snap, events, cs)
	if err != nil {
		return err
	}
	if err := p.writeJSON(dir+"/.next-"+id, ptr); err != nil {
		return err
	}
	if err := p.syncDir(dir); err != nil {
		return err
	}
	if err := p.hit(ctx, "before_rename"); err != nil {
		return err
	}
	if err := p.root.Rename(dir+"/.next-"+id, dir+"/CURRENT"); err != nil {
		return storage(err)
	}
	renamed = true
	// Rename visibility precedes durability. Any failure from here until the
	// parent sync is ambiguous; evidence is retained and admission is latched.
	if err := p.hit(ctx, "renamed"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.syncDir(dir); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.hit(ctx, "parent_sync"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	// The generation and CURRENT are now durably decided. Cleanup is idempotent.
	if err := p.hit(ctx, "cleanup"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.root.Remove("transactions/" + id + ".json"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.syncDir("transactions"); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	if err := p.sweep(dir, gen); err != nil {
		p.latch(snap.Board.Scope)
		return fail(dp.CommitOutcomeUnknown)
	}
	return nil
}
func (p *Provider) generation(ctx context.Context, dir, gen string, old diskState, snap dp.Snapshot, events []dp.Event, cs dp.ChangeSet) (pointer, error) {
	location := dir + "/" + gen
	if err := p.root.Mkdir(location, 0700); err != nil {
		return pointer{}, storage(err)
	}
	if err := p.root.Mkdir(location+"/resources", 0700); err != nil {
		return pointer{}, storage(err)
	}
	var snapshot bytes.Buffer
	if _, err := dp.EncodeSnapshot(&snapshot, snap); err != nil {
		return pointer{}, err
	}
	if err := p.write(location+"/snapshot.json", snapshot.Bytes()); err != nil {
		return pointer{}, err
	}
	if err := p.hit(ctx, "first_write"); err != nil {
		return pointer{}, err
	}
	// Consume every original reader exactly once in put-list order. Identical
	// existing puts are still verified; validated staging supplies size encoding.
	putFiles := map[string]string{}
	for _, r := range cs.PutResources {
		name := location + "/resources/" + r.Info.SHA256
		if err := p.copyInput(ctx, name, r); err != nil {
			return pointer{}, err
		}
		for _, existing := range old.Snapshot.Resources {
			if existing.SHA256 == r.Info.SHA256 {
				if err := p.equalFiles(ctx, old.Dir+"/resources/"+existing.SHA256, name); err != nil {
					return pointer{}, err
				}
				break
			}
		}
		putFiles[r.Info.SHA256] = name
	}
	for _, r := range snap.Resources {
		if _, ok := putFiles[r.SHA256]; ok {
			continue
		}
		source, err := p.safeFile(old.Dir + "/resources/" + r.SHA256)
		if err != nil {
			return pointer{}, err
		}
		err = p.copyInput(ctx, location+"/resources/"+r.SHA256, dp.ResourceInput{Info: r, Reader: source})
		ce := source.Close()
		if err == nil {
			err = storage(ce)
		}
		if err != nil {
			return pointer{}, err
		}
	}
	if err := p.syncDir(location + "/resources"); err != nil {
		return pointer{}, err
	}
	if err := p.hit(ctx, "resource_payload"); err != nil {
		return pointer{}, err
	}
	// Canonical command size uses only private staged readers, never original inputs.
	if _, err := dp.EncodeChangeSet(ctx, io.Discard, cs, func(n int) (io.ReadCloser, error) { return p.safeFile(putFiles[cs.PutResources[n].Info.SHA256]) }); err != nil {
		return pointer{}, err
	}
	if err := p.hit(ctx, "resource_membership"); err != nil {
		return pointer{}, err
	}
	ef, err := p.root.OpenFile(location+"/events.ndjson", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return pointer{}, storage(err)
	}
	h := sha256.New()
	w := io.MultiWriter(ef, h)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			ef.Close()
			return pointer{}, dp.ContextError(err)
		}
		if _, err := dp.EncodeEvent(w, event); err != nil {
			ef.Close()
			return pointer{}, err
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			ef.Close()
			return pointer{}, storage(err)
		}
	}
	err = ef.Chmod(0400)
	if err == nil {
		err = ef.Sync()
	}
	if ce := ef.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return pointer{}, storage(err)
	}
	if err := p.hit(ctx, "revision_event"); err != nil {
		return pointer{}, err
	}
	env := envelope{Scope: snap.Board.Scope, SnapshotSHA256: hash(snapshot.Bytes()), EventsSHA256: hex.EncodeToString(h.Sum(nil))}
	eb, _ := json.Marshal(env)
	if err := p.write(location+"/envelope.json", eb); err != nil {
		return pointer{}, err
	}
	if err := p.syncDir(location); err != nil {
		return pointer{}, err
	}
	if err := p.hit(ctx, "last_write"); err != nil {
		return pointer{}, err
	}
	return pointer{Generation: gen, SHA256: hash(eb)}, nil
}
func (p *Provider) copyInput(ctx context.Context, name string, input dp.ResourceInput) error {
	f, err := p.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return storage(err)
	}
	_, err = dp.CopyResource(ctx, f, input)
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

// Even a hypothetical equal-digest collision must not replace existing bytes.
func (p *Provider) equalFiles(ctx context.Context, left, right string) error {
	a, err := p.safeFile(left)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := p.safeFile(right)
	if err != nil {
		return err
	}
	defer b.Close()
	x, y := make([]byte, 32768), make([]byte, 32768)
	for {
		if err := ctx.Err(); err != nil {
			return dp.ContextError(err)
		}
		n, ae := a.Read(x)
		m, be := b.Read(y)
		if n != m || !bytes.Equal(x[:n], y[:m]) {
			return fail(dp.InvalidSnapshot)
		}
		if ae == io.EOF && be == io.EOF {
			return nil
		}
		if ae != nil && ae != io.EOF || be != nil && be != io.EOF {
			return fail(dp.StorageError)
		}
		if ae != be {
			return fail(dp.InvalidSnapshot)
		}
	}
}
