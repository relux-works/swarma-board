package query

import (
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

func Leaf(t string) bool     { return t == "task" || t == "bug" }
func Terminal(s string) bool { return s == "done" || s == "closed" }

// Board is a read-only index over one detached snapshot. Stored edges and
// advisory projections are kept separate. IDs returned here preserve storage casing.
type Board struct {
	Elements                                                map[string]contract.Element
	IDs                                                     []string
	lower                                                   map[string]string
	Children                                                map[string][]string
	DerivedBlockedBy, DerivedBlocks, Inverse, ManualInverse map[string][]string
}

func Index(elements []contract.Element) *Board {
	b := &Board{Elements: map[string]contract.Element{}, lower: map[string]string{}, Children: map[string][]string{}, DerivedBlockedBy: map[string][]string{}, DerivedBlocks: map[string][]string{}, Inverse: map[string][]string{}, ManualInverse: map[string][]string{}}
	for _, e := range elements {
		b.Elements[e.ID] = e
		b.lower[strings.ToLower(e.ID)] = e.ID
		b.IDs = append(b.IDs, e.ID)
	}
	sort.Strings(b.IDs)
	for _, id := range b.IDs {
		e := b.Elements[id]
		if e.ParentID != "" {
			p := b.Canonical(e.ParentID)
			b.Children[p] = append(b.Children[p], id)
		}
		for _, raw := range e.ManualBlockedBy {
			v := b.Canonical(raw)
			b.ManualInverse[v] = append(b.ManualInverse[v], id)
		}
		if !Leaf(e.Type) {
			continue
		}
		for _, raw := range e.BlockedBy {
			v := b.Canonical(raw)
			b.Inverse[v] = append(b.Inverse[v], id)
			blocker, ok := b.Elements[v]
			if !ok || !Leaf(blocker.Type) {
				continue
			}
			a, c := b.Ancestors(id), b.Ancestors(v)
			for i := 0; i < len(a) && i < len(c); i++ {
				if a[i] != c[i] {
					b.DerivedBlockedBy[a[i]] = append(b.DerivedBlockedBy[a[i]], c[i])
					b.DerivedBlocks[c[i]] = append(b.DerivedBlocks[c[i]], a[i])
				}
			}
		}
	}
	for _, m := range []map[string][]string{b.Children, b.Inverse, b.ManualInverse, b.DerivedBlockedBy, b.DerivedBlocks} {
		for k, v := range m {
			m[k] = Unique(v)
		}
	}
	return b
}
func Unique(v []string) []string {
	out := append([]string{}, v...)
	sort.Strings(out)
	r := []string{}
	for _, s := range out {
		if len(r) == 0 || r[len(r)-1] != s {
			r = append(r, s)
		}
	}
	return r
}
func (b *Board) Canonical(id string) string {
	if v, ok := b.lower[strings.ToLower(id)]; ok {
		return v
	}
	return id
}
func (b *Board) Find(id string) (contract.Element, bool) {
	e, ok := b.Elements[b.Canonical(id)]
	return e, ok
}
func (b *Board) Ancestors(id string) []string {
	out := []string{}
	seen := map[string]bool{id: true}
	e, ok := b.Find(id)
	for ok && e.ParentID != "" {
		p := b.Canonical(e.ParentID)
		if seen[p] {
			break
		}
		seen[p] = true
		e, ok = b.Find(p)
		if ok {
			out = append(out, p)
		}
	}
	return out
}
func (b *Board) Descendants(id string) []string {
	out := []string{}
	queue := append([]string{}, b.Children[id]...)
	seen := map[string]bool{id: true}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		queue = append(queue, b.Children[v]...)
	}
	return Unique(out)
}
func (b *Board) Active(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		e, ok := b.Find(id)
		if !ok || !Terminal(e.Status) {
			out = append(out, b.Canonical(id))
		}
	}
	return Unique(out)
}
func (b *Board) Blocked(e contract.Element) bool {
	return Leaf(e.Type) && len(b.Active(e.BlockedBy)) > 0
}
func (b *Board) Advisory(e contract.Element) bool {
	return !Leaf(e.Type) && len(b.Active(append(append([]string{}, e.ManualBlockedBy...), b.DerivedBlockedBy[e.ID]...))) > 0
}
func (b *Board) Estimate(e contract.Element) any {
	if Leaf(e.Type) {
		if e.Estimate == nil {
			return contract.Estimate{Kind: "notEstimated"}
		}
		return e.Estimate
	}
	leaves := []contract.Element{}
	for _, id := range b.Descendants(e.ID) {
		c := b.Elements[id]
		if Leaf(c.Type) {
			leaves = append(leaves, c)
		}
	}
	sum := new(big.Int)
	missing := []string{}
	for _, c := range leaves {
		if c.Estimate != nil && c.Estimate.Kind == "estimated" && c.Estimate.Scale != nil {
			sum.Add(sum, big.NewInt(int64(c.Estimate.Scale.Number)))
		} else {
			missing = append(missing, c.ID)
		}
	}
	rolled := map[string]any{"kind": "estimated", "scale": map[string]any{"kind": "fibonacci", "number": json.Number(sum.String())}}
	if len(missing) == 0 {
		return rolled
	}
	sort.Strings(missing)
	return map[string]any{"kind": "partiallyEstimated", "rolledEstimate": rolled, "notEstimatedCount": strconvCount(len(missing), len(leaves)), "unestimatedTaskIds": missing}
}
func strconvCount(a, b int) string { // JSON's decimal formatting avoids a second enum representation.
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) + "/" + string(y)
}

// Select computes each allowed selector from this snapshot. phase=true filters
// only blockedBy to active blockers; get/list retain every stored task edge.
func (b *Board) Select(e contract.Element, fields []string, phase bool) map[string]any {
	row := map[string]any{}
	for _, f := range fields {
		var v any
		switch f {
		case "id":
			v = e.ID
		case "type":
			v = e.Type
		case "name":
			v = e.Name
		case "title":
			v = e.Title
		case "status":
			v = e.Status
		case "review":
			v = e.Review
			if v == "" {
				v = "required"
			}
		case "task_class":
			v = e.TaskClass
			if v == "" {
				v = "code"
			}
		case "estimate":
			v = b.Estimate(e)
		case "parent":
			v = e.ParentID
		case "assignee":
			v = e.AssignedTo
		case "description":
			v = e.Description
		case "scope":
			v = e.Scope
		case "ac":
			v = e.AcceptanceCriteria
		case "blockedBy":
			if Leaf(e.Type) {
				v = Unique(e.BlockedBy)
			} else {
				v = Unique(append(append([]string{}, e.ManualBlockedBy...), b.DerivedBlockedBy[e.ID]...))
			}
			if phase {
				v = b.Active(v.([]string))
			}
		case "blocks":
			if Leaf(e.Type) {
				v = Unique(b.Inverse[e.ID])
			} else {
				v = Unique(append(append([]string{}, b.ManualInverse[e.ID]...), b.DerivedBlocks[e.ID]...))
			}
		case "derivedBlockedBy":
			v = Unique(b.DerivedBlockedBy[e.ID])
		case "derivedBlocks":
			v = Unique(b.DerivedBlocks[e.ID])
		case "checklist":
			items := []map[string]any{}
			for _, c := range e.Checklist {
				items = append(items, map[string]any{"text": c.Text, "done": c.Checked})
			}
			v = items
		case "notes":
			v = e.Notes
		case "created":
			v = timestamp(e.CreatedAt)
		case "updated":
			v = timestamp(e.UpdatedAt)
		case "isBlocked":
			v = b.Blocked(e)
		case "isAdvisoryBlocked":
			v = b.Advisory(e)
		case "children":
			v = Unique(b.Children[e.ID])
		case "preconditionResources", "outcomeResources":
			want := "precondition"
			if f == "outcomeResources" {
				want = "outcome"
			}
			refs := []map[string]string{}
			for _, r := range e.ResourceRefs {
				if r.Type == want {
					refs = append(refs, map[string]string{"name": r.Name, "uri": r.URI, "description": r.Description})
				}
			}
			v = refs
		}
		row[f] = v
	}
	return row
}
func timestamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
