package cli

import (
	"encoding/json"
	"testing"
)

func TestFrozenReadGrammar(t *testing.T) {
	for _, q := range []string{`get(TASK-a) { full }`, `get(id=TASK-a){id title preconditionResources}`, `list(type=task,status=development,parent=STORY-a,assignee="synthetic label",blocked=true,skip=0,take=1000){planning}`, `count(blocked=false)`, `summary()`, `plan()`, `plan(TASK-a,mode=related,active=true){progress}`, `plan(scope=[TASK-a,"TASK-b"],active=false)`, `plan([TASK-a,TASK-b])`} {
		t.Run(q, func(t *testing.T) {
			if _, err := Validate(q, false); err != nil {
				t.Fatal(err)
			}
		})
	}
	op, err := Validate(`plan(scope=[TASK-a,TASK-b])`, false)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if json.Unmarshal(op.Args["scope"], &ids) != nil || len(ids) != 2 {
		t.Fatal("list lost")
	}
}
func TestFrozenMutationGrammar(t *testing.T) {
	queries := []string{`create(task,name="Synthetic task",parent=STORY-a,description="text",scope="scope",ac="criteria",review=required,task_class=code)`, `set_details(TASK-a,title="",description="",scope="",ac="",review=light,task_class=docs)`, `rename(TASK-a,name="new name")`, `assign(TASK-a,agent="synthetic-agent")`, `unassign(TASK-a)`, `set_status(TASK-a,status=development)`, `set_estimate(TASK-a,estimate=estimated,scale=fibonacci,number=4)`, `set_estimate(TASK-a,estimate=notEstimated)`, `link(TASK-a,blocked_by=TASK-b)`, `unlink(TASK-a,blocked_by=TASK-b)`, `check_item(TASK-a,item=1)`, `uncheck_item(TASK-a,item=2)`, `add_checklist_item(TASK-a,text="check")`, `remove_checklist_item(TASK-a,item=1)`, `set_notes(TASK-a,text="line\nline",set=false)`, `set_parent(TASK-a,parent=STORY-b)`, `delete(TASK-a,confirm=true)`}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			if _, err := Validate(q, true); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(q, false); err == nil {
				t.Fatal("q accepted mutation")
			}
		})
	}
}
func TestRejectGrammarBeforeNetwork(t *testing.T) {
	for _, q := range []string{`get()`, `get(TASK-a,id=TASK-b)`, `get(TASK-a,TASK-b)`, `get(id=TASK-a,TASK-b)`, `get(TASK-a,extra=x)`, `list(task)`, `list(take=0)`, `list(take=1001)`, `list(skip=-1)`, `list(take=9223372036854775808)`, `list(blocked=1)`, `list(status=running)`, `summary(extra=x)`, `summary(){id}`, `count(){}`, `get(TASK-a){unknown}`, `get(TASK-a){children{id}}`, `get(TASK-a);count()`, `spawn()`, `plan(scope="[\"TASK-a\",\"TASK-b\"]")`, `plan(scope=[])`, `plan(scope=[TASK-a,])`, `plan(scope=[TASK-a,TASK-b)`, `plan(mode=[TASK-a])`, `plan(scope=TASK-a,scope=TASK-b)`, `plan(active=True)`, `get("secret body`} {
		t.Run(q, func(t *testing.T) {
			if _, err := Validate(q, false); err == nil {
				t.Fatal("invalid read accepted")
			}
		})
	}
	for _, q := range []string{`count()`, `create(task,"second positional")`, `create(task,name="")`, `create(task,name=x,name=y)`, `create(task,name=x,model=bad)`, `create(task,name=[TASK-a])`, `create(task,name=x,scope=[TASK-a])`, `set_details(TASK-a)`, `assign(TASK-a,agent="")`, `set_estimate(TASK-a,estimate=partiallyEstimated)`, `set_estimate(TASK-a,estimate=estimated)`, `set_estimate(TASK-a,estimate=estimated,scale=fibonacci,number=-1)`, `set_estimate(TASK-a,estimate=notEstimated,number=0)`, `delete(TASK-a,confirm=false)`, `delete(TASK-a)`, `delete(TASK-a,confirm=true){}`, `set_status(TASK-a,status=done){full}`, `check_item(TASK-a,item=0)`} {
		t.Run(q, func(t *testing.T) {
			if _, err := Validate(q, true); err == nil {
				t.Fatal("invalid mutation accepted")
			}
		})
	}
}
