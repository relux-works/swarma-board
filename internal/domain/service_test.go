package domain_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/domain"
	"github.com/relux-works/swarma-board/internal/query"
)

var ctx = context.Background()
var now = time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)

type memory struct {
	snap                               contract.Snapshot
	views, updates, callbacks, commits int
	fail                               error
	before                             func()
	lastAudit                          contract.AuditContext
}

func clone[T any](v T) T { b, _ := json.Marshal(v); var r T; _ = json.Unmarshal(b, &r); return r }
func (m *memory) View(context.Context, string) (contract.Snapshot, error) {
	m.views++
	return clone(m.snap), nil
}
func (m *memory) Update(c context.Context, p string, r contract.Revision, a contract.AuditContext, fn func(contract.Snapshot) (contract.ChangeSet, error)) (contract.Commit, error) {
	m.updates++
	m.lastAudit = a
	if m.before != nil {
		m.before()
	}
	if r != m.snap.Revision {
		return contract.Commit{}, &contract.Error{Code: "revision_conflict"}
	}
	m.callbacks++
	changes, err := fn(clone(m.snap))
	if err != nil {
		return contract.Commit{}, err
	}
	if m.fail != nil {
		return contract.Commit{}, m.fail
	}
	if err = c.Err(); err != nil {
		return contract.Commit{}, err
	}
	next := clone(m.snap)
	for _, id := range changes.Deletes {
		for i, e := range next.Elements {
			if e.ID == id {
				next.Elements = append(next.Elements[:i], next.Elements[i+1:]...)
				break
			}
		}
	}
	ids := append([]string{}, changes.Deletes...)
	for _, e := range changes.Upserts {
		found := false
		for i, old := range next.Elements {
			if old.ID == e.ID {
				next.Elements[i] = e
				found = true
			}
		}
		if !found {
			next.Elements = append(next.Elements, e)
		}
		ids = append(ids, e.ID)
	}
	if changes.Provenance != nil {
		next.Provenance = changes.Provenance
	}
	next.Revision++
	m.snap = next
	m.commits++
	return contract.Commit{Revision: next.Revision, ChangedIDs: query.Unique(ids)}, nil
}
func (*memory) Ready(context.Context) error { return nil }
func (*memory) Close() error                { return nil }
func fixture(t *testing.T) contract.Snapshot {
	t.Helper()
	b, err := os.ReadFile("../testadapter/testdata/elements.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Elements []contract.Element `json:"elements"`
	}
	if json.Unmarshal(b, &doc) != nil {
		t.Fatal("fixture")
	}
	return contract.Snapshot{Project: contract.Project{ID: "synthetic-board"}, Elements: doc.Elements}
}
func service(t *testing.T, m *memory) *domain.Service {
	t.Helper()
	s, err := domain.New(m, func() time.Time { return now }, func(typ string) (string, error) { return strings.ToUpper(typ) + "-261105-abc123", nil })
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func parse(t *testing.T, input string) contract.Operation {
	t.Helper()
	op, err := query.New().Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func call(t *testing.T, s *domain.Service, m *memory, input string) (contract.Reply, error) {
	t.Helper()
	r := m.snap.Revision
	return s.Execute(ctx, contract.Call{ProjectID: "synthetic-board", Operation: parse(t, input), Expected: &r, Audit: contract.AuditContext{AccountID: "synthetic-editor", RequestID: "request-1", Operation: input, At: now}})
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *contract.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("got %v want %s", err, want)
	}
}
func element(m *memory, id string) *contract.Element {
	for i := range m.snap.Elements {
		if m.snap.Elements[i].ID == id {
			return &m.snap.Elements[i]
		}
	}
	panic(id)
}
func decode(t *testing.T, r contract.Reply) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.JSON, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func success(t *testing.T, s *domain.Service, m *memory, input string) contract.Reply {
	t.Helper()
	r, e := call(t, s, m, input)
	if e != nil {
		t.Fatalf("%s: %v", input, e)
	}
	return r
}

func TestMutationUsesOnlyTransactionSnapshotAndCommitSuccess(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	m.fail = errors.New("commit refused")
	original := clone(m.snap)
	r, err := call(t, s, m, `set_notes(TASK-001,text="candidate")`)
	if err == nil || len(r.JSON) != 0 || r.Revision != 0 || !reflect.DeepEqual(m.snap, original) || m.views != 0 || m.callbacks != 1 || m.commits != 0 {
		t.Fatal("leaked failure", r, err, m)
	}
	m.fail = nil
	m.before = func() { element(m, "TASK-001").Notes = "fresh callback state" }
	r = success(t, s, m, `set_notes(task-001,text="appended")`)
	if element(m, "TASK-001").Notes != "fresh callback state\nappended" || m.views != 0 || m.commits != 1 {
		t.Fatal("stale snapshot")
	}
	row := decode(t, r)["set_notes"].(map[string]any)
	if row["id"] != "TASK-001" || r.Revision != 1 || !reflect.DeepEqual(row["changed_ids"], []any{"TASK-001"}) {
		t.Fatal(row, r)
	}
}
func TestNoRepositoryAccessForInvalidOrUntrustedOperations(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	rev := contract.Revision(0)
	for _, name := range []string{"spawn", "session", "scheduler", "agents", "models", "activity", "ledger", "handoff", "normalize", "resource_upload"} {
		_, err := s.Execute(ctx, contract.Call{Operation: contract.Operation{Name: name}, Expected: &rev, Audit: contract.AuditContext{AccountID: "a"}})
		code(t, err, "unsupported_operation")
	}
	op := parse(t, `set_status(TASK-001,status=done)`)
	_, err := s.Execute(ctx, contract.Call{Operation: op, Expected: &rev})
	code(t, err, "unauthorized")
	_, err = s.Execute(ctx, contract.Call{Operation: op, Audit: contract.AuditContext{AccountID: "a"}})
	code(t, err, "revision_required")
	op.Args["runtime"] = json.RawMessage(`true`)
	_, err = s.Execute(ctx, contract.Call{Operation: op, Expected: &rev, Audit: contract.AuditContext{AccountID: "a"}})
	code(t, err, "query_error")
	if m.views+m.updates+m.callbacks+m.commits != 0 {
		t.Fatal("repository touched")
	}
}
func TestLifecycleEstimateAssigneeAndBlockers(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	_, err := call(t, s, m, `set_status(TASK-003,status=development)`)
	code(t, err, "estimate_required")
	success(t, s, m, `set_estimate(TASK-003,estimate=estimated,scale=fibonacci,number=4)`)
	_, err = call(t, s, m, `set_status(TASK-003,status=development)`)
	code(t, err, "blocked")
	success(t, s, m, `set_status(TASK-001,status=done)`)
	success(t, s, m, `set_status(TASK-002,status=closed)`)
	_, err = call(t, s, m, `set_status(TASK-003,status=development)`)
	code(t, err, "not_assigned")
	success(t, s, m, `assign(TASK-003,agent="label only")`)
	success(t, s, m, `set_status(TASK-003,status=development)`)
	// Reasserting development must not re-take work or require a fresh estimate.
	success(t, s, m, `set_estimate(TASK-003,estimate=notEstimated)`)
	success(t, s, m, `set_status(TASK-003,status=development)`)
	success(t, s, m, `set_status(TASK-003,status=reviewing)`)
	_, err = call(t, s, m, `set_status(TASK-003,status=development)`)
	code(t, err, "estimate_required")
	// Administrative closed may bypass unresolved TASK-003; done may not.
	_, err = call(t, s, m, `set_status(TASK-004,status=done)`)
	code(t, err, "blocked")
	success(t, s, m, `set_status(TASK-004,status=closed)`)
	// Legacy null remains null and can enter development with a label.
	m2 := &memory{snap: fixture(t)}
	s2 := service(t, m2)
	success(t, s2, m2, `assign(TASK-002,agent=legacy)`)
	success(t, s2, m2, `set_status(TASK-002,status=development)`)
	if element(m2, "TASK-002").Estimate != nil {
		t.Fatal("null estimate erased")
	}
	// Missing blockers never satisfy readiness, including a reassertion.
	element(m2, "TASK-002").BlockedBy = []string{"TASK-999"}
	_, err = call(t, s2, m2, `set_status(TASK-002,status=development)`)
	if err == nil {
		t.Fatal("dangling blocker accepted")
	}
}
func TestTerminalAncestorsExplicitReopeningAndReparent(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	_, err := call(t, s, m, `set_status(STORY-001,status=done)`)
	code(t, err, "dependency_conflict")
	success(t, s, m, `set_status(TASK-001,status=closed)`)
	success(t, s, m, `set_status(TASK-002,status=closed)`)
	success(t, s, m, `set_status(STORY-001,status=closed)`)
	for _, input := range []string{`set_status(TASK-001,status=backlog)`, `create(task,name=active,parent=STORY-001)`, `set_parent(TASK-003,parent=STORY-001)`} {
		before := clone(m.snap)
		_, err = call(t, s, m, input)
		code(t, err, "dependency_conflict")
		if !reflect.DeepEqual(before, m.snap) {
			t.Fatal("partial ancestor change")
		}
	}
	success(t, s, m, `set_status(STORY-001,status=backlog)`)
	success(t, s, m, `set_status(TASK-001,status=backlog)`)
	_, err = call(t, s, m, `set_parent(TASK-003,parent=EPIC-001)`)
	code(t, err, "invalid_parent")
	_, err = call(t, s, m, `create(epic,name=bad,parent=STORY-001)`)
	code(t, err, "invalid_parent")
}
func TestCycleRefusalAndDoneBlockerLinkAreAtomic(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	before := clone(m.snap)
	_, err := call(t, s, m, `link(TASK-001,blocked_by=TASK-004)`)
	code(t, err, "dependency_cycle")
	if !reflect.DeepEqual(before, m.snap) || m.commits != 0 {
		t.Fatal("cycle changed graph")
	}
	_, err = call(t, s, m, `link(STORY-001,blocked_by=STORY-002)`)
	code(t, err, "dependency_conflict")
	_, err = call(t, s, m, `link(TASK-001,blocked_by=TASK-001)`)
	code(t, err, "dependency_conflict")
	success(t, s, m, `set_status(TASK-001,status=done)`)
	before = clone(m.snap)
	_, err = call(t, s, m, `link(TASK-001,blocked_by=TASK-002)`)
	code(t, err, "blocked")
	if !reflect.DeepEqual(before, m.snap) {
		t.Fatal("done link changed")
	}
	success(t, s, m, `set_status(TASK-001,status=closed)`)
	success(t, s, m, `link(TASK-001,blocked_by=TASK-002)`)
}
func TestDetailsNamesChecklistNotesAndDelete(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	title := element(m, "TASK-001").Title
	success(t, s, m, `rename(task-001,name="  New_Name!  ")`)
	if e := element(m, "TASK-001"); e.ID != "TASK-001" || e.Name != "new-name" || e.Title != title {
		t.Fatal(e)
	}
	success(t, s, m, `set_details(TASK-001,title="A title",description="",scope="new",review=none,task_class=metadata)`)
	if e := element(m, "TASK-001"); e.Title != "A title" || e.Description != "" || e.Scope != "new" || e.AcceptanceCriteria == "" || e.Review != "none" || e.TaskClass != "metadata" {
		t.Fatal(e)
	}
	for _, input := range []string{`uncheck_item(TASK-001,item=1)`, `check_item(TASK-001,item=2)`, `add_checklist_item(TASK-001,text="third")`, `remove_checklist_item(TASK-001,item=1)`} {
		success(t, s, m, input)
	}
	if got := element(m, "TASK-001").Checklist; len(got) != 2 || got[0].Text != "Second check" || !got[0].Checked || got[1].Text != "third" {
		t.Fatal(got)
	}
	before := clone(m.snap)
	_, err := call(t, s, m, `remove_checklist_item(TASK-001,item=3)`)
	code(t, err, "query_error")
	if !reflect.DeepEqual(before, m.snap) {
		t.Fatal("bad checklist changed")
	}
	success(t, s, m, `set_notes(TASK-001,text="replacement",set=true)`)
	success(t, s, m, `set_notes(TASK-001,text="next")`)
	if element(m, "TASK-001").Notes != "replacement\nnext" {
		t.Fatal("notes")
	}
	_, err = call(t, s, m, `delete(TASK-001,confirm=true)`)
	code(t, err, "dependency_conflict")
	_, err = call(t, s, m, `delete(STORY-001,confirm=true)`)
	code(t, err, "dependency_conflict")
	success(t, s, m, `unlink(TASK-003,blocked_by=TASK-001)`)
	success(t, s, m, `delete(TASK-001,confirm=true)`)
	if len(m.snap.Elements) != 6 {
		t.Fatal("cascade")
	}
	success(t, s, m, `create(bug,name=" New Bug ",parent=story-001)`)
	created := element(m, "BUG-261105-abc123")
	if created.Name != "new-bug" || created.Title != created.Name || created.ParentID != "STORY-001" || created.Review != "required" || created.TaskClass != "code" || created.Estimate == nil || created.Estimate.Kind != "notEstimated" || created.CreatedAt == nil || !created.CreatedAt.Equal(now) {
		t.Fatal(created)
	}
}
func TestReadEnvelopesSingleViewOrderingAndProjections(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	r := success(t, s, m, `get(task-003){planning}`)
	row := decode(t, r)["get"].(map[string]any)
	if row["id"] != "TASK-003" || !reflect.DeepEqual(row["blockedBy"], []any{"TASK-001", "TASK-002"}) || m.views != 1 || m.updates != 0 {
		t.Fatal(row, m)
	}
	r = success(t, s, m, `get(STORY-002){full}`)
	row = decode(t, r)["get"].(map[string]any)
	if !reflect.DeepEqual(row["derivedBlockedBy"], []any{"STORY-001"}) || row["isBlocked"] != false || row["isAdvisoryBlocked"] != true || row["estimate"].(map[string]any)["kind"] != "partiallyEstimated" {
		t.Fatal(row)
	}
	r = success(t, s, m, `list(type=task,skip=1,take=2){id}`)
	if !reflect.DeepEqual(decode(t, r)["list"], []any{map[string]any{"id": "TASK-002"}, map[string]any{"id": "TASK-003"}}) {
		t.Fatal(string(r.JSON))
	}
	r = success(t, s, m, `count(type=task,blocked=true)`)
	if decode(t, r)["count"] != float64(2) {
		t.Fatal(string(r.JSON))
	}
	r = success(t, s, m, `count(type=story,blocked=true)`)
	if decode(t, r)["count"] != float64(1) {
		t.Fatal(string(r.JSON))
	}
	r = success(t, s, m, `summary()`)
	summary := decode(t, r)["summary"].(map[string]any)
	if summary["total"] != float64(7) || len(summary["byStatus"].(map[string]any)) != 10 {
		t.Fatal(summary)
	}
	// Reparenting removes only the derived STORY link; imported manual facts survive.
	success(t, s, m, `set_parent(TASK-003,parent=STORY-001)`)
	r = success(t, s, m, `get(STORY-002){planning}`)
	row = decode(t, r)["get"].(map[string]any)
	if !reflect.DeepEqual(row["blockedBy"], []any{"STORY-001"}) || len(row["derivedBlockedBy"].([]any)) != 1 { // TASK-004 now induces the opposite relation back to STORY-001.
		t.Fatal(row)
	}
	if !reflect.DeepEqual(element(m, "STORY-002").ManualBlockedBy, []string{"STORY-001"}) {
		t.Fatal("manual facts rewritten")
	}
}
func TestIDRetryAndConstructorRequirements(t *testing.T) {
	m := &memory{snap: fixture(t)}
	element(m, "TASK-001").ID = "TASK-261105-abc123"
	element(m, "TASK-003").BlockedBy = []string{"TASK-261105-abc123", "TASK-002"}
	n := 0
	s, err := domain.New(m, func() time.Time { return now }, func(string) (string, error) {
		n++
		if n == 1 {
			return "TASK-261105-abc123", nil
		}
		return "TASK-261105-abc124", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	success(t, s, m, `create(task,name=new,parent=STORY-001)`)
	if n != 2 {
		t.Fatal(n)
	}
	if _, err = domain.New(nil, func() time.Time { return now }, nil); err == nil {
		t.Fatal("nil composition")
	}
	id, err := domain.RandomID("task", now)
	if err != nil || !strings.HasPrefix(id, "TASK-261105-") || len(id) != 18 {
		t.Fatal(id, err)
	}
}

func provenance(path string, data []byte) contract.ProvenanceFile {
	sum := sha256.Sum256(data)
	return contract.ProvenanceFile{Path: path, Data: data, SHA256: hex.EncodeToString(sum[:])}
}
func prepared(t *testing.T) contract.PreparedImport {
	snap := fixture(t)
	digest := strings.Repeat("a", 64)
	metadata, _ := json.Marshal(map[string]any{"archive_sha256": digest, "source_format": "swarma-board", "source_version": 1, "source_project_id": snap.Project.ID, "source_revision": 0, "source_commit": "synthetic-exporter", "exclusions": []string{}})
	snap.Provenance = []contract.ProvenanceFile{provenance("provenance/transfer-"+digest+".json", metadata), provenance("provenance/history.txt", []byte("inert synthetic claim"))}
	return contract.PreparedImport{SHA256: digest, Snapshot: snap, Report: contract.ImportReport{SHA256: digest, Format: "swarma-board", Version: 1, ElementCount: len(snap.Elements), Exclusions: []string{}}}
}
func TestImportAtomicEmptyTargetDigestAndAudit(t *testing.T) {
	p := prepared(t)
	m := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	s := service(t, m)
	audit := contract.AuditContext{AccountID: "current-authenticated-editor", At: now}
	m.fail = errors.New("commit failure")
	commit, err := s.Import(ctx, p.Snapshot.Project.ID, 0, audit, p)
	if err == nil || commit.Revision != 0 || m.commits != 0 || len(m.snap.Elements) != 0 || len(m.snap.Provenance) != 0 || m.views != 0 || m.callbacks != 1 {
		t.Fatal(commit, err, m)
	}
	m.fail = nil
	commit, err = s.Import(ctx, p.Snapshot.Project.ID, 0, audit, p)
	if err != nil || commit.Revision != 1 || m.callbacks != 2 || m.commits != 1 || m.views != 0 || len(m.snap.Provenance) != 3 || m.lastAudit.AccountID != audit.AccountID {
		t.Fatal(commit, err, m)
	}
	if !reflect.DeepEqual(m.snap.Elements, p.Snapshot.Elements) {
		t.Fatal("facts changed on import")
	}
	_, err = s.Import(ctx, p.Snapshot.Project.ID, 1, audit, p)
	code(t, err, "already_exists")
	if m.commits != 1 {
		t.Fatal("repeated import wrote")
	}
	// The target can become occupied after validation but before the callback.
	m2 := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	s2 := service(t, m2)
	m2.before = func() { m2.snap.Elements = []contract.Element{p.Snapshot.Elements[0]} }
	_, err = s2.Import(ctx, p.Snapshot.Project.ID, 0, audit, p)
	code(t, err, "already_exists")
	if m2.commits != 0 || m2.callbacks != 1 {
		t.Fatal("emptiness outside transaction")
	}
}
func TestImportRejectsInvalidGraphAndConflictingProvenance(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*contract.PreparedImport)
	}{
		{"dangling", func(p *contract.PreparedImport) { p.Snapshot.Elements[3].BlockedBy = []string{"TASK-999"} }},
		{"cycle", func(p *contract.PreparedImport) { p.Snapshot.Elements[3].BlockedBy = []string{"TASK-004"} }},
		{"invalid estimate", func(p *contract.PreparedImport) {
			p.Snapshot.Elements[3].Estimate = &contract.Estimate{Kind: "partiallyEstimated"}
		}},
		{"missing metadata", func(p *contract.PreparedImport) { p.Snapshot.Provenance = nil }},
		{"wrong project", func(p *contract.PreparedImport) { p.Snapshot.Project.ID = "other" }},
		{"wrong digest", func(p *contract.PreparedImport) { p.Report.SHA256 = strings.Repeat("b", 64) }},
		{"bad provenance checksum", func(p *contract.PreparedImport) { p.Snapshot.Provenance[1].Data = []byte("different") }},
		{"conflicting import bytes", func(p *contract.PreparedImport) {
			p.Snapshot.Provenance = append(p.Snapshot.Provenance, provenance("provenance/import-"+p.SHA256+".json", []byte("conflict")))
		}},
		{"conflicting history", func(p *contract.PreparedImport) {
			p.Snapshot.Provenance = append(p.Snapshot.Provenance, provenance("provenance/history.txt", []byte("conflict")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := prepared(t)
			test.change(&p)
			m := &memory{snap: contract.Snapshot{Project: contract.Project{ID: "synthetic-board"}}}
			s := service(t, m)
			_, err := s.Import(ctx, "synthetic-board", 0, contract.AuditContext{AccountID: "a"}, p)
			code(t, err, "invalid_snapshot")
			if m.updates != 0 || m.commits != 0 {
				t.Fatal("invalid candidate reached repository")
			}
		})
	}
}

func TestReadProjectionRecomputedWithoutPersistingDerivedFacts(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	success(t, s, m, `set_parent(TASK-003,parent=STORY-001)`)
	success(t, s, m, `set_parent(TASK-004,parent=STORY-001)`)
	row := decode(t, success(t, s, m, `get(STORY-002){blockedBy derivedBlockedBy}`))["get"].(map[string]any)
	if !reflect.DeepEqual(row["blockedBy"], []any{"STORY-001"}) || !reflect.DeepEqual(row["derivedBlockedBy"], []any{}) {
		t.Fatal(row)
	}
	for _, e := range m.snap.Elements {
		if !query.Leaf(e.Type) && len(e.BlockedBy) > 0 {
			t.Fatal("derived projection persisted")
		}
	}
}
func TestCanonicalStatusesRemainExplicitFacts(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	for _, status := range query.Statuses {
		success(t, s, m, `set_status(TASK-001,status=`+status+`)`)
		if element(m, "TASK-001").Status != status || element(m, "STORY-001").Status != "backlog" {
			t.Fatal("status inferred or parent changed")
		}
	}
}
func TestRevisionConflictAndCancellationReturnNoSuccess(t *testing.T) {
	m := &memory{snap: fixture(t)}
	s := service(t, m)
	stale := contract.Revision(9)
	r, err := s.Execute(ctx, contract.Call{ProjectID: "synthetic-board", Operation: parse(t, `rename(TASK-001,name=other)`), Expected: &stale, Audit: contract.AuditContext{AccountID: "a"}})
	code(t, err, "revision_conflict")
	if len(r.JSON) != 0 || m.callbacks != 0 || m.commits != 0 {
		t.Fatal("stale callback or payload")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Execute(canceled, contract.Call{Operation: parse(t, `count()`)})
	if !errors.Is(err, context.Canceled) || m.views != 0 {
		t.Fatal("cancellation ignored", err)
	}
}
func TestIDCollisionBoundAndInvalidGeneratorAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name, id  string
		wantCalls int
	}{
		{"invalid", "TASK-001", 1}, {"collision", "TASK-261105-abc123", 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &memory{snap: fixture(t)}
			element(m, "TASK-001").ID = "TASK-261105-abc123"
			n := 0
			s, err := domain.New(m, func() time.Time { return now }, func(string) (string, error) { n++; return test.id, nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = call(t, s, m, `create(task,name=next,parent=STORY-001)`)
			code(t, err, "unavailable")
			if n != test.wantCalls || m.commits != 0 {
				t.Fatal(n, m)
			}
		})
	}
}
func TestImportPreservesFactsWithoutReplayingTransitionsAndIdenticalProvenance(t *testing.T) {
	p := prepared(t)
	// An imported development fact is not a new take-work transition. Its explicit
	// notEstimated value survives; ordinary subsequent entering still guards it.
	p.Snapshot.Elements[3].Status = "development"
	p.Snapshot.Elements[3].Estimate = &contract.Estimate{Kind: "notEstimated"}
	m := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	s := service(t, m)
	audit := contract.AuditContext{AccountID: "current-editor"}
	_, err := s.Import(ctx, "synthetic-board", 0, audit, p)
	if err != nil {
		t.Fatal(err)
	}
	var appended contract.ProvenanceFile
	for _, f := range m.snap.Provenance {
		if strings.Contains(f.Path, "/import-") {
			appended = f
		}
	}
	if len(appended.Data) == 0 {
		t.Fatal("no import digest provenance")
	}
	p.Snapshot.Provenance = append(p.Snapshot.Provenance, appended)
	m2 := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	s2 := service(t, m2)
	_, err = s2.Import(ctx, "synthetic-board", 0, audit, p)
	if err != nil || len(m2.snap.Provenance) != 3 {
		t.Fatal(err, m2.snap.Provenance)
	}
	// A different historical import record is inert; only this digest's transfer
	// record can supply source metadata. Live audit still uses current editor.
	if m2.lastAudit.AccountID != "current-editor" {
		t.Fatal("provenance became audit actor")
	}
	_, err = s2.Import(ctx, "synthetic-board", 1, contract.AuditContext{}, p)
	code(t, err, "unauthorized")
}
