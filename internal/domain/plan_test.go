package domain_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/domain"
	"github.com/relux-works/swarma-board/internal/query"
)

func plan(t *testing.T, snapshot contract.Snapshot, input string) domain.Plan {
	t.Helper()
	p, err := domain.BuildPlan(snapshot, parse(t, input))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func waves(p domain.Plan) [][]string {
	out := [][]string{}
	for _, phase := range p.Phases {
		ids := []string{}
		for _, r := range phase.Elements {
			ids = append(ids, r["id"].(string))
		}
		out = append(out, ids)
	}
	return out
}
func TestA03WavesCriticalPathAndEstimateIndependence(t *testing.T) {
	snap := fixture(t)
	p := plan(t, snap, `plan(){planning}`)
	want := [][]string{{"TASK-001", "TASK-002"}, {"TASK-003"}, {"TASK-004"}}
	critical := []string{"TASK-001", "TASK-003", "TASK-004"}
	if !p.Usable || !reflect.DeepEqual(waves(p), want) || !reflect.DeepEqual(p.CriticalPath, critical) || p.Summary.TotalElements != 4 || p.Summary.TotalPhases != 3 {
		t.Fatalf("%+v", p)
	}
	if !reflect.DeepEqual(p.Phases[0].Containers, []string{"EPIC-001", "STORY-001"}) || !reflect.DeepEqual(p.Phases[1].Containers, []string{"EPIC-001", "STORY-002"}) {
		t.Fatal("ancestor context", p.Phases)
	}
	for i := range snap.Elements {
		if query.Leaf(snap.Elements[i].Type) {
			snap.Elements[i].Estimate = &contract.Estimate{Kind: "estimated", Scale: &contract.EstimateScale{Kind: "fibonacci", Number: 999999}}
		}
	}
	q := plan(t, snap, `plan(){planning}`)
	if !reflect.DeepEqual(waves(p), waves(q)) || !reflect.DeepEqual(p.CriticalPath, q.CriticalPath) {
		t.Fatal("estimate weighted path")
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	_ = json.Unmarshal(encoded, &wire)
	for _, k := range []string{"scope", "usable", "summary", "phases", "criticalPath", "dependency_problems", "task_cycles", "projection_cycles"} {
		if _, ok := wire[k]; !ok {
			t.Fatal("missing wire field", k)
		}
	}
	if len(wire) != 8 {
		t.Fatal(wire)
	}
}
func TestScopeDefaultsSetsClosureAndActiveResolution(t *testing.T) {
	snap := fixture(t)
	p := plan(t, snap, `plan(TASK-003){id}`)
	if p.Scope.Mode != "related" || p.Summary.TotalElements != 4 || !reflect.DeepEqual(p.Scope.IDs, []string{"TASK-003"}) {
		t.Fatal(p)
	}
	p = plan(t, snap, `plan(STORY-001){id}`)
	if p.Scope.Mode != "children" || p.Summary.TotalElements != 2 || !p.Usable {
		t.Fatal(p)
	}
	p = plan(t, snap, `plan([STORY-001,TASK-003,STORY-001]){id}`)
	if p.Scope.Mode != "mixed" || p.Scope.Type != "set" || p.Scope.ID != "STORY-001+TASK-003" || p.Scope.Name != "Scope set (2 roots)" || p.Summary.TotalElements != 4 {
		t.Fatal(p)
	}
	p = plan(t, snap, `plan([STORY-001,TASK-002],mode=children){id}`)
	if p.Scope.Mode != "children" || p.Summary.TotalElements != 2 || p.Scope.Type != "set" {
		t.Fatal(p)
	}
	// Terminal blockers outside the scope resolve against the full snapshot.
	snap.Elements[3].Status = "done"
	snap.Elements[4].Status = "closed"
	p = plan(t, snap, `plan(STORY-002,active=true){planning}`)
	if !p.Usable || !reflect.DeepEqual(waves(p), [][]string{{"TASK-003"}, {"TASK-004"}}) || p.Summary.TotalElements != 2 {
		t.Fatal(p)
	}
	if got := p.Phases[0].Elements[0]["blockedBy"]; !reflect.DeepEqual(got, []string{}) {
		t.Fatal("phase displayed terminal blockers", got)
	}
	p = plan(t, snap, `plan(active=true){id}`)
	if p.Summary.TotalElements != 2 || !p.Usable {
		t.Fatal(p)
	}
	_, err := domain.BuildPlan(snap, parse(t, `plan(TASK-999)`))
	code(t, err, "not_found")
	p = plan(t, contract.Snapshot{}, `plan(){id}`)
	if !p.Usable || len(p.Phases) != 0 || len(p.CriticalPath) != 0 || p.Scope.Type != "project" || p.Scope.Mode != "children" || p.Scope.IDs == nil {
		t.Fatal(p)
	}
	snap.Elements = snap.Elements[:5]
	p = plan(t, snap, `plan(STORY-001){id}`)
	if len(p.CriticalPath) != 0 || len(p.Phases) != 1 {
		t.Fatal("isolated path", p)
	}
}
func TestA04A05FullSCCMembersAndDependencyDiagnostics(t *testing.T) {
	snap := fixture(t)
	snap.Elements[3].BlockedBy = []string{"TASK-003"}
	p := plan(t, snap, `plan(){id}`)
	if p.Usable || len(p.Phases) != 0 || len(p.CriticalPath) != 0 || len(p.TaskCycles) != 1 {
		t.Fatal(p)
	}
	cycle := p.TaskCycles[0]
	if !reflect.DeepEqual(cycle.Members, []string{"TASK-001", "TASK-003"}) || !reflect.DeepEqual(cycle.Chain, []string{"TASK-001", "TASK-003", "TASK-001"}) || len(cycle.Links) != 2 {
		t.Fatal("SCC includes downstream nodes", cycle)
	}
	// Dangling, scope and terminal inconsistency remain separate reasons.
	for _, test := range []struct {
		name, input, reason string
		change              func(*contract.Snapshot)
	}{
		{"out of scope", `plan(TASK-003,mode=children){id}`, "out-of-scope", func(*contract.Snapshot) {}},
		{"dangling", `plan(){id}`, "dangling", func(s *contract.Snapshot) { s.Elements[5].BlockedBy = []string{"TASK-999"} }},
		{"terminal active", `plan(active=true){id}`, "resolved-element-has-active-blocker", func(s *contract.Snapshot) { s.Elements[5].Status = "closed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			snap := fixture(t)
			test.change(&snap)
			p := plan(t, snap, test.input)
			if p.Usable || len(p.Phases) != 0 || len(p.CriticalPath) != 0 || len(p.DependencyProblems) == 0 {
				t.Fatal(p)
			}
			for _, problem := range p.DependencyProblems {
				if problem.Reason != test.reason {
					t.Fatal(problem)
				}
			}
		})
	}
	// Reversing input and edges must not perturb diagnostics or usable waves.
	reversed := clone(snap)
	for i, j := 0, len(reversed.Elements)-1; i < j; i, j = i+1, j-1 {
		reversed.Elements[i], reversed.Elements[j] = reversed.Elements[j], reversed.Elements[i]
	}
	q := plan(t, reversed, `plan(){id}`)
	if !reflect.DeepEqual(p, q) {
		t.Fatal("nondeterministic plan")
	}
}
func TestAdvisoryProjectionCyclesDoNotBlockTaskPlans(t *testing.T) {
	snap := fixture(t)
	// TASK-001 -> TASK-003 -> TASK-002 is a DAG whose story projection cycles.
	snap.Elements[5].BlockedBy = []string{"TASK-001"}
	snap.Elements[4].BlockedBy = []string{"TASK-003"}
	snap.Elements[6].BlockedBy = nil
	p := plan(t, snap, `plan(){id}`)
	if !p.Usable || len(p.TaskCycles) != 0 || len(p.ProjectionCycles) != 1 {
		t.Fatal(p)
	}
	if !reflect.DeepEqual(p.ProjectionCycles[0].Members, []string{"STORY-001", "STORY-002"}) {
		t.Fatal(p.ProjectionCycles)
	}
	// Manual edges remain separately stored and likewise advisory.
	snap = fixture(t)
	snap.Elements[1].ManualBlockedBy = []string{"STORY-002"}
	p = plan(t, snap, `plan(){id}`)
	if !p.Usable || len(p.ProjectionCycles) != 1 {
		t.Fatal(p)
	}
}
func TestCriticalPathFullSequenceLexicalTie(t *testing.T) {
	elements := []contract.Element{}
	for _, id := range []string{"TASK-001", "TASK-002", "TASK-003", "TASK-009", "TASK-010"} {
		elements = append(elements, contract.Element{ID: id, Type: "task", Status: "backlog"})
	}
	elements[3].BlockedBy = []string{"TASK-001"}
	elements[2].BlockedBy = []string{"TASK-002"}
	elements[4].BlockedBy = []string{"TASK-009", "TASK-003"}
	p := plan(t, contract.Snapshot{Elements: elements}, `plan(){id}`)
	if !reflect.DeepEqual(p.CriticalPath, []string{"TASK-001", "TASK-009", "TASK-010"}) {
		t.Fatal(p.CriticalPath)
	}
	// Equal-length choices after a shared root compare the next full ID first.
	elements[2].BlockedBy = []string{"TASK-001"}
	p = plan(t, contract.Snapshot{Elements: elements}, `plan(){id}`)
	if !reflect.DeepEqual(p.CriticalPath, []string{"TASK-001", "TASK-003", "TASK-010"}) {
		t.Fatal(p.CriticalPath)
	}
}
func TestPlannerAgainstExhaustiveSmallDAGOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(27))
	for sample := 0; sample < 50; sample++ {
		nodes := make([]contract.Element, 8)
		out := map[string][]string{}
		for i := range nodes {
			nodes[i] = contract.Element{ID: fmt.Sprintf("TASK-%03d", i+1), Type: "task", Status: "backlog"}
			for j := 0; j < i; j++ {
				if rng.Intn(4) == 0 {
					nodes[i].BlockedBy = append(nodes[i].BlockedBy, nodes[j].ID)
					out[nodes[j].ID] = append(out[nodes[j].ID], nodes[i].ID)
				}
			}
		}
		best := []string{}
		var walk func([]string)
		walk = func(path []string) {
			if len(path) > 1 && (len(path) > len(best) || len(path) == len(best) && lexLess(path, best)) {
				best = append([]string{}, path...)
			}
			for _, next := range out[path[len(path)-1]] {
				walk(append(append([]string{}, path...), next))
			}
		}
		for _, n := range nodes {
			walk([]string{n.ID})
		}
		rng.Shuffle(len(nodes), func(i, j int) { nodes[i], nodes[j] = nodes[j], nodes[i] })
		p := plan(t, contract.Snapshot{Elements: nodes}, `plan(){id}`)
		if !p.Usable || !reflect.DeepEqual(p.CriticalPath, best) {
			t.Fatalf("sample %d got %v want %v", sample, p.CriticalPath, best)
		}
		phaseByID := map[string]int{}
		for _, phase := range p.Phases {
			ids := []string{}
			for _, row := range phase.Elements {
				id := row["id"].(string)
				ids = append(ids, id)
				phaseByID[id] = phase.Number
			}
			if !sort.StringsAreSorted(ids) {
				t.Fatal("unsorted wave")
			}
		}
		for _, n := range nodes {
			expected := 1
			for _, b := range n.BlockedBy {
				if phaseByID[b]+1 > expected {
					expected = phaseByID[b] + 1
				}
			}
			if phaseByID[n.ID] != expected {
				t.Fatal("wrong Kahn wave", n.ID)
			}
		}
	}
}
func lexLess(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func TestBoundedBoardLongChain(t *testing.T) {
	if testing.Short() {
		t.Skip("bounded-board chain")
	}
	nodes := make([]contract.Element, 10000)
	for i := range nodes {
		nodes[i] = contract.Element{ID: fmt.Sprintf("TASK-%05d", i), Type: "task", Status: "backlog"}
		if i > 0 {
			nodes[i].BlockedBy = []string{nodes[i-1].ID}
		}
	}
	p := plan(t, contract.Snapshot{Elements: nodes}, `plan(){id}`)
	if !p.Usable || len(p.Phases) != 10000 || len(p.CriticalPath) != 10000 || p.CriticalPath[0] != "TASK-00000" || p.CriticalPath[9999] != "TASK-09999" {
		t.Fatal("bounded board chain failed")
	}
}

func TestFullGraphCycleDiagnosticsAndBranchingWitness(t *testing.T) {
	snap := fixture(t)
	// One SCC has two loops sharing TASK-001; the witness must visit both arms.
	snap.Elements[3].BlockedBy = []string{"TASK-002", "TASK-003"}
	snap.Elements[4].BlockedBy = []string{"TASK-001"}
	snap.Elements[5].BlockedBy = []string{"TASK-001"}
	p := plan(t, snap, `plan(TASK-004,mode=children){id}`)
	if p.Usable || len(p.TaskCycles) != 1 || !reflect.DeepEqual(p.TaskCycles[0].Members, []string{"TASK-001", "TASK-002", "TASK-003"}) {
		t.Fatal(p)
	}
	if !reflect.DeepEqual(p.TaskCycles[0].Chain, []string{"TASK-001", "TASK-002", "TASK-001", "TASK-003", "TASK-001"}) {
		t.Fatal(p.TaskCycles[0])
	}
}
func TestProjectionCyclesReportInducingStoredLinks(t *testing.T) {
	snap := fixture(t)
	snap.Elements[5].BlockedBy = []string{"TASK-001"}
	snap.Elements[4].BlockedBy = []string{"TASK-003"}
	snap.Elements[6].BlockedBy = nil
	p := plan(t, snap, `plan(){id}`)
	want := []domain.Link{{BlockedID: "STORY-002", BlockerID: "STORY-001"}, {BlockedID: "TASK-002", BlockerID: "TASK-003"}, {BlockedID: "TASK-003", BlockerID: "TASK-001"}}
	if len(p.ProjectionCycles) != 1 || !reflect.DeepEqual(p.ProjectionCycles[0].Links, want) {
		t.Fatal(p.ProjectionCycles)
	}
}
