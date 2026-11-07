package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func metadataBoundsCase(t *testing.T, s *session) string {
	t.Run("element-count", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		upserts := make([]dp.Element, 0, dp.MaxElements-3)
		for i := 2; i <= dp.MaxElements-2; i++ {
			e := dp.CloneElement(local.seed[2])
			e.ID = fmt.Sprintf("TASK-%d", i)
			e.Name = e.ID
			upserts = append(upserts, e)
		}
		_, at := local.apply(dp.ChangeSet{Upserts: upserts}, "at-count")
		equal(t, len(at.snapshot.Elements), dp.MaxElements)
		e := dp.CloneElement(local.seed[2])
		e.ID = "TASK-99999"
		local.refuse(dp.ChangeSet{Upserts: []dp.Element{e}}, dp.InvalidSnapshot)
		local.apply(dp.ChangeSet{Deletes: []string{"TASK-9998"}}, "count-cleanup")
		_, retry := local.apply(dp.ChangeSet{Upserts: []dp.Element{e}}, "count-retry")
		equal(t, len(retry.snapshot.Elements), dp.MaxElements)
	})
	t.Run("provenance-count", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		files := make([]dp.ProvenanceFile, dp.MaxProvenanceCount)
		for i := range files {
			files[i] = provenance(fmt.Sprintf("f%05d", i), "")
		}
		_, at := local.apply(dp.ChangeSet{Provenance: files}, "at-provenance")
		equal(t, len(at.snapshot.Provenance), dp.MaxProvenanceCount)
		files = append(files, provenance("extra", ""))
		local.refuse(dp.ChangeSet{Provenance: files}, dp.InvalidSnapshot)
		local.apply(dp.ChangeSet{Provenance: []dp.ProvenanceFile{}}, "provenance-cleanup")
		local.apply(dp.ChangeSet{Provenance: []dp.ProvenanceFile{provenance("extra", "")}}, "provenance-retry")
	})
	t.Run("provenance-bytes-and-names", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		for _, n := range []int{99, 100} {
			file := provenance(strings.Repeat("a", n), "inert")
			_, now := local.apply(dp.ChangeSet{Provenance: []dp.ProvenanceFile{file}}, "provenance-name")
			equal(t, now.snapshot.Provenance, []dp.ProvenanceFile{file})
		}
		for _, name := range []string{strings.Repeat("a", 101), "", "é", "a/b", "../x"} {
			local.refuse(dp.ChangeSet{Provenance: []dp.ProvenanceFile{provenance(name, "inert")}}, dp.InvalidSnapshot)
		}
		file := provenance("at-byte-cap", strings.Repeat("x", int(dp.MaxProvenanceBytes)))
		local.apply(dp.ChangeSet{Provenance: []dp.ProvenanceFile{file}}, "provenance-byte-cap")
		file = provenance("one-over", strings.Repeat("x", int(dp.MaxProvenanceBytes)+1))
		local.refuse(dp.ChangeSet{Provenance: []dp.ProvenanceFile{file}}, dp.PayloadTooLarge)
		local.apply(dp.ChangeSet{Provenance: []dp.ProvenanceFile{}}, "provenance-byte-cleanup")
	})
	t.Run("snapshot-bytes", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		old := local.observe(local.scope())
		want := dp.CloneSnapshot(old.snapshot)
		want.Revision.Counter++
		base, err := dp.EncodeSnapshot(io.Discard, want)
		must(t, err)
		e := dp.CloneElement(local.seed[2])
		e.Notes = strings.Repeat("x", int(dp.MaxSnapshotBytes-base))
		_, at := local.apply(dp.ChangeSet{Upserts: []dp.Element{e}}, "snapshot-at-cap")
		n, err := dp.EncodeSnapshot(io.Discard, at.snapshot)
		must(t, err)
		equal(t, n, dp.MaxSnapshotBytes)
		// A decimal counter digit change would itself change the byte bound;
		// these counters stay single-digit, isolating the Notes byte boundary.
		e.Notes += "x"
		local.refuse(dp.ChangeSet{Upserts: []dp.Element{e}}, dp.PayloadTooLarge)
		e.Notes = ""
		local.apply(dp.ChangeSet{Upserts: []dp.Element{e}}, "snapshot-cleanup")
	})
	if _, ok := s.faults.(HistoryDriver); !ok {
		return "Element/provenance/Snapshot boundaries implemented; real complete event-NDJSON fixture installer not supplied"
	}
	t.Run("event-ndjson-exact-and-one-over", func(t *testing.T) { local := s.fresh(t); local.setup(); eventCapacity(t, local) })
	return ""
}

type repeatingReader byte

func (r repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}
func generatedInput(b byte, n int64) dp.ResourceInput {
	h := sha256.New()
	_, _ = io.CopyN(h, repeatingReader(b), n)
	return dp.ResourceInput{Info: dp.ResourceInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, Reader: io.LimitReader(repeatingReader(b), n)}
}
func resourceBoundsCase(t *testing.T, s *session) string {
	t.Run("resource-file-and-scope-bytes", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		at := generatedInput('a', dp.MaxResourceBytes)
		local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{at}}, "resource-file-at")
		tooBig := generatedInput('z', dp.MaxResourceBytes+1)
		local.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{tooBig}}, dp.PayloadTooLarge)
		puts := []dp.ResourceInput{}
		for b := byte('b'); b < 'i'; b++ {
			puts = append(puts, generatedInput(b, dp.MaxResourceBytes))
		}
		_, now := local.apply(dp.ChangeSet{PutResources: puts}, "raw-scope-at")
		var total int64
		for _, r := range now.snapshot.Resources {
			total += r.Size
		}
		equal(t, total, dp.MaxScopeResourceBytes)
		local.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{input("x")}}, dp.PayloadTooLarge)
		// Resulting-state replacement at the cap must not charge old+new bytes.
		replacement := generatedInput('j', dp.MaxResourceBytes)
		local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{replacement}, DeleteResources: []string{at.Info.SHA256}}, "at-cap-replace")
		local.apply(dp.ChangeSet{DeleteResources: []string{replacement.Info.SHA256}}, "raw-quota-cleanup")
		local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("x")}}, "raw-quota-retry")
	})
	t.Run("resource-count", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		puts := make([]dp.ResourceInput, dp.MaxResourceCount)
		for i := range puts {
			var raw [4]byte
			binary.BigEndian.PutUint32(raw[:], uint32(i))
			puts[i] = input(string(raw[:]))
		}
		_, at := local.apply(dp.ChangeSet{PutResources: puts}, "resource-count-at")
		equal(t, len(at.snapshot.Resources), dp.MaxResourceCount)
		local.refuse(dp.ChangeSet{PutResources: []dp.ResourceInput{input("extra")}}, dp.InvalidSnapshot)
		local.apply(dp.ChangeSet{DeleteResources: []string{at.snapshot.Resources[0].SHA256}}, "membership-cleanup")
		_, retry := local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("extra")}}, "membership-retry")
		equal(t, len(retry.snapshot.Resources), dp.MaxResourceCount)
	})
	return "32MiB/file,256MiB/raw-scope,10000-membership boundaries and resulting-state replacement implemented; whole ChangeSet/Checkpoint guard qualification is separate encoder harness evidence"
}
func closeCase(t *testing.T, s *session) string {
	s.setup()
	s.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "drain-fixture")
	r, err := s.p.ReadResource(s.ctx, s.scope(), abcDigest)
	closeOnRefusal(err, r.Reader)
	must(t, err)
	defer r.Reader.Close()
	closed := make(chan error, 1)
	go func() { closed <- s.p.Close() }()
	deadline := time.NewTimer(dp.LockTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case err := <-closed:
			t.Fatalf("Close completed with live admitted reader: %v", err)
		case <-deadline.C:
			t.Fatal("Close did not stop admission within lock bound")
		case <-ticker.C:
			_, err := s.p.View(s.ctx, s.scope())
			if err != nil {
				code(t, err, dp.Unavailable)
				break wait
			}
		}
	}
	equal(t, readAll(t, r.Reader), []byte("abc"))
	select {
	case err := <-closed:
		must(t, err)
	case <-s.ctx.Done():
		t.Fatal("Close failed to drain")
	}
	must(t, s.p.Close())
	must(t, s.p.Close())
	_, err = s.p.View(s.ctx, s.scope())
	code(t, err, dp.Unavailable)
	points, gap := mappedPoints(t, s, BeforeDecision)
	if gap != "" {
		return gap
	}
	t.Run("actual-shutdown-timeout-retains-admitted-operation", func(t *testing.T) {
		local := s.fresh(t)
		local.setup()
		old := local.observe(local.scope())
		point := points[0]
		ticket := arm(t, local, point, Pause)
		done := startEnvelope(local)
		must(t, ticket.WaitReached(local.ctx))
		// The physical test pause deliberately holds an admitted operation past
		// its deadline, simulating uninterruptible storage. No common cap is
		// shortened and no real backend qualification is claimed by module tests.
		closed := make(chan error, 1)
		started := time.Now()
		go func() { closed <- local.p.Close() }()
		select {
		case err := <-closed:
			code(t, err, dp.Unavailable)
		case <-time.After(dp.CloseTimeout + dp.LockTimeout):
			t.Fatal("Close exceeded shutdown bound")
		}
		elapsed := time.Since(started)
		t.Logf("actual Close timeout elapsed=%s bound=%s", elapsed, dp.CloseTimeout)
		if elapsed < dp.CloseTimeout-time.Second {
			t.Fatal("Close reported timeout before the shutdown deadline")
		}
		trigger(t, local, ticket, point, Pause)
		r := awaitUpdate(t, local, done)
		if !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("expired pre-decision operation outcome: %v", r.err)
		}
		zeroCommit(t, r.c)
		must(t, local.p.Close())
		must(t, local.p.Close())
		local.restart()
		local.unchanged(old)
	})
	closePoints, gap := mappedPoints(t, s, DuringProviderClose)
	if gap != "" {
		return gap
	}
	for _, point := range closePoints {
		t.Run("stable-close-error-"+point.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			ticket := arm(t, local, point, ProviderCloseFailure)
			done := make(chan error, 1)
			go func() { done <- local.p.Close() }()
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, point, ProviderCloseFailure)
			var first error
			select {
			case first = <-done:
			case <-local.ctx.Done():
				t.Fatal("final close exceeded bound")
			}
			code(t, first, dp.StorageError)
			second := local.p.Close()
			code(t, second, dp.StorageError)
			equal(t, first.Error(), second.Error())
			if !errors.Is(first, second) {
				t.Fatal("final close error is not stable")
			}
			_, err := local.p.View(local.ctx, local.scope())
			code(t, err, dp.Unavailable)
			must(t, ticket.Close())
			// Cleanup/restart belongs to the test controller after final close; no
			// second physical close is allowed to replace the stable final error.
			local.restart()
			local.unchanged(old)
		})
	}
	return ""
}
func peerCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	if s.instance.OpenPeer == nil {
		return "Distinct real handle/process opener missing; process-local mutex is not evidence"
	}
	ctx, cancel := context.WithTimeout(s.ctx, dp.LockTimeout)
	defer cancel()
	started := time.Now()
	peer, _, cleanup, err := s.instance.OpenPeer(ctx)
	t.Logf("peer open elapsed=%s", time.Since(started))
	if err != nil {
		code(t, err, dp.Unavailable)
		if time.Since(started) > dp.LockTimeout+time.Second {
			t.Fatal("owner refusal exceeded bound")
		}
		s.unchanged(old)
		// Preserve the same final observable command outcome when the supported
		// topology refuses a second owner. The refusal itself must have no effect.
		_, err = s.update(1, "one", "changed")
		must(t, err)
		s.expectNotes(old, "one", "changed")
		return ""
	}
	if cleanup == nil || peer == nil {
		t.Fatal("peer opener returned incomplete handles")
	}
	defer cleanup()
	defer peer.Close()
	start := make(chan struct{})
	done := make(chan updateResult, 2)
	for _, p := range []dp.Provider{s.p, peer} {
		p := p
		go func() {
			<-start
			c, err := p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(snap dp.Snapshot) (dp.ChangeSet, error) {
				e := snap.Elements[2]
				e.Notes = "changed"
				return dp.ChangeSet{Upserts: []dp.Element{e}}, nil
			})
			done <- updateResult{c, err}
		}()
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		r := awaitUpdate(t, s, done)
		if r.err == nil {
			wins++
		} else {
			code(t, r.err, dp.RevisionConflict)
			zeroCommit(t, r.c)
		}
	}
	equal(t, wins, 1)
	s.expectNotes(old, "one", "changed")
	return ""
}
func deadlinesCase(t *testing.T, s *session) string {
	s.setup()
	s.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "deadline-fixture")
	ctx, cancel := context.WithCancel(s.ctx)
	r, err := s.p.ReadResource(ctx, s.scope(), abcDigest)
	closeOnRefusal(err, r.Reader)
	must(t, err)
	defer r.Reader.Close()
	cancel()
	buf := make([]byte, 1)
	_, err = r.Reader.Read(buf)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resource cancellation not recognizable: %v", err)
	}
	must(t, r.Reader.Close())
	ctx, cancel = context.WithCancel(s.ctx)
	cp, err := s.p.Checkpoint(ctx, s.scope())
	closeOnRefusal(err, cp)
	must(t, err)
	defer cp.Close()
	snap := cp.Snapshot()
	cancel()
	child, err := cp.OpenResource(abcDigest)
	if child != nil {
		_ = child.Close()
		t.Fatal("canceled handle opened child")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint cancellation not recognizable: %v", err)
	}
	must(t, cp.Close())
	equal(t, cp.Snapshot(), snap)
	if s.faults == nil {
		return "Caller cancellation assertions implemented; real lifetime/lock/recovery scenarios require adapter fault machinery; wire alignment is later integration"
	}
	for _, checkpoint := range []bool{false, true} {
		for _, caller := range []string{"missing", "short", "long"} {
			name := "resource-"
			cap := dp.ReadTimeout
			if checkpoint {
				name = "checkpoint-"
				cap = dp.CheckpointTimeout
			}
			t.Run(name+caller, func(t *testing.T) {
				local := s.fresh(t)
				local.setup()
				local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "lifetime-fixture")
				ctx := local.ctx
				cancel := func() {}
				effective := cap
				if caller == "short" {
					effective = 25 * time.Millisecond
					ctx, cancel = context.WithTimeout(ctx, effective)
				} else if caller == "long" {
					ctx, cancel = context.WithTimeout(ctx, 2*cap)
				}
				defer cancel()
				started := time.Now()
				var handle dp.Checkpoint
				var child io.ReadCloser
				var err error
				if checkpoint {
					handle, err = local.p.Checkpoint(ctx, local.scope())
					if handle != nil {
						defer handle.Close()
					}
				} else {
					r, e := local.p.ReadResource(ctx, local.scope(), abcDigest)
					err = e
					child = r.Reader
					if child != nil {
						defer child.Close()
					}
				}
				if err != nil {
					if caller != "short" || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
					if handle != nil || child != nil {
						t.Fatal("handle on failed acquisition")
					}
					return
				}
				// Admission time includes acquisition/preparation. These real timers are
				// intentionally not shortened for a backend or replaced by a mock pass.
				wake := started.Add(effective + 20*time.Millisecond)
				if wait := time.Until(wake); wait > 0 {
					select {
					case <-time.After(wait):
					case <-local.ctx.Done():
						t.Fatal("lifetime scenario exceeded enclosing bound")
					}
				}
				if checkpoint {
					child, err = handle.OpenResource(abcDigest)
					if child != nil {
						_ = child.Close()
						t.Fatal("expired checkpoint opened a child")
					}
				} else {
					_, err = child.Read(make([]byte, 1))
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lifetime expiration not recognizable: %v", err)
				}
				t.Logf("actual %s lifetime elapsed=%s effective=%s", name+caller, time.Since(started), effective)
				// An expired, deliberately unclosed handle must release its lease so
				// provider shutdown can complete; explicit Close is still safe afterward.
				closed := make(chan error, 1)
				go func() { closed <- local.p.Close() }()
				select {
				case err := <-closed:
					must(t, err)
				case <-time.After(dp.CloseTimeout + time.Second):
					t.Fatal("expired handle retained its provider lease past shutdown bound")
				}
			})
		}
	}
	return "Caller cancellation and real missing/short/long 30s/300s lifetime actions implemented; lock assertions use C13, shutdown uses C21; actual 600s recovery timeout and wire deadline alignment remain NOT_VERIFIED"
}
func rootCase(t *testing.T, s *session) string {
	t.Run("aliased-root", func(t *testing.T) {
		target := t.TempDir()
		must(t, os.Chmod(target, 0700))
		parent := t.TempDir()
		must(t, os.Chmod(parent, 0700))
		alias := filepath.Join(parent, "alias")
		must(t, os.Symlink(target, alias))
		instance, err := s.factory.Open(s.ctx, alias)
		if instance != nil {
			if instance.Provider != nil {
				_ = instance.Provider.Close()
			}
			if instance.Cleanup != nil {
				_ = instance.Cleanup()
			}
		}
		if err == nil {
			t.Fatal("factory admitted a symlink storage root")
		}
		if _, ok := dp.CodeOf(err); !ok {
			t.Fatal("unsafe-root refusal lacks safe typed error")
		}
	})
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	for _, corrupt := range []Corruption{OutsideRootAlias, UnreadableFacts, UnknownMarkdown, ExternalWriter, AuthorityDependency} {
		t.Run(string(corrupt), func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			must(t, local.p.Close())
			must(t, local.faults.Corrupt(local.ctx, local.root, local.scope(), corrupt))
			p, pr, err := local.instance.Restart(local.ctx)
			if err != nil {
				if _, ok := dp.CodeOf(err); !ok {
					t.Fatal(err)
				}
				return
			}
			local.p = p
			local.provision = pr
			snap, err := p.View(local.ctx, local.scope())
			if err == nil {
				t.Fatal("unsafe/partial/authority-dependent facts were exposed")
			}
			equal(t, snap, dp.Snapshot{})
			if _, ok := dp.CodeOf(err); !ok {
				t.Fatal(err)
			}
		})
	}
	return "Root/unsafe-fact refusal actions implemented; source-specific markdown fidelity and non-authorizing recovery evidence require adapter review"
}
func strictTreeCase(t *testing.T, s *session) string {
	s.setup()
	old := s.observe(s.scope())
	e := dp.CloneElement(s.seed[2])
	e.Notes = "intro\n## Notes\n### Literal heading\n- [ ] literal task\n\ntrailing\n"
	e.Description = "literal ## and markdown [link](https://example.invalid/)"
	e.ResourceRefs = []dp.ResourceRef{{Name: "inert", URI: "urn:example:literal", Type: "outcome"}}
	_, now := s.apply(dp.ChangeSet{Upserts: []dp.Element{e}, Provenance: []dp.ProvenanceFile{provenance("literal", "## inert provenance\n")}}, "literal-markdown")
	equal(t, now.snapshot.Elements[2], e)
	must(t, s.provision.EnsureEmpty(s.ctx, s.boards[0]))
	s.unchanged(now)
	s.reopen()
	s.unchanged(now)
	if dp.EqualCanonicalSnapshot(old.snapshot, now.snapshot) {
		t.Fatal("markdown mutation disappeared")
	}
	if s.faults != nil {
		for _, corrupt := range []Corruption{LegacyTree, ProgressOnly, UnknownMarkdown, ScopeMetadata, RecoveryEvidence} {
			t.Run(string(corrupt), func(t *testing.T) {
				local := s.fresh(t)
				local.setup()
				must(t, local.p.Close())
				must(t, local.faults.Corrupt(local.ctx, local.root, local.scope(), corrupt))
				p, pr, err := local.instance.Restart(local.ctx)
				if err != nil {
					if _, ok := dp.CodeOf(err); !ok {
						t.Fatal(err)
					}
					return
				}
				local.p = p
				local.provision = pr
				_, err = p.View(local.ctx, local.scope())
				if err == nil {
					t.Fatal("unsafe/legacy/incomplete tree admitted")
				}
			})
		}
	}
	return "Literal markdown/provenance and idempotent EnsureEmpty/restart actions implemented; native-v2 import composition awaits shared integration and native-format/strict-tree conflict resolution"
}
func capacityCase(t *testing.T, s *session) string {
	if _, ok := s.faults.(HistoryDriver); !ok {
		return "Real complete event history fixture installer required for warning/refusal/sealing/at-cap reads"
	}
	t.Run("fact-provenance-resource-capacity-cleanup-and-history", func(t *testing.T) {
		if gap := metadataBoundsCase(t, s); gap != "" {
			t.Fatal(gap)
		}
		resourceBoundsCase(t, s)
	})
	points, gap := mappedPoints(t, s, BeforeDecision)
	if gap != "" {
		return gap
	}
	for _, point := range points {
		t.Run("disk-full-"+point.ID, func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			old := local.observe(local.scope())
			ticket := arm(t, local, point, DiskFull)
			done := startEnvelope(local)
			must(t, ticket.WaitReached(local.ctx))
			trigger(t, local, ticket, point, DiskFull)
			r := awaitUpdate(t, local, done)
			code(t, r.err, dp.StorageError)
			zeroCommit(t, r.c)
			local.unchanged(old)
			local.reopen()
			local.unchanged(old)
		})
	}
	return "Fact/provenance/resource resulting-state cleanup, exact event admission/sealing/at-cap reads and disk-full actions implemented; native-v2 cross-scope refusal/same-scope empty-server restoration requires shared integration"
}

func eventCapacity(t *testing.T, s *session) {
	t.Helper()
	installer, ok := s.faults.(HistoryDriver)
	if !ok {
		t.Fatal("complete history fixture installer required")
	}
	seed := s.observe(s.scope())
	headroom := int64(512)
	var fixture HistoryFixture
	// The next counter's decimal width determines its exact event length. Find
	// the fixed point without assuming a constant event-size reservation.
	for attempts := 0; attempts < 8; attempts++ {
		var err error
		fixture, err = exactHistory(seed.snapshot, seed.events[0], dp.MaxEventNDJSONBytes-headroom)
		must(t, err)
		n, err := dp.EncodeEvent(io.Discard, minimumNextEvent(fixture.Snapshot, s.actor))
		must(t, err)
		n++
		if n == headroom {
			break
		}
		headroom = n
		if attempts == 7 {
			t.Fatal("event fixture length did not stabilize")
		}
	}
	if fixture.NDJSONBytes < dp.EventWarningBytes {
		t.Fatal("near-cap fixture below warning threshold")
	}
	must(t, s.p.Close())
	must(t, installer.InstallHistory(s.ctx, s.root, fixture))
	s.restart()
	old := s.observe(s.scope())
	equal(t, old.snapshot, fixture.Snapshot)
	minimal := minimumNextEvent(old.snapshot, s.actor)
	tooLarge := minimal.Actor
	tooLarge.RequestID += "r"
	calls := 0
	c, err := s.p.Update(s.ctx, s.scope(), old.snapshot.Revision, tooLarge, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	code(t, err, dp.PayloadTooLarge)
	zeroCommit(t, c)
	if calls > 1 {
		t.Fatal("event-overflow callback rerun")
	}
	s.unchanged(old)
	c, err = s.p.Update(s.ctx, s.scope(), old.snapshot.Revision, minimal.Actor, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
	must(t, err)
	equal(t, c, dp.Commit{Revision: minimal.Revision, ChangedIDs: []string{}, ChangedResourceIDs: []string{}})
	sealed := s.observe(s.scope())
	equal(t, sealed.events[len(sealed.events)-1], minimal)
	var bytes int64
	for _, e := range sealed.events {
		n, err := dp.EncodeEvent(io.Discard, e)
		must(t, err)
		bytes += n + 1
	}
	equal(t, bytes, dp.MaxEventNDJSONBytes)
	c, err = s.p.Update(s.ctx, s.scope(), sealed.snapshot.Revision, minimal.Actor, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
	code(t, err, dp.PayloadTooLarge)
	zeroCommit(t, c)
	s.unchanged(sealed)
	must(t, s.p.Ready(s.ctx))
	other := dp.Board{Scope: dp.Scope{ProductID: "retired", BoardID: "fresh"}, Name: "Fresh"}
	must(t, s.provision.EnsureEmpty(s.ctx, other))
	fresh := s.observe(other.Scope)
	equal(t, fresh.snapshot.Revision.Counter, uint64(0))
	equal(t, fresh.snapshot.Elements, []dp.Element{})
	equal(t, fresh.events, []dp.Event{})
	s.reopen()
	s.unchanged(sealed)
	s.unchanged(fresh)
}
func resourceCorruptionCase(t *testing.T, s *session) string {
	if gap := missingFaults(s); gap != "" {
		return gap
	}
	for _, corrupt := range []Corruption{PayloadBytes, PayloadDigest, PayloadSize, PayloadCatalog} {
		t.Run(string(corrupt), func(t *testing.T) {
			local := s.fresh(t)
			local.setup()
			local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "corrupt-fixture")
			must(t, local.p.Close())
			must(t, local.faults.Corrupt(local.ctx, local.root, local.scope(), corrupt))
			p, pr, err := local.instance.Restart(local.ctx)
			if err != nil {
				c, ok := dp.CodeOf(err)
				if !ok || c != dp.StorageError && c != dp.Unavailable {
					t.Fatal(err)
				}
				return
			}
			local.p = p
			local.provision = pr
			r, err := p.ReadResource(local.ctx, local.scope(), abcDigest)
			if r.Reader != nil {
				_ = r.Reader.Close()
				t.Fatal("corrupt bytes exposed")
			}
			c, ok := dp.CodeOf(err)
			if !ok || c != dp.StorageError && c != dp.Unavailable {
				t.Fatalf("corruption error: %v", err)
			}
			cp, err := p.Checkpoint(local.ctx, local.scope())
			if cp != nil {
				_ = cp.Close()
				t.Fatal("corrupt checkpoint exposed")
			}
			c, ok = dp.CodeOf(err)
			if !ok || c != dp.StorageError && c != dp.Unavailable {
				t.Fatal(err)
			}
		})
	}
	for _, scenario := range []struct {
		stage Stage
		mode  FaultMode
	}{{DuringResourceRead, ReadFailure}, {DuringResourceClose, CloseFailure}} {
		points, gap := mappedPoints(t, s, scenario.stage)
		if gap != "" {
			return gap
		}
		for _, point := range points {
			for _, checkpoint := range []bool{false, true} {
				name := "resource-"
				if checkpoint {
					name = "checkpoint-"
				}
				t.Run(name+point.ID, func(t *testing.T) {
					local := s.fresh(t)
					local.setup()
					local.apply(dp.ChangeSet{PutResources: []dp.ResourceInput{input("abc")}}, "late-fault-fixture")
					old := local.observe(local.scope())
					var cp dp.Checkpoint
					var reader io.ReadCloser
					var err error
					if checkpoint {
						cp, err = local.p.Checkpoint(local.ctx, local.scope())
						closeOnRefusal(err, cp)
						must(t, err)
						defer cp.Close()
					} else {
						r, err := local.p.ReadResource(local.ctx, local.scope(), abcDigest)
						closeOnRefusal(err, r.Reader)
						must(t, err)
						reader = r.Reader
						defer reader.Close()
					}
					ticket := arm(t, local, point, scenario.mode)
					done := make(chan error, 1)
					go func() {
						if checkpoint {
							_, err := dp.EncodeCheckpoint(local.ctx, io.Discard, cp)
							done <- err
							return
						}
						_, readErr := io.Copy(io.Discard, reader)
						closeErr := reader.Close()
						if readErr != nil {
							done <- readErr
						} else {
							done <- closeErr
						}
					}()
					must(t, ticket.WaitReached(local.ctx))
					trigger(t, local, ticket, point, scenario.mode)
					select {
					case err := <-done:
						code(t, err, dp.StorageError)
					case <-local.ctx.Done():
						t.Fatal("late stream fault exceeded bound")
					}
					must(t, ticket.Close())
					local.unchanged(old)
				})
			}
		}
	}
	return ""
}
