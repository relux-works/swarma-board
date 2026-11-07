//go:build linux

package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

func fixtureGeneration(root string, s dp.Scope) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, scopePath(s), "CURRENT"))
	if err != nil {
		return "", err
	}
	var ptr pointer
	if err := json.Unmarshal(b, &ptr); err != nil {
		return "", err
	}
	return filepath.Join(root, scopePath(s), ptr.Generation), nil
}
func replaceFixture(name string, b []byte) error {
	if err := os.Chmod(name, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(name, b, 0400); err != nil {
		return err
	}
	return os.Chmod(name, 0400)
}
func (d *driver) InstallRevision(ctx context.Context, root string, s dp.Scope, counter uint64) error {
	dir, err := fixtureGeneration(root, s)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return err
	}
	var snapshot dp.Snapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		return err
	}
	snapshot.Revision.Counter = counter
	b, err = json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return replaceFixture(filepath.Join(dir, "snapshot.json"), b)
}
func (d *driver) Corrupt(ctx context.Context, root string, s dp.Scope, kind cf.Corruption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := fixtureGeneration(root, s)
	if err != nil {
		return err
	}
	write := func(name string, b []byte) error { return os.WriteFile(name, b, 0400) }
	switch kind {
	case cf.MissingSchema, cf.LegacyTree:
		return os.Remove(filepath.Join(root, "schema.json"))
	case cf.UnknownSchema:
		return replaceFixture(filepath.Join(root, "schema.json"), []byte(`{"format":"scoped-filesystem","version":999}`))
	case cf.ScopeMetadata:
		return replaceFixture(filepath.Join(root, scopePath(s), "scope.json"), []byte(`{"scope":{"product_id":"foreign","board_id":"board"},"name":"foreign","description":""}`))
	case cf.RecoveryEvidence:
		return write(filepath.Join(root, "transactions", "ambiguous.json"), []byte("{ambiguous"))
	case cf.UnknownMarkdown:
		return write(filepath.Join(dir, "unknown.md"), []byte("## Unknown\nnonrepresentable\n"))
	case cf.ProgressOnly:
		return write(filepath.Join(dir, "progress.md"), []byte("progress without README\n"))
	case cf.AuthorityDependency:
		return write(filepath.Join(dir, "execution-authority.json"), []byte(`{"required":true}`))
	case cf.UnreadableFacts:
		return os.Chmod(filepath.Join(dir, "snapshot.json"), 0000)
	case cf.OutsideRootAlias:
		name := filepath.Join(dir, "snapshot.json")
		if err := os.Remove(name); err != nil {
			return err
		}
		outside := filepath.Join(root, "..", "outside-facts")
		if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
			return err
		}
		return os.Symlink(outside, name)
	case cf.ExternalWriter:
		fd, err := os.OpenFile(filepath.Join(root, "owner.lock"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			fd.Close()
			return err
		}
		// Lifetime held by a separate real descriptor; release during factory cleanup.
		d.mu.Lock()
		d.foreignLocks = append(d.foreignLocks, fd)
		d.mu.Unlock()
		return nil
	case cf.HistoryGap, cf.HistoryDuplicate, cf.HistoryTail, cf.HistoryScope, cf.HistoryRevision, cf.HistoryOrdering:
		name := filepath.Join(dir, "events.ndjson")
		b, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		switch kind {
		case cf.HistoryGap:
			lines = lines[1:]
		case cf.HistoryDuplicate:
			lines = append(lines, lines[0])
		default:
			var e dp.Event
			if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
				return err
			}
			switch kind {
			case cf.HistoryTail:
				e.Actor.RequestID = "overwritten"
			case cf.HistoryScope:
				e.Scope.ProductID = "foreign"
			case cf.HistoryRevision:
				e.Revision.Counter++
			case cf.HistoryOrdering:
				e.ChangedIDs = []string{"TASK-1", "EPIC-1"}
			}
			line, _ := json.Marshal(e)
			lines[0] = string(line)
		}
		return replaceFixture(name, []byte(strings.Join(lines, "\n")+"\n"))
	case cf.PayloadBytes, cf.PayloadDigest, cf.PayloadSize, cf.PayloadCatalog:
		var snapshot dp.Snapshot
		b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &snapshot); err != nil {
			return err
		}
		if len(snapshot.Resources) == 0 {
			return fail(dp.InvalidRequest)
		}
		name := filepath.Join(dir, "resources", snapshot.Resources[0].SHA256)
		switch kind {
		case cf.PayloadBytes:
			return replaceFixture(name, []byte("corrupt"))
		case cf.PayloadSize:
			return replaceFixture(name, []byte(""))
		case cf.PayloadDigest:
			return os.Rename(name, filepath.Join(dir, "resources", strings.Repeat("0", 64)))
		case cf.PayloadCatalog:
			return os.Remove(name)
		}
	}
	return fail(dp.InvalidRequest)
}
