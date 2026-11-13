package transfer

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

func TestLegacyDescriptionHeadingsSurviveNativeRoundtrip(t *testing.T) {
	// A legacy description preserves embedded Markdown; all other facts match the valid control through native v1.
	files := unpack(t, fixture(t, "legacy-v2"))
	baseline := mustPrepare(t, pack(t, files))
	const taskPath = "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a/README.md"
	const description = "Requirement.\n\n## Background\n> Preserve «the exact quote».\n\n## Status\ndone\n## Assigned To\nother-label\n### Detail\nKeep this line."
	files[taskPath] = []byte("# Title\n\n## Description\n" + description + "\n\n## Scope\nBounded\n\n## Acceptance Criteria\nRetained\n")
	prepared := mustPrepare(t, pack(t, files))
	want := baseline.Snapshot.Elements
	for i := range want {
		if want[i].ID == "TASK-001" {
			want[i].Title = "Title"
			want[i].Description = description
			want[i].Scope = "Bounded"
			want[i].AcceptanceCriteria = "Retained"
		}
	}
	if !reflect.DeepEqual(prepared.Snapshot.Elements, want) || !reflect.DeepEqual(prepared.Report.Exclusions, baseline.Report.Exclusions) {
		t.Fatal("description or unrelated facts changed")
	}
	var native bytes.Buffer
	if err := New("dev").Encode(context.Background(), &native, prepared.Snapshot); err != nil {
		t.Fatal(err)
	}
	if got := mustPrepare(t, native.Bytes()); !reflect.DeepEqual(got.Snapshot.Elements, want) {
		t.Fatal("native v1 roundtrip changed facts")
	}
}

func TestLegacyDescriptionOuterSectionRefusals(t *testing.T) {
	// A valid archive reaches the Markdown gate; forbidden outer fields refuse without a partially prepared snapshot.
	files := unpack(t, fixture(t, "legacy-v2"))
	const taskPath = "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a/README.md"
	const valid = "# Title\n\n## Description\n## Background\nText\n\n## Scope\nBounded\n\n## Acceptance Criteria\nRetained\n"
	files[taskPath] = []byte(valid)
	mustPrepare(t, pack(t, files))
	for _, tc := range []struct{ suffix, message string }{
		{"\n## Unknown\nmeaningful\n", "unknown README section"},
		{"\n## Description\nsecond\n", "duplicate README section"},
		{"\n## Scope\nsecond\n", "duplicate README section"},
	} {
		files[taskPath] = []byte(valid + tc.suffix)
		p, err := New("dev").Prepare(context.Background(), bytes.NewReader(pack(t, files)), contract.ImportOptions{ProjectID: "synthetic-board"})
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || !strings.Contains(ce.Message, tc.message) || !reflect.DeepEqual(p, contract.PreparedImport{}) {
			t.Fatalf("wanted specific atomic refusal %q, got %+v, %v", tc.message, p, err)
		}
	}
}
