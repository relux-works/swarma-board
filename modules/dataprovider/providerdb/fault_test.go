package providerdb

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
	bolt "go.etcd.io/bbolt"
)

type physicalDriver struct {
	mu      sync.Mutex
	tickets map[string]*physicalTicket
}
type physicalTicket struct {
	driver       *physicalDriver
	root         string
	point        cf.FaultPoint
	mode         cf.FaultMode
	reached      chan struct{}
	mu           sync.Mutex
	process      *process
	hookID       uint64
	receipt      cf.FaultReceipt
	used, closed bool
}

func newDriver() *physicalDriver { return &physicalDriver{tickets: map[string]*physicalTicket{}} }
func (d *physicalDriver) Points() []cf.FaultPoint {
	return []cf.FaultPoint{
		{ID: "payload-membership", Stage: cf.BeforeDecision, Physical: "bbolt writable transaction after resource chunk/membership edits, before snapshot/event publication; rollback still owns all changes"},
		{ID: "before-commit", Stage: cf.BeforeDecision, Physical: "bbolt writable transaction contains facts/catalog/chunks/revision/event/integrity; immediately before Tx.Commit, no dirty pages committed"},
		{ID: "after-commit", Stage: cf.AfterDecision, Physical: "immediately after successful bbolt Tx.Commit with NoSync=false (dirty pages sync and metadata page sync), before API acknowledgment"},
		{ID: "recovery-validate", Stage: cf.DuringRecovery, Physical: "after bbolt.Open has selected its valid metadata page, before full scoped envelope/payload integrity validation; kill tests process restart, not power loss or an in-engine recovery write"},
		{ID: "exposed-read", Stage: cf.DuringResourceRead, Physical: "test-only pipe worker at actual pinned private resource reader Read; injected I/O refusal after successful verified acquisition"},
		{ID: "exposed-close", Stage: cf.DuringResourceClose, Physical: "test-only pipe worker at actual pinned resource reader Close; real backing is closed even on injected I/O failure"},
		{ID: "provider-close", Stage: cf.DuringProviderClose, Physical: "after admitted operations drain, immediately before Storm/bbolt Close; injected final-close failure is stable while real engine close still executes"},
	}
}
func (d *physicalDriver) Arm(ctx context.Context, root string, point cf.FaultPoint, mode cf.FaultMode) (cf.FaultTicket, error) {
	if e := checkContext(ctx); e != nil {
		return nil, e
	}
	allowed := false
	for _, p := range d.Points() {
		if p == point {
			allowed = true
		}
	}
	if !allowed {
		return nil, dp.NewError(dp.InvalidRequest)
	}
	valid := mode == cf.Pause || point.Stage == cf.BeforeDecision && (mode == cf.DefiniteFailure || mode == cf.DiskFull) || point.Stage == cf.AfterDecision && (mode == cf.Kill || mode == cf.AmbiguousFailure) || point.Stage == cf.DuringRecovery && mode == cf.Kill || point.Stage == cf.DuringResourceRead && mode == cf.ReadFailure || point.Stage == cf.DuringResourceClose && mode == cf.CloseFailure || point.Stage == cf.DuringProviderClose && mode == cf.ProviderCloseFailure
	if !valid {
		return nil, dp.NewError(dp.InvalidRequest)
	}
	ticket := &physicalTicket{driver: d, root: root, point: point, mode: mode, reached: make(chan struct{})}
	d.mu.Lock()
	defer d.mu.Unlock()
	if old := d.tickets[root]; old != nil {
		old.mu.Lock()
		active := !old.used && !old.closed
		old.mu.Unlock()
		if active {
			return nil, dp.NewError(dp.Unavailable)
		}
	}
	d.tickets[root] = ticket
	return ticket, nil
}
func (d *physicalDriver) reached(p *process, m message) {
	d.mu.Lock()
	ticket := d.tickets[p.root]
	d.mu.Unlock()
	if ticket == nil || ticket.point.ID != m.Point {
		_ = p.send(message{ID: m.ID, Kind: "resume"})
		return
	}
	ticket.mu.Lock()
	if ticket.used || ticket.closed || ticket.process != nil {
		ticket.mu.Unlock()
		_ = p.send(message{ID: m.ID, Kind: "resume"})
		return
	}
	ticket.process = p
	ticket.hookID = m.ID
	ticket.receipt = cf.FaultReceipt{PointID: ticket.point.ID, Mode: ticket.mode, ReachedAt: time.Now().UTC()}
	close(ticket.reached)
	ticket.mu.Unlock()
}
func (t *physicalTicket) WaitReached(ctx context.Context) error {
	select {
	case <-t.reached:
		return nil
	case <-ctx.Done():
		return dp.ContextError(ctx.Err())
	}
}
func (t *physicalTicket) Trigger(ctx context.Context) (cf.FaultReceipt, error) {
	if e := t.WaitReached(ctx); e != nil {
		return cf.FaultReceipt{}, e
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.used || t.closed {
		return cf.FaultReceipt{}, dp.NewError(dp.InvalidRequest)
	}
	t.used = true
	t.receipt.TriggeredAt = time.Now().UTC()
	if t.mode == cf.Kill {
		exit := t.process.kill()
		t.receipt.ProcessExit = &exit
	} else {
		m := message{ID: t.hookID, Kind: "resume"}
		if t.mode != cf.Pause {
			m.Code = dp.StorageError
		}
		if e := t.process.send(m); e != nil {
			return cf.FaultReceipt{}, dp.NewError(dp.StorageError)
		}
	}
	// Durable provider.db and the actual receipt are retained outside the worker.
	// DiskFull is a closed test I/O refusal at a real predecision point, NOT an
	// actual full-device experiment; no native storage qualification is inferred.
	t.receipt.EvidenceRetained = true
	return t.receipt, nil
}
func (t *physicalTicket) Close() error {
	t.mu.Lock()
	if !t.used && t.process != nil {
		_ = t.process.send(message{ID: t.hookID, Kind: "resume"})
	}
	t.closed = true
	t.mu.Unlock()
	t.driver.mu.Lock()
	if t.driver.tickets[t.root] == t {
		delete(t.driver.tickets, t.root)
	}
	t.driver.mu.Unlock()
	return nil
}
func (d *physicalDriver) Corrupt(ctx context.Context, root string, scope dp.Scope, kind cf.Corruption) error {
	if e := privatePath(root); e != nil {
		return e
	}
	if e := checkContext(ctx); e != nil {
		return e
	}
	if kind == cf.OutsideRootAlias {
		path := filepath.Join(root, "provider.db")
		target := filepath.Join(root, "retained.db")
		if e := os.Rename(path, target); e != nil {
			return e
		}
		return os.Symlink(target, path)
	}
	raw, e := bolt.Open(filepath.Join(root, "provider.db"), 0600, &bolt.Options{Timeout: dp.LockTimeout})
	if e != nil {
		return e
	}
	defer raw.Close()
	return raw.Update(func(tx *bolt.Tx) error {
		top := tx.Bucket(scopes)
		b := top.Bucket(key(scope))
		if b == nil {
			return dp.NewError(dp.NotFound)
		}
		switch kind {
		case cf.UnknownSchema:
			return top.Put([]byte("schema"), []byte("unknown/9"))
		case cf.MissingSchema, cf.LegacyTree:
			return top.Delete([]byte("schema"))
		case cf.ScopeMetadata:
			var s dp.Snapshot
			if e := json.Unmarshal(b.Get([]byte("snapshot")), &s); e != nil {
				return e
			}
			s.Board.Scope.ProductID = "different"
			p, _ := json.Marshal(s)
			return b.Put([]byte("snapshot"), p)
		case cf.RecoveryEvidence:
			return top.Put([]byte("unrecognized-recovery-evidence"), []byte("retained"))
		case cf.UnreadableFacts:
			return b.Put([]byte("snapshot"), []byte("{broken"))
		case cf.UnknownMarkdown, cf.ProgressOnly, cf.AuthorityDependency, cf.ExternalWriter:
			// DB has no markdown/authority loader. Preserve concrete unrecognized
			// schema fields rather than silently interpreting an alien tree or hook.
			return b.Put([]byte(string(kind)), []byte("unrecognized persistent field"))
		case cf.HistoryGap:
			return b.Bucket([]byte("events")).Delete(number(1))
		case cf.HistoryDuplicate:
			return b.Bucket([]byte("events")).Put(number(0), bytes.Clone(b.Bucket([]byte("events")).Get(number(1))))
		case cf.HistoryTail, cf.HistoryScope, cf.HistoryRevision, cf.HistoryOrdering:
			eb := b.Bucket([]byte("events"))
			k, v := eb.Cursor().Last()
			var event dp.Event
			if e := json.Unmarshal(v, &event); e != nil {
				return e
			}
			switch kind {
			case cf.HistoryTail:
				event.Actor.RequestID = "overwritten"
			case cf.HistoryScope:
				event.Scope.ProductID = "foreign"
			case cf.HistoryRevision:
				event.Revision.Counter++
			case cf.HistoryOrdering:
				event.ChangedIDs = []string{"TASK-1", "EPIC-1"}
			}
			p, _ := json.Marshal(event)
			return eb.Put(k, p)
		case cf.PayloadBytes, cf.PayloadDigest, cf.PayloadSize, cf.PayloadCatalog:
			pb := b.Bucket([]byte("payloads"))
			k, _ := pb.Cursor().First()
			if k == nil {
				return dp.NewError(dp.NotFound)
			}
			switch kind {
			case cf.PayloadBytes:
				return pb.Bucket(k).Put(number(0), []byte("bad"))
			case cf.PayloadDigest:
				dest, e := pb.CreateBucket([]byte("0000000000000000000000000000000000000000000000000000000000000000"))
				if e != nil {
					return e
				}
				if e = dest.Put(number(0), []byte("abc")); e != nil {
					return e
				}
				return pb.DeleteBucket(k)
			case cf.PayloadSize:
				return pb.Bucket(k).Put(number(1), []byte("extra"))
			case cf.PayloadCatalog:
				return pb.DeleteBucket(k)
			}
		}
		return dp.NewError(dp.InvalidRequest)
	})
}
func (d *physicalDriver) InstallRevision(ctx context.Context, root string, scope dp.Scope, n uint64) error {
	if e := checkContext(ctx); e != nil {
		return e
	}
	raw, e := bolt.Open(filepath.Join(root, "provider.db"), 0600, &bolt.Options{Timeout: dp.LockTimeout})
	if e != nil {
		return e
	}
	defer raw.Close()
	return raw.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(scopes).Bucket(key(scope))
		s, _, e := load(b)
		if e != nil {
			return e
		}
		s.Revision.Counter = n
		return persist(b, s, nil)
	})
}

func TestConformance(t *testing.T) {
	d := newDriver()
	report := cf.Run(t, factory(d), d)
	if path := os.Getenv("PROVIDERDB_REPORT"); path != "" {
		p, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(p, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	// RequireAll deliberately belongs to the later shared complete-report gate.
	// The unchanged runner records partial/integration/native cases explicitly.
}

// InstallHistory operates only on a closed disposable fixture. The supplied
// generator writes its complete exact contiguous stream. It cannot choose an
// oracle, shorten history or change a limit. A failed installation is retained
// as invalid state rather than exposed as a usable provider.
func (d *physicalDriver) InstallHistory(ctx context.Context, root string, fixture cf.HistoryFixture) error {
	if e := privatePath(root); e != nil {
		return e
	}
	if e := dp.ValidateSnapshot(fixture.Snapshot); e != nil {
		return e
	}
	raw, e := bolt.Open(filepath.Join(root, "provider.db"), 0600, &bolt.Options{Timeout: dp.LockTimeout})
	if e != nil {
		return e
	}
	defer raw.Close()
	return raw.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(scopes).Bucket(key(fixture.Snapshot.Board.Scope))
		if b == nil {
			return dp.NewError(dp.NotFound)
		}
		if len(fixture.Snapshot.Resources) != 0 {
			return dp.NewError(dp.InvalidRequest)
		}
		if e := b.DeleteBucket([]byte("events")); e != nil {
			return e
		}
		eb, e := b.CreateBucket([]byte("events"))
		if e != nil {
			return e
		}
		var count uint64
		var total int64
		e = fixture.Emit(func(event dp.Event) error {
			if e := checkContext(ctx); e != nil {
				return e
			}
			count++
			if event.FormatVersion != 1 || event.Scope != fixture.Snapshot.Board.Scope || event.Revision.Scope != event.Scope || event.Revision.Counter != count || dp.ValidateActor(event.Actor) != nil {
				return dp.NewError(dp.InvalidSnapshot)
			}
			var buf bytes.Buffer
			n, e := dp.EncodeEvent(&buf, event)
			if e != nil {
				return e
			}
			total += n + 1
			if total > dp.MaxEventNDJSONBytes {
				return dp.NewError(dp.PayloadTooLarge)
			}
			return eb.Put(number(count), buf.Bytes())
		})
		if e != nil {
			return e
		}
		if count != fixture.Snapshot.Revision.Counter || total != fixture.NDJSONBytes {
			return dp.NewError(dp.InvalidSnapshot)
		}
		return persist(b, fixture.Snapshot, nil)
	})
}
