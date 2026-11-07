//go:build linux

package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

const projectionLayout = "id-rank-pairs/1"
const projectionHeader = "Generated view; snapshot.json is authoritative. Native display defaults are not stored facts.\n\n"

// 20,000 entries with fixed slot paths, decimal sizes and SHA256 fit below
// 4 MiB. This is provider-private metadata, not a new common/wire limit.
const maxProjectionManifestBytes = 4 << 20

type projectionFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type projectionManifest struct {
	Layout         string           `json:"layout"`
	Scope          dp.Scope         `json:"scope"`
	Revision       dp.Revision      `json:"revision"`
	SnapshotSHA256 string           `json:"snapshot_sha256"`
	Files          []projectionFile `json:"files"`
}

// Views intentionally omit non-native facts. The untouched canonical snapshot
// is the only slot-to-ID mapping and the only round-trip representation.
func renderViews(e dp.Element) [2][]byte {
	rd := readmeView{Title: e.Title, Description: e.Description, Scope: e.Scope, AC: e.AcceptanceCriteria}
	pd := progressView{Status: e.Status, Review: e.Review, TaskClass: e.TaskClass, Estimate: e.Estimate, AssignedTo: e.AssignedTo, BlockedBy: e.BlockedBy, Checklist: e.Checklist, Notes: e.Notes}
	if e.CreatedAt != nil {
		pd.CreatedAt = *e.CreatedAt
	} else {
		pd.CreatedAt = time.Time{}
	}
	if e.UpdatedAt != nil {
		pd.LastUpdate = *e.UpdatedAt
	} else {
		pd.LastUpdate = time.Time{}
	}
	for _, ref := range e.ResourceRefs {
		switch ref.Type {
		case "precondition":
			pd.PreconditionResources = append(pd.PreconditionResources, ref)
		case "outcome":
			pd.OutcomeResources = append(pd.OutcomeResources, ref)
		}
	}
	return [2][]byte{[]byte(projectionHeader + writeCanonicalReadme(&rd)), []byte(projectionHeader + writeCanonicalProgress(&pd))}
}
func newProjection(s dp.Snapshot, digest string) projectionManifest {
	return projectionManifest{Layout: projectionLayout, Scope: s.Board.Scope, Revision: s.Revision, SnapshotSHA256: digest, Files: make([]projectionFile, 0, 2*len(s.Elements))}
}
func projectionPaths(i int) [2]string {
	slot := fmt.Sprintf("elements/%05d/", i)
	return [2]string{slot + "README.md", slot + "progress.md"}
}
func projectionEntry(name string, b []byte) projectionFile {
	return projectionFile{Path: name, Size: int64(len(b)), SHA256: hash(b)}
}
func projectionBytes(m projectionManifest) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil || len(b) > maxProjectionManifestBytes {
		return nil, fail(dp.StorageError)
	}
	return b, nil
}
func (p *Provider) writeProjection(ctx context.Context, dir string, s dp.Snapshot, digest string) (string, error) {
	// ValidateSnapshot already enforces this unchanged common cap. Keep this
	// guard local too: a five-digit slot outside 00000..09999 is never emitted.
	if len(s.Elements) > dp.MaxElements {
		return "", fail(dp.InvalidSnapshot)
	}
	if err := p.root.Mkdir(dir+"/elements", 0700); err != nil {
		return "", storage(err)
	}
	m := newProjection(s, digest)
	for i, e := range s.Elements {
		if err := ctx.Err(); err != nil {
			return "", dp.ContextError(err)
		}
		slot := fmt.Sprintf("%s/elements/%05d", dir, i)
		if err := p.root.Mkdir(slot, 0700); err != nil {
			return "", storage(err)
		}
		paths, views := projectionPaths(i), renderViews(e)
		for j, name := range paths {
			if err := p.write(dir+"/"+name, views[j]); err != nil {
				return "", err
			}
			m.Files = append(m.Files, projectionEntry(name, views[j]))
			if i == 0 && j == 0 {
				if err := p.hit(ctx, "first_projection_write"); err != nil {
					return "", err
				}
			}
			if i == len(s.Elements)-1 && j == 1 {
				if err := p.hit(ctx, "last_projection_write"); err != nil {
					return "", err
				}
			}
		}
		if err := p.syncDir(slot); err != nil {
			return "", err
		}
	}
	if err := p.syncDir(dir + "/elements"); err != nil {
		return "", err
	}
	b, err := projectionBytes(m)
	if err != nil {
		return "", err
	}
	if err := p.write(dir+"/projection.json", b); err != nil {
		return "", err
	}
	if err := p.hit(ctx, "projection_sync"); err != nil {
		return "", err
	}
	return hash(b), nil
}
func (p *Provider) verifyProjection(ctx context.Context, dir string, s dp.Snapshot, env envelope) error {
	b, err := p.read(dir+"/projection.json", maxProjectionManifestBytes)
	if err != nil {
		return err
	}
	if dp.ValidateDigest(env.ProjectionSHA256) != nil || hash(b) != env.ProjectionSHA256 {
		return fail(dp.StorageError)
	}
	if err := p.checkDir(dir + "/elements"); err != nil {
		return err
	}
	slots, err := p.entries(dir + "/elements")
	if err != nil {
		return err
	}
	if len(slots) != len(s.Elements) {
		return fail(dp.StorageError)
	}
	names := make([]string, 0, len(slots))
	for _, slot := range slots {
		names = append(names, slot.Name())
	}
	slices.Sort(names)
	m := newProjection(s, env.SnapshotSHA256)
	for i, e := range s.Elements {
		if err := ctx.Err(); err != nil {
			return dp.ContextError(err)
		}
		slot := fmt.Sprintf("%05d", i)
		if names[i] != slot {
			return fail(dp.StorageError)
		}
		location := dir + "/elements/" + slot
		if err := p.checkDir(location); err != nil {
			return err
		}
		entries, err := p.entries(location)
		if err != nil {
			return err
		}
		pair := make([]string, 0, len(entries))
		for _, entry := range entries {
			pair = append(pair, entry.Name())
		}
		slices.Sort(pair)
		if !slices.Equal(pair, []string{"README.md", "progress.md"}) {
			return fail(dp.StorageError)
		}
		paths, views := projectionPaths(i), renderViews(e)
		for j, name := range paths {
			actual, err := p.read(dir+"/"+name, int64(len(views[j])))
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, views[j]) {
				return fail(dp.StorageError)
			}
			m.Files = append(m.Files, projectionEntry(name, views[j]))
		}
	}
	expected, err := projectionBytes(m)
	if err != nil {
		return err
	}
	// Comparing canonical expected bytes also refuses duplicate/unknown fields,
	// aliases, mixed Scope/revision/snapshot, reordered entries and edited hashes.
	if !bytes.Equal(b, expected) {
		return fail(dp.StorageError)
	}
	return nil
}
