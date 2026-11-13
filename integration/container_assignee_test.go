package integration

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/relux-works/swarma-board/internal/cli"
)

func TestCLIContainerStatusesPreserveLeafAndGrantGuards(t *testing.T) {
	// Real CLI/domain/bbolt accept unassigned explicit container states, retain leaf/grant refusals and preserve states after restart.
	dir := filepath.Join(t.TempDir(), "data")
	h := start(t, dir, false)
	imported(t, h.client(t, "admin"), fixture(t, "native-v1.tar.gz"))
	for _, id := range []string{"EPIC-001", "STORY-001"} {
		for _, status := range []string{"development", "to-review"} {
			before, err := h.app.Service.Snapshot(context.Background(), project)
			if err != nil {
				t.Fatal(err)
			}
			n, out, errout := run(h, "--json", "m", fmt.Sprintf("set_status(%s,status=%s)", id, status), "--if-match", strconv.FormatUint(uint64(before.Revision), 10))
			if n != 0 {
				t.Fatalf("container CLI transition refused: exit=%d out=%s err=%s", n, out, errout)
			}
		}
	}
	before, err := h.app.Service.Snapshot(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"development", "to-review"} {
		n, _, errout := run(h, "--json", "m", "set_status(TASK-002,status="+status+")", "--if-match", strconv.FormatUint(uint64(before.Revision), 10))
		if n == 0 || !bytes.Contains(errout, []byte("not_assigned")) {
			t.Fatal("CLI leaf guard lost", n, string(errout))
		}
	}
	options := h.options("viewer")
	var stdout, stderr bytes.Buffer
	options.Out = &stdout
	options.Err = &stderr
	n := cli.Run(context.Background(), []string{"--json", "m", "set_status(EPIC-001,status=analysis)", "--if-match", strconv.FormatUint(uint64(before.Revision), 10)}, options)
	if n != 3 || !bytes.Contains(stderr.Bytes(), []byte("forbidden")) {
		t.Fatal("container rule bypassed project grant", n, stderr.String())
	}
	after, err := h.app.Service.Snapshot(context.Background(), project)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("CLI refusals changed facts/resources/revision")
	}
	h.close()
	h = start(t, dir, false)
	persisted, err := h.app.Service.Snapshot(context.Background(), project)
	if err != nil || !reflect.DeepEqual(persisted, after) {
		t.Fatal("restart changed container states/labels")
	}
}
