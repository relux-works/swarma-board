package query

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

func TestSelectorShapesAndNullEstimatePreservation(t *testing.T) {
	at := time.Date(2026, 11, 5, 15, 0, 0, 0, time.FixedZone("offset", 3600))
	elements := []contract.Element{
		{ID: "EPIC-001", Type: "epic", Status: "backlog"},
		{ID: "STORY-001", Type: "story", ParentID: "EPIC-001", Status: "backlog"},
		{ID: "TASK-001", Type: "task", ParentID: "STORY-001", Status: "done", CreatedAt: &at, Checklist: []contract.ChecklistItem{{Text: "one", Checked: true}}, ResourceRefs: []contract.ResourceRef{{Name: "r", URI: "urn:synthetic:r", Type: "outcome"}}},
		{ID: "TASK-002", Type: "task", ParentID: "STORY-001", Status: "backlog", BlockedBy: []string{"TASK-001"}, Estimate: &contract.Estimate{Kind: "estimated", Scale: &contract.EstimateScale{Kind: "fibonacci", Number: 0}}},
	}
	b := Index(elements)
	row := b.Select(elements[2], Fields, false)
	if row["created"] != "2026-11-05T14:00:00Z" || row["updated"] != "" || row["estimate"].(contract.Estimate).Kind != "notEstimated" || row["review"] != "required" || row["task_class"] != "code" {
		t.Fatal(row)
	}
	if !reflect.DeepEqual(row["checklist"], []map[string]any{{"text": "one", "done": true}}) || len(row["preconditionResources"].([]map[string]string)) != 0 || len(row["outcomeResources"].([]map[string]string)) != 1 {
		t.Fatal(row)
	}
	rollup := b.Estimate(elements[0]).(map[string]any)
	if rollup["kind"] != "partiallyEstimated" || rollup["notEstimatedCount"] != "1/2" || !reflect.DeepEqual(rollup["unestimatedTaskIds"], []string{"TASK-001"}) {
		t.Fatal(rollup)
	}
	if elements[2].Estimate != nil {
		t.Fatal("selector modified legacy null")
	}
	phase := b.Select(elements[3], []string{"blockedBy", "isBlocked"}, true)
	if !reflect.DeepEqual(phase["blockedBy"], []string{}) || phase["isBlocked"] != false {
		t.Fatal(phase)
	}
	full := b.Select(elements[3], []string{"blockedBy"}, false)
	if !reflect.DeepEqual(full["blockedBy"], []string{"TASK-001"}) {
		t.Fatal(full)
	}
	// Missing edges stay unresolved; no authority marker can satisfy them.
	elements[3].BlockedBy = []string{"TASK-999"}
	b = Index(elements)
	if !b.Blocked(elements[3]) {
		t.Fatal("missing blocker satisfied")
	}
}
func TestRollupDoesNotOverflowLeafIntegerBounds(t *testing.T) {
	max := math.MaxInt
	elements := []contract.Element{{ID: "STORY-001", Type: "story"}, {ID: "TASK-001", Type: "task", ParentID: "STORY-001", Estimate: &contract.Estimate{Kind: "estimated", Scale: &contract.EstimateScale{Kind: "fibonacci", Number: max}}}, {ID: "TASK-002", Type: "task", ParentID: "STORY-001", Estimate: &contract.Estimate{Kind: "estimated", Scale: &contract.EstimateScale{Kind: "fibonacci", Number: max}}}}
	value := Index(elements).Estimate(elements[0])
	data, err := json.Marshal(value)
	if err != nil || string(data) != `{"kind":"estimated","scale":{"kind":"fibonacci","number":18446744073709551614}}` {
		t.Fatal(string(data), err)
	}
}
