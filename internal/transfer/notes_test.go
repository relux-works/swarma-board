package transfer

import (
	"bytes"
	"context"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

func TestLegacyNotesHeadingsSurviveNativeRoundtrip(t *testing.T) {
	files := unpack(t, fixture(t, "legacy-v2"))
	const taskPath = "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a/progress.md"
	const notes = "Investigation.\n\n## Findings\nKeep this as notes.\n\n## Next steps\nKeep this too."
	files[taskPath] = append(files[taskPath], []byte("\n## Notes\n"+notes+"\n\n## Assigned To\nsynthetic-person\n")...)
	prepared := mustPrepare(t, pack(t, files))
	check := func(p contract.PreparedImport) {
		t.Helper()
		for _, element := range p.Snapshot.Elements {
			if element.ID == "TASK-001" {
				if element.Notes != notes || element.AssignedTo != "synthetic-person" {
					t.Fatalf("notes or neighboring field changed: %+v", element)
				}
				return
			}
		}
		t.Fatal("TASK-001 missing")
	}
	check(prepared)
	var native bytes.Buffer
	if err := New("dev").Encode(context.Background(), &native, prepared.Snapshot); err != nil {
		t.Fatal(err)
	}
	check(mustPrepare(t, native.Bytes()))
}
