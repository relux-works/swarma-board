package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/query"
)

type Link struct {
	BlockedID string `json:"blocked_id"`
	BlockerID string `json:"blocker_id"`
}
type Cycle struct {
	Members []string `json:"members"`
	Chain   []string `json:"chain"`
	Links   []Link   `json:"links"`
}
type Problem struct {
	ElementID string `json:"elementId"`
	BlockerID string `json:"blockerId"`
	Reason    string `json:"reason"`
}
type Phase struct {
	Number     int              `json:"phase"`
	Elements   []map[string]any `json:"elements"`
	Containers []string         `json:"containers"`
}
type Scope struct {
	ID     string   `json:"id"`
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	IDs    []string `json:"ids"`
	Mode   string   `json:"mode"`
	Active bool     `json:"active"`
}
type Plan struct {
	Scope   Scope `json:"scope"`
	Usable  bool  `json:"usable"`
	Summary struct {
		TotalElements int `json:"totalElements"`
		TotalPhases   int `json:"totalPhases"`
	} `json:"summary"`
	Phases             []Phase   `json:"phases"`
	CriticalPath       []string  `json:"criticalPath"`
	DependencyProblems []Problem `json:"dependency_problems"`
	TaskCycles         []Cycle   `json:"task_cycles"`
	ProjectionCycles   []Cycle   `json:"projection_cycles"`
}

// BuildPlan is pure: full-snapshot blockers are resolved before active filtering.
func BuildPlan(snapshot contract.Snapshot, op contract.Operation) (Plan, error) {
	b := query.Index(snapshot.Elements)
	p := Plan{Usable: true, Phases: []Phase{}, CriticalPath: []string{}, DependencyProblems: []Problem{}, TaskCycles: []Cycle{}, ProjectionCycles: []Cycle{}}
	p.Scope = Scope{Type: "project", Name: "Project", IDs: []string{}, Mode: "children", Active: query.Bool(op, "active")}
	roots := []string{}
	set := false
	if raw, ok := op.Args["scope"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			roots = append(roots, s)
		} else {
			set = true
			if json.Unmarshal(raw, &roots) != nil || len(roots) == 0 {
				return p, query.Error("query_error", "Invalid scope")
			}
		}
	}
	for i, id := range roots {
		e, ok := b.Find(id)
		if !ok {
			return p, query.Error("not_found", "Scope element not found")
		}
		roots[i] = e.ID
	}
	roots = query.Unique(roots)
	p.Scope.IDs = roots
	selected := map[string]bool{}
	modes := map[string]bool{}
	if len(roots) == 0 {
		for _, id := range b.IDs {
			if query.Leaf(b.Elements[id].Type) {
				selected[id] = true
			}
		}
		if m := query.String(op, "mode"); m != "" {
			p.Scope.Mode = m
		}
	}
	for _, root := range roots {
		mode := query.String(op, "mode")
		if mode == "" {
			mode = "children"
			if query.Leaf(b.Elements[root].Type) {
				mode = "related"
			}
		}
		modes[mode] = true
		seed := append([]string{root}, b.Descendants(root)...)
		queue := []string{}
		local := map[string]bool{}
		for _, id := range seed {
			if query.Leaf(b.Elements[id].Type) {
				local[id] = true
				queue = append(queue, id)
			}
		}
		if mode == "related" {
			for len(queue) > 0 {
				id := queue[0]
				queue = queue[1:]
				neighbors := append(append([]string{}, b.Elements[id].BlockedBy...), b.Inverse[id]...)
				for _, raw := range neighbors {
					v := b.Canonical(raw)
					e, ok := b.Elements[v]
					if ok && query.Leaf(e.Type) && !local[v] {
						local[v] = true
						queue = append(queue, v)
					}
				}
			}
		}
		for id := range local {
			selected[id] = true
		}
	}
	if len(roots) > 0 {
		p.Scope.ID = strings.Join(roots, "+")
		if set || len(roots) > 1 {
			p.Scope.Type = "set"
			p.Scope.Name = fmt.Sprintf("Scope set (%d roots)", len(roots))
		} else {
			e := b.Elements[roots[0]]
			p.Scope.Type = e.Type
			p.Scope.Name = e.Title
			if p.Scope.Name == "" {
				p.Scope.Name = e.Name
			}
		}
		if len(modes) > 1 {
			p.Scope.Mode = "mixed"
		} else {
			for m := range modes {
				p.Scope.Mode = m
			}
		}
	}
	nodes := []string{}
	all := []string{}
	for _, id := range b.IDs {
		if selected[id] {
			all = append(all, id)
			if !p.Scope.Active || !query.Terminal(b.Elements[id].Status) {
				nodes = append(nodes, id)
			}
		}
	}
	// SCC diagnostics use the full task graph, including terminal and
	// out-of-scope nodes; a scoped read must not hide corrupt dependency cycles.
	rawGraph := map[string][]string{}
	leaves := []string{}
	containers := []string{}
	projection := map[string][]string{}
	origins := map[Link][]Link{}
	for _, id := range b.IDs {
		e := b.Elements[id]
		if !query.Leaf(e.Type) {
			containers = append(containers, id)
			for _, ref := range e.ManualBlockedBy {
				v, ok := b.Find(ref)
				if ok && !query.Leaf(v.Type) {
					edge := Link{id, v.ID}
					projection[id] = append(projection[id], v.ID)
					origins[edge] = append(origins[edge], edge)
				}
			}
			continue
		}
		leaves = append(leaves, id)
		for _, ref := range e.BlockedBy {
			v, ok := b.Find(ref)
			if !ok || !query.Leaf(v.Type) {
				continue
			}
			rawGraph[id] = append(rawGraph[id], v.ID)
			a, c := b.Ancestors(id), b.Ancestors(v.ID)
			for i := 0; i < len(a) && i < len(c); i++ {
				if a[i] != c[i] {
					edge := Link{a[i], c[i]}
					projection[a[i]] = append(projection[a[i]], c[i])
					origins[edge] = append(origins[edge], Link{id, v.ID})
				}
			}
		}
	}
	p.TaskCycles = cycles(leaves, rawGraph)
	p.ProjectionCycles = cycles(containers, projection)
	for i := range p.ProjectionCycles {
		sources := map[Link]bool{}
		for _, edge := range p.ProjectionCycles[i].Links {
			for _, source := range origins[edge] {
				sources[source] = true
			}
		}
		p.ProjectionCycles[i].Links = []Link{}
		for source := range sources {
			p.ProjectionCycles[i].Links = append(p.ProjectionCycles[i].Links, source)
		}
		sort.Slice(p.ProjectionCycles[i].Links, func(a, b int) bool {
			x, y := p.ProjectionCycles[i].Links[a], p.ProjectionCycles[i].Links[b]
			if x.BlockedID != y.BlockedID {
				return x.BlockedID < y.BlockedID
			}
			return x.BlockerID < y.BlockerID
		})
	}
	in := map[string]int{}
	adj := map[string][]string{}
	candidates := map[string]bool{}
	for _, id := range nodes {
		candidates[id] = true
	}
	for _, id := range all {
		e := b.Elements[id]
		for _, ref := range query.Unique(e.BlockedBy) {
			v := b.Canonical(ref)
			blocker, exists := b.Elements[v]
			reason := ""
			if !exists || !query.Leaf(blocker.Type) {
				reason = "dangling"
			} else if !query.Terminal(blocker.Status) {
				if query.Terminal(e.Status) {
					reason = "resolved-element-has-active-blocker"
				} else if !selected[v] {
					reason = "out-of-scope"
				}
			}
			if reason != "" {
				p.DependencyProblems = append(p.DependencyProblems, Problem{id, v, reason})
				continue
			}
			// With active=false, satisfied in-scope blockers remain facts in the DAG.
			if candidates[id] && candidates[v] {
				in[id]++
				adj[v] = append(adj[v], id)
			}
		}
	}
	sort.Slice(p.DependencyProblems, func(i, j int) bool {
		a, c := p.DependencyProblems[i], p.DependencyProblems[j]
		if a.ElementID != c.ElementID {
			return a.ElementID < c.ElementID
		}
		if a.BlockerID != c.BlockerID {
			return a.BlockerID < c.BlockerID
		}
		return a.Reason < c.Reason
	})
	p.Summary.TotalElements = len(nodes)
	if len(p.TaskCycles) > 0 || len(p.DependencyProblems) > 0 {
		p.Usable = false
		return p, nil
	}
	ready := []string{}
	for _, id := range nodes {
		adj[id] = query.Unique(adj[id])
		if in[id] == 0 {
			ready = append(ready, id)
		}
	}
	order := []string{}
	for len(ready) > 0 {
		sort.Strings(ready)
		phase := Phase{Number: len(p.Phases) + 1, Elements: []map[string]any{}, Containers: []string{}}
		next := []string{}
		for _, id := range ready {
			order = append(order, id)
			phase.Elements = append(phase.Elements, b.Select(b.Elements[id], op.Fields, true))
			phase.Containers = append(phase.Containers, b.Ancestors(id)...)
			for _, v := range adj[id] {
				in[v]--
				if in[v] == 0 {
					next = append(next, v)
				}
			}
		}
		phase.Containers = query.Unique(phase.Containers)
		p.Phases = append(p.Phases, phase)
		ready = next
	}
	if len(order) != len(nodes) {
		p.Usable = false
		p.Phases = []Phase{}
		return p, nil
	}
	p.Summary.TotalPhases = len(p.Phases)
	distance := map[string]int{}
	successor := map[string]string{}
	start := ""
	longest := 0
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		for _, v := range adj[id] {
			n := distance[v] + 1
			if n > distance[id] || (n == distance[id] && (successor[id] == "" || v < successor[id])) {
				distance[id] = n
				successor[id] = v
			}
		}
		if distance[id] > longest || (distance[id] == longest && distance[id] > 0 && (start == "" || id < start)) {
			start = id
			longest = distance[id]
		}
	}
	if longest > 0 {
		for v := start; v != ""; v = successor[v] {
			p.CriticalPath = append(p.CriticalPath, v)
		}
	}
	return p, nil
}

// cycles uses Tarjan SCCs, excluding downstream-only vertices. Both neighbor
// order and the closed witness chain are reproducible across map iteration.
func cycles(nodes []string, adj map[string][]string) []Cycle {
	out := []Cycle{}
	allowed := map[string]bool{}
	for _, v := range nodes {
		allowed[v] = true
		adj[v] = query.Unique(adj[v])
	}
	index := map[string]int{}
	low := map[string]int{}
	on := map[string]bool{}
	stack := []string{}
	counter := 0
	var visit func(string)
	visit = func(v string) {
		counter++
		index[v] = counter
		low[v] = counter
		stack = append(stack, v)
		on[v] = true
		for _, w := range adj[v] {
			if !allowed[w] {
				continue
			}
			if index[w] == 0 {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if on[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] != index[v] {
			return
		}
		members := []string{}
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			members = append(members, w)
			if w == v {
				break
			}
		}
		sort.Strings(members)
		if len(members) == 1 && !query.Contains(adj[v], v) {
			return
		}
		inside := map[string]bool{}
		for _, m := range members {
			inside[m] = true
		}
		links := []Link{}
		for _, m := range members {
			for _, w := range adj[m] {
				if inside[w] {
					links = append(links, Link{m, w})
				}
			}
		}
		// Connect sorted members with deterministic shortest paths, then
		// close the walk. Branching SCCs may require repeated vertices.
		path := []string{members[0]}
		for _, target := range append(append([]string{}, members[1:]...), members[0]) {
			start := path[len(path)-1]
			queue := []string{start}
			seen := map[string]bool{start: true}
			parent := map[string]string{}
			for len(queue) > 0 && !seen[target] {
				cur := queue[0]
				queue = queue[1:]
				for _, next := range adj[cur] {
					if inside[next] && !seen[next] {
						seen[next] = true
						parent[next] = cur
						queue = append(queue, next)
					}
				}
			}
			tail := []string{}
			for v := target; v != start; v = parent[v] {
				tail = append(tail, v)
			}
			for i := len(tail) - 1; i >= 0; i-- {
				path = append(path, tail[i])
			}
		}
		if len(path) == 1 {
			path = append(path, path[0])
		}
		out = append(out, Cycle{members, path, links})
	}
	for _, v := range query.Unique(nodes) {
		if index[v] == 0 {
			visit(v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Members[0] < out[j].Members[0] })
	return out
}
