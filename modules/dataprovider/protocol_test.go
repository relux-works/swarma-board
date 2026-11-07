package dataprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

func requireCode(t *testing.T, err error, want Code) {
	t.Helper()
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("got %v want %s", err, want)
	}
}
func TestIdentityCASAndActorBoundaries(t *testing.T) {
	s := Scope{ProductID: "alpha", BoardID: "work"}
	if err := ValidateScope(s); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../x", "a/b", "a\\b", "%2f", "UPPER", "nul\x00", strings.Repeat("a", 65), "аlpha"} {
		requireCode(t, ValidateScope(Scope{ProductID: id, BoardID: "work"}), InvalidRequest)
		requireCode(t, ValidateScope(Scope{ProductID: "alpha", BoardID: id}), InvalidRequest)
	}
	if err := ValidateScope(Scope{ProductID: strings.Repeat("a", 64), BoardID: "a-_9"}); err != nil {
		t.Fatal(err)
	}
	r, err := NextRevision(s, Revision{s, 1}, Revision{s, 1})
	if err != nil || r != (Revision{s, 2}) {
		t.Fatal(r, err)
	}
	_, err = NextRevision(s, Revision{s, 1}, Revision{s, 0})
	requireCode(t, err, RevisionConflict)
	_, err = NextRevision(s, Revision{s, 1}, Revision{Scope{ProductID: "beta", BoardID: "work"}, 1})
	requireCode(t, err, InvalidRequest)
	_, err = NextRevision(s, Revision{s, math.MaxUint64}, Revision{s, math.MaxUint64})
	requireCode(t, err, Unavailable)
	a := Actor{AccountID: strings.Repeat("x", 256), RequestID: strings.Repeat("r", 128), Operation: "import", At: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}
	if err := ValidateActor(a); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Actor){func(a *Actor) { a.AccountID += "x" }, func(a *Actor) { a.AccountID = "x\n" }, func(a *Actor) { a.RequestID += "x" }, func(a *Actor) { a.RequestID = "\x7f" }, func(a *Actor) { a.Operation = "execute" }, func(a *Actor) { a.At = a.At.In(time.FixedZone("off", 3600)) }, func(a *Actor) { a.At = time.Time{} }} {
		bad := a
		mutate(&bad)
		requireCode(t, ValidateActor(bad), InvalidRequest)
	}
}
func TestClosedErrorsDoNotExposeCauses(t *testing.T) {
	e := NewError(Code("raw path or diagnostic"))
	if e.Code() != StorageError || strings.Contains(e.Error(), "raw path") {
		t.Fatal(e)
	}
	e = ContextError(errors.Join(context.Canceled, errors.New("private diagnostic")))
	if !errors.Is(e, context.Canceled) || strings.Contains(e.Error(), "private") {
		t.Fatal(e)
	}
	if !errors.Is(NewError(InvalidRequest), NewError(InvalidRequest)) {
		t.Fatal("code matching failed")
	}
}
func TestReservedURNHasNoExternalFallback(t *testing.T) {
	d := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	for _, prefix := range []string{"urn:sha256:", "URN:sha256:", "urn:SHA256:", "UrN:sHa256:"} {
		got, res, err := ResourceDigest(prefix + d)
		if err != nil || !res || got != d {
			t.Fatal(got, res, err)
		}
	}
	for _, uri := range []string{"urn:sha256", "urn:sha256:", "URN:SHA256:ABC", "urn:sha256:" + strings.ToUpper(d), "urn:sha256:" + d + "?q", "urn:sha256:" + d + "#f", "urn:sha256:" + d + ":tail"} {
		_, res, err := ResourceDigest(uri)
		if !res {
			t.Fatal("reserved form escaped", uri)
		}
		requireCode(t, err, InvalidSnapshot)
	}
	_, res, err := ResourceDigest("urn:example:external")
	if res || err != nil {
		t.Fatal(res, err)
	}
	_, res, err = ResourceDigest("urn:ſha256:" + d)
	if res || err != nil {
		t.Fatal("Unicode case fold expanded reserved ASCII namespace", res, err)
	}
}

type boundedProbe struct {
	left   int
	read   int
	closed int
}

func (r *boundedProbe) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(r.left, len(p))
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	r.left -= n
	r.read += n
	return n, nil
}
func (r *boundedProbe) Close() error { r.closed++; return nil }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("sensitive read cause") }
func TestOneShotStreamingCountsEOFAndNeverClosesInputs(t *testing.T) {
	for _, raw := range [][]byte{{}, []byte("abc"), {'a', 0, 0xff}} {
		h := sha256.Sum256(raw)
		var out bytes.Buffer
		in := ResourceInput{Info: ResourceInfo{SHA256: hex.EncodeToString(h[:]), Size: int64(len(raw))}, Reader: bytes.NewBuffer(raw)}
		n, err := CopyResource(context.Background(), &out, in)
		if err != nil || n != int64(len(raw)) || !bytes.Equal(out.Bytes(), raw) {
			t.Fatal(n, err)
		}
	}
	p := &boundedProbe{left: 1000000}
	_, err := CopyResource(context.Background(), io.Discard, ResourceInput{Info: ResourceInfo{SHA256: strings.Repeat("0", 64), Size: 3}, Reader: p})
	requireCode(t, err, InvalidSnapshot)
	if p.read != 4 || p.closed != 0 {
		t.Fatal("must read at most size+1, never close", p)
	}
	_, err = CopyResource(context.Background(), io.Discard, ResourceInput{Info: ResourceInfo{SHA256: strings.Repeat("0", 64), Size: 3}, Reader: failingReader{}})
	requireCode(t, err, StorageError)
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = &boundedProbe{left: 3}
	_, err = CopyResource(ctx, io.Discard, ResourceInput{Info: ResourceInfo{SHA256: strings.Repeat("0", 64), Size: 3}, Reader: p})
	if !errors.Is(err, context.Canceled) || p.read != 0 {
		t.Fatal(err, p)
	}
}
func TestCanonicalGoldenBytesAndIndependentSizeAccounting(t *testing.T) {
	s := Snapshot{Board: Board{Scope: Scope{ProductID: "alpha", BoardID: "work"}, Name: "<A>"}, Revision: Revision{Scope: Scope{ProductID: "alpha", BoardID: "work"}}}
	const want = `{"board":{"scope":{"product_id":"alpha","board_id":"work"},"name":"\u003cA\u003e","description":""},"revision":{"scope":{"product_id":"alpha","board_id":"work"},"counter":0},"elements":[],"provenance":[],"resources":[]}`
	var out bytes.Buffer
	n, err := EncodeSnapshot(&out, s)
	if err != nil || out.String() != want || n != int64(len(want)) {
		t.Fatalf("%s (%d): %v", out.String(), n, err)
	}
	if out.Bytes()[len(out.Bytes())-1] == '\n' {
		t.Fatal("unexpected newline")
	}
	// A one-shot original reader is deliberately unusable: logical accounting
	// must use validated staging, and close the staging reader it acquires.
	c := ChangeSet{PutResources: []ResourceInput{{Info: ResourceInfo{SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3}, Reader: failingReader{}}}}
	stage := &closeProbe{Reader: strings.NewReader("abc")}
	out.Reset()
	n, err = EncodeChangeSet(context.Background(), &out, c, func(i int) (io.ReadCloser, error) {
		if i != 0 {
			t.Fatal(i)
		}
		return stage, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const changeWant = `{"upserts":[],"deletes":[],"provenance":null,"put_resources":[{"info":{"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","size":3},"data":"YWJj"}],"delete_resources":[]}`
	if out.String() != changeWant || n != int64(len(changeWant)) || stage.closed != 1 {
		t.Fatal(out.String(), n, stage.closed)
	}
	// Whole-checkpoint guard is redundant under lower writable caps: budget
	// injection verifies the encoder mechanism, never a backend at-cap claim.
	for _, cap := range []int64{0, 2, 3} {
		var dst bytes.Buffer
		b := &budgetWriter{w: &dst, cap: cap}
		_, err = b.Write([]byte("abc"))
		if cap < 3 {
			requireCode(t, err, PayloadTooLarge)
			if dst.Len() != 0 {
				t.Fatal("overflow chunk partially written")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

type closeProbe struct {
	io.Reader
	closed   int
	closeErr error
}

func (r *closeProbe) Close() error { r.closed++; return r.closeErr }

type checkpointProbe struct {
	snapshot Snapshot
	events   []Event
	child    io.ReadCloser
	closed   int
	closeErr error
	opened   int
}

func (c *checkpointProbe) Snapshot() Snapshot { return CloneSnapshot(c.snapshot) }
func (c *checkpointProbe) Events() []Event    { return CloneEvents(c.events) }
func (c *checkpointProbe) OpenResource(string) (io.ReadCloser, error) {
	c.opened++
	return c.child, nil
}
func (c *checkpointProbe) Close() error { c.closed++; return c.closeErr }
func TestCheckpointEncoderRejectsLateReadAndCloseFailures(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	s := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}, Resources: []ResourceInfo{{SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3}}}
	for _, mode := range []string{"read", "child-close", "handle-close"} {
		t.Run(mode, func(t *testing.T) {
			child := &closeProbe{Reader: strings.NewReader("abc")}
			cp := &checkpointProbe{snapshot: s, events: []Event{}, child: child}
			if mode == "read" {
				child.Reader = io.MultiReader(strings.NewReader("a"), failingReader{})
			}
			if mode == "child-close" {
				child.closeErr = errors.New("private close cause")
			}
			if mode == "handle-close" {
				cp.closeErr = errors.New("private handle cause")
			}
			_, err := EncodeCheckpoint(context.Background(), io.Discard, cp)
			requireCode(t, err, StorageError)
			if cp.closed != 1 || child.closed != 1 {
				t.Fatal("owned handles not closed", cp.closed, child.closed)
			}
		})
	}
}
func TestNativeV1BoundaryRetainsFieldOrderAndNullability(t *testing.T) {
	// This independent golden enumerates every accepted field in stored order.
	e := Element{BlockedBy: []string{}, ManualBlockedBy: nil, Checklist: []ChecklistItem{}, ResourceRefs: []ResourceRef{}, Estimate: &Estimate{Kind: "notEstimated"}}
	p, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"","type":"","name":"","parent_id":"","title":"","description":"","scope":"","acceptance_criteria":"","status":"","assigned_to":"","created_at":null,"updated_at":null,"blocked_by":[],"manual_blocked_by":null,"checklist":[],"notes":"","estimate":{"kind":"notEstimated"},"resource_refs":[]}`
	if string(p) != want {
		t.Fatal(string(p))
	}
	clone := CloneElement(e)
	clone.Estimate.Kind = "changed"
	if e.Estimate.Kind != "notEstimated" {
		t.Fatal("estimate alias")
	}
	if clone.ManualBlockedBy != nil || clone.BlockedBy == nil {
		t.Fatal("null/empty normalization")
	}
}
func TestBudgetIncludesCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	ctx, done := BoundedContext(parent, ReadTimeout)
	defer done()
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining > ReadTimeout || remaining < ReadTimeout-time.Second {
		t.Fatal(remaining)
	}
	short, shortCancel := context.WithTimeout(parent, time.Second)
	defer shortCancel()
	ctx2, done2 := BoundedContext(short, CheckpointTimeout)
	defer done2()
	a, _ := short.Deadline()
	b, _ := ctx2.Deadline()
	if !a.Equal(b) {
		t.Fatal("caller deadline was extended")
	}
}
func TestNativeV2GuardRefusesNonemptyAtCurrentCAS(t *testing.T) {
	b := Board{Scope: Scope{ProductID: "alpha", BoardID: "work"}, Name: "A work"}
	empty := Snapshot{Board: b, Revision: Revision{Scope: b.Scope, Counter: 7}}
	if err := CheckEmptyImportTarget(empty, b); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Elements = []Element{{ID: "TASK-1"}} }, func(s *Snapshot) { s.Provenance = []ProvenanceFile{{Path: "provenance/inert"}} }, func(s *Snapshot) { s.Resources = []ResourceInfo{{SHA256: strings.Repeat("a", 64)}} }} {
		current := empty
		mutate(&current)
		requireCode(t, CheckEmptyImportTarget(current, b), AlreadyExists)
	}
	other := b
	other.Scope.ProductID = "beta"
	requireCode(t, CheckEmptyImportTarget(empty, other), ScopeConflict)
	other = b
	other.Name = "different"
	requireCode(t, CheckEmptyImportTarget(empty, other), ScopeConflict)
}
