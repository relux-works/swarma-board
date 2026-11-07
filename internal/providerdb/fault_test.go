package providerdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
	bolt "go.etcd.io/bbolt"
)

type physicalDriver struct {
	evidenceSeq   atomic.Uint64
	evidenceMu    sync.Mutex
	evidenceBytes int64
	mu            sync.Mutex
	tickets       map[string]*physicalTicket
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
	t.receipt.TriggeredAt = time.Now().UTC()
	retained, e := t.driver.capture(t.root, t.point.ID+"-"+string(t.mode))
	if e != nil {
		return cf.FaultReceipt{}, e
	}
	if t.mode != cf.Pause && !retained {
		return cf.FaultReceipt{}, dp.NewError(dp.Unavailable)
	}
	t.used = true

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
	// Only a completed physical capture and custody record establish retention.
	// DiskFull is a closed test I/O refusal at a real predecision point, NOT an
	// actual full-device experiment; no native storage qualification is inferred.
	t.receipt.EvidenceRetained = retained
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

// capture retains actual stopped/paused synthetic files before test cleanup.
// It has a separate small cap and never follows a source symlink. Test-only
// evidence paths are supplied by the isolated controller, not a provider API.
// A missing destination is optional only for a pause; Trigger refuses all
// evidence-required faults unless both the file and custody record are closed.
func (d *physicalDriver) capture(root, label string) (bool, error) {
	destination := os.Getenv("PROVIDERDB_EVIDENCE")
	if destination == "" {
		return false, nil
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || filepath.Base(label) != label || label == "." || label == ".." {
		return false, dp.NewError(dp.InvalidRequest)
	}
	// Custody beneath the fixture root would disappear with fixture cleanup.
	// privatePath also rejects symlink aliases in either path's ancestry.
	if e := privatePath(root); e != nil {
		return false, e
	}
	rel, e := filepath.Rel(root, destination)
	if e != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false, dp.NewError(dp.InvalidRequest)
	}
	if e := os.MkdirAll(destination, 0700); e != nil {
		return false, e
	}
	if e := privatePath(destination); e != nil {
		return false, e
	}
	path := filepath.Join(root, "provider.db")
	st, e := os.Lstat(path)
	if e != nil {
		return false, e
	}
	if !st.Mode().IsRegular() {
		return false, dp.NewError(dp.Unavailable)
	}
	if st.Size() > 2<<20 {
		return false, dp.NewError(dp.PayloadTooLarge)
	}
	d.evidenceMu.Lock()
	defer d.evidenceMu.Unlock()
	if st.Size() > 16<<20-d.evidenceBytes {
		return false, dp.NewError(dp.PayloadTooLarge)
	}
	id := d.evidenceSeq.Add(1)
	in, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return false, e
	}
	defer in.Close()
	opened, e := in.Stat()
	if e != nil {
		return false, e
	}
	if !os.SameFile(st, opened) || opened.Size() != st.Size() {
		return false, dp.NewError(dp.StorageError)
	}
	filename := fmt.Sprintf("%04d-%s.db", id, label)
	out, e := os.OpenFile(filepath.Join(destination, filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return false, e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, (2<<20)+1))
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return false, e
	}
	if n != st.Size() {
		return false, dp.NewError(dp.StorageError)
	}
	d.evidenceBytes += n
	entry := struct {
		File       string    `json:"file"`
		Label      string    `json:"label"`
		SHA256     string    `json:"sha256"`
		Bytes      int64     `json:"bytes"`
		CapturedAt time.Time `json:"captured_at"`
	}{filename, label, hex.EncodeToString(h.Sum(nil)), n, time.Now().UTC()}
	manifest, e := os.OpenFile(filepath.Join(destination, "manifest.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return false, e
	}
	mst, e := manifest.Stat()
	if e == nil && (!mst.Mode().IsRegular() || mst.Mode().Perm() != 0600) {
		e = dp.NewError(dp.Unavailable)
	}
	if e == nil {
		e = json.NewEncoder(manifest).Encode(entry)
	}
	if e == nil {
		e = manifest.Sync()
	}
	ce = manifest.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return false, e
	}
	return true, nil
}
