package domain_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/query"
)

func TestAssigneeRuleIsScopedToLeaves(t *testing.T) {
	// Both container kinds may carry unassigned active statuses; both leaf kinds still reject empty/whitespace labels without changes.
	for _, typ := range []string{"epic", "story", "task", "bug"} {
		for _, status := range []string{"development", "to-review"} {
			for _, label := range []string{"", " \t", "assigned-label"} {
				t.Run(fmt.Sprintf("%s/%s/%q", typ, status, label), func(t *testing.T) {
					m := &memory{snap: fixture(t)}
					id := "TASK-001"
					switch typ {
					case "epic":
						id = "EPIC-001"
					case "story":
						id = "STORY-001"
					case "bug":
						for i := range m.snap.Elements {
							e := &m.snap.Elements[i]
							if e.ID == "TASK-001" {
								e.ID = "BUG-001"
								e.Type = "bug"
							}
							for j, ref := range e.BlockedBy {
								if ref == "TASK-001" {
									e.BlockedBy[j] = "BUG-001"
								}
							}
						}
						id = "BUG-001"
					}
					element(m, id).AssignedTo = label
					before := clone(m.snap)
					s := service(t, m)
					_, err := call(t, s, m, fmt.Sprintf("set_status(%s,status=%s)", id, status))
					if (typ == "task" || typ == "bug") && strings.TrimSpace(label) == "" {
						code(t, err, "not_assigned")
						if m.commits != 0 || !reflect.DeepEqual(m.snap, before) {
							t.Fatal("rejected leaf transition changed state")
						}
						return
					}
					if err != nil || m.commits != 1 || element(m, id).Status != status || element(m, id).AssignedTo != label {
						t.Fatal("valid type-aware transition refused/changed label", err)
					}
				})
			}
		}
	}
}

func TestImportPreservesUnassignedActiveContainers(t *testing.T) {
	// Import and ordinary mutation share the same type rule; imported container labels/statuses remain exact.
	p := prepared(t)
	p.Snapshot.Elements[0].Status = "development"
	p.Snapshot.Elements[0].AssignedTo = ""
	p.Snapshot.Elements[1].Status = "to-review"
	p.Snapshot.Elements[1].AssignedTo = ""
	m := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	s := service(t, m)
	_, err := s.Import(ctx, p.Snapshot.Project.ID, 0, contract.AuditContext{AccountID: "fixture-owner", At: now}, p)
	if err != nil || m.commits != 1 || !reflect.DeepEqual(m.snap.Elements, p.Snapshot.Elements) {
		t.Fatal("import changed/refused unassigned containers", err)
	}
}

func TestContainerExceptionRetainsOtherRefusals(t *testing.T) {
	// Type/status, blockers and terminal-descendant restrictions remain independent of the container assignee exception.
	for _, tc := range []struct{ query, want string }{{"set_status(EPIC-001,status=unknown)", "query_error"}, {"set_status(TASK-003,status=reviewing)", "blocked"}, {"set_status(EPIC-001,status=done)", "dependency_conflict"}} {
		m := &memory{snap: fixture(t)}
		before := clone(m.snap)
		op, err := query.New().Parse(tc.query)
		if err == nil {
			rev := m.snap.Revision
			_, err = service(t, m).Execute(ctx, contract.Call{ProjectID: m.snap.Project.ID, Operation: op, Expected: &rev, Audit: contract.AuditContext{AccountID: "fixture-owner"}})
		}
		code(t, err, tc.want)
		if m.commits != 0 || !reflect.DeepEqual(m.snap, before) {
			t.Fatal("refusal changed state")
		}
	}
	p := prepared(t)
	p.Snapshot.Elements[0].Type = "unknown"
	p.Snapshot.Elements[0].Status = "development"
	p.Snapshot.Elements[0].AssignedTo = ""
	m := &memory{snap: contract.Snapshot{Project: p.Snapshot.Project}}
	_, err := service(t, m).Import(ctx, p.Snapshot.Project.ID, 0, contract.AuditContext{AccountID: "fixture-owner"}, p)
	code(t, err, "invalid_snapshot")
	if m.commits != 0 || len(m.snap.Elements) != 0 || len(m.snap.Provenance) != 0 {
		t.Fatal("unknown type gained an exception")
	}
}
