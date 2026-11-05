package query

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

func TestAllowedOperationsAndTypedArguments(t *testing.T) {
	cases := []string{
		`get(task-001){full}`, `list(type=task,status=blocked,parent=STORY-001,assignee="a b",blocked=true,skip=2,take=1000){planning}`,
		`count(blocked=false)`, `summary()`, `plan([TASK-001,"TASK-002"],active=true){progress}`,
		`create(task,name="New Feature",parent=STORY-001,description="a\nb",scope="",ac="",review=none,task_class=docs)`,
		`set_details(TASK-001,title="",description="",scope="",ac="",review=light,task_class=research)`,
		`rename(TASK-001,name="next")`, `assign(TASK-001,agent="synthetic label")`, `unassign(TASK-001)`,
		`set_status(TASK-001,status=integrating)`, `set_estimate(TASK-001,estimate=estimated,scale=fibonacci,number=4)`,
		`set_estimate(TASK-001,estimate=notEstimated)`, `link(TASK-001,blocked_by=TASK-002)`, `unlink(TASK-001,blocked_by=TASK-002)`,
		`check_item(TASK-001,item=1)`, `uncheck_item(TASK-001,item=1)`, `add_checklist_item(TASK-001,text="check")`,
		`remove_checklist_item(TASK-001,item=2)`, `set_notes(TASK-001,text="; [literal]",set=true)`, `set_parent(TASK-001,parent=STORY-002)`, `delete(TASK-001,confirm=true)`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			op, err := New().Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Validate(op)
			if err != nil || !reflect.DeepEqual(op, again) {
				t.Fatalf("not normalized: %+v %v", again, err)
			}
		})
	}
	op, err := New().Parse(`plan(scope=[TASK-002,TASK-001],mode=related,active=true){minimal}`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if json.Unmarshal(op.Args["scope"], &ids) != nil || len(ids) != 2 || !Bool(op, "active") {
		t.Fatalf("bad typed op %+v", op)
	}
	op, err = New().Parse(`list()`)
	if err != nil || Int(op, "take") != 50 || Int(op, "skip") != 0 {
		t.Fatal(op, err)
	}
}
func TestRejectInvalidDSL(t *testing.T) {
	cases := map[string]string{
		`get(TASK-001);count()`: "unsupported_batch", `get(TASK-001);spawn()`: "unsupported_batch",
		`spawn()`: "unsupported_operation", `agents()`: "unsupported_operation", `models()`: "unsupported_operation", `activity()`: "unsupported_operation", `ledger()`: "unsupported_operation", `handoff()`: "unsupported_operation", `normalize()`: "unsupported_operation", `upload()`: "unsupported_operation",
		`get(TASK-001,id=TASK-002)`: "query_error", `get(id=TASK-001,id=TASK-002)`: "query_error", `get(id=TASK-001,TASK-002)`: "query_error", `create(task,"name")`: "query_error",
		`list(blocked=yes)`: "query_error", `list(skip=9223372036854775808)`: "query_error", `list(take=0)`: "query_error", `list(take=1001)`: "query_error", `list(skip=-1)`: "query_error", `count(skip=0)`: "query_error",
		`plan([])`: "query_error", `plan([TASK-001,])`: "query_error", `plan([id=TASK-001])`: "query_error", `get([TASK-001])`: "query_error", `plan(mode=scheduled)`: "query_error", `plan(active=TRUE)`: "query_error", `plan(scope=[])`: "query_error",
		`set_status(TASK-001,status=accepted)`: "query_error", `create(leaf,name=n)`: "query_error", `create(task,name=n,review=accept)`: "query_error", `create(task,name=n,task_class=runtime)`: "query_error",
		`set_details(TASK-001)`: "query_error", `set_estimate(TASK-001,estimate=partiallyEstimated)`: "query_error", `set_estimate(TASK-001,estimate=estimated,number=3)`: "query_error", `set_estimate(TASK-001,estimate=estimated,scale=fibonacci,number=-1)`: "query_error", `set_estimate(TASK-001,estimate=notEstimated,number=0)`: "query_error",
		`delete(TASK-001)`: "query_error", `delete(TASK-001,confirm=false)`: "query_error", `delete(TASK-001,confirm=1)`: "query_error", `check_item(TASK-001,item=0)`: "query_error",
		`get(TASK-001){integrationCheckpointed}`: "query_error", `get(TASK-001){observability}`: "query_error", `get(TASK-001){missing}`: "query_error", `get(TASK-001){estimate{kind}}`: "query_error", `set_status(TASK-001,status=done){id}`: "query_error", `summary(){}`: "query_error", `count(){id}`: "query_error", `set_notes(TASK-001,text="x",dry_run=true)`: "query_error",
	}
	for input, code := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := New().Parse(input)
			e, ok := err.(*contract.Error)
			if !ok || e.Code != code {
				t.Fatalf("got %v want %s", err, code)
			}
		})
	}
}
func TestPresetCasingAndSafeErrors(t *testing.T) {
	op, err := New().Parse(`get(TASK-001){minimal planning blockedBy}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "status", "name", "blockedBy", "blocks", "derivedBlockedBy", "derivedBlocks", "parent"}
	if !reflect.DeepEqual(op.Fields, want) {
		t.Fatal(op.Fields)
	}
	_, err = New().Parse(`get(TASK-001){BlockedBy}`)
	if err == nil {
		t.Fatal("casing accepted")
	}
	_, err = New().Parse(`set_notes(TASK-001,text="private body",unknown=true)`)
	if err == nil || err.Error() != "Invalid operation arguments or selectors" && err.Error() != "Query rejected" {
		t.Fatal("unsafe error", err)
	}
}
func TestValidateTypedInProcessInputs(t *testing.T) {
	for _, op := range []contract.Operation{
		{Name: "set_status", Args: map[string]json.RawMessage{"id": json.RawMessage(`"TASK-001"`), "status": json.RawMessage(`null`)}},
		{Name: "list", Args: map[string]json.RawMessage{"blocked": json.RawMessage(`"true"`)}},
		{Name: "list", Args: map[string]json.RawMessage{"skip": json.RawMessage(`1.0`)}},
		{Name: "plan", Args: map[string]json.RawMessage{"scope": json.RawMessage(`null`)}},
	} {
		if _, err := Validate(op); err == nil {
			t.Fatalf("accepted %+v", op)
		}
	}
}
