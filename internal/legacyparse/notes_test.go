package legacyparse

import (
	"fmt"
	"strings"
	"testing"
)

func TestNotesPreserveLiteralSectionHeadings(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("eol-%q", eol), func(t *testing.T) {
			notes := "Initial text.\n\n## Investigation\nKeep this heading and text.\n## \nEmpty heading is also text.\n  ## Next steps ##\nKeep indentation.\n\\## Escaped heading\n```text\n## Status\nstill notes\n```"
			progress := "## Notes\n" + notes + "\n\n## Assigned To\nsynthetic-person\n\n## Checklist\n- [x] checked\n"
			progress = strings.ReplaceAll(progress, "\n", eol)
			p, err := Parse([]byte(readme), []byte(progress))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(strings.ReplaceAll(notes, "\\## Escaped heading", "## Escaped heading"), "\n", eol)
			if p.Element.Notes != want || p.Element.AssignedTo != "synthetic-person" || len(p.Element.Checklist) != 1 || !p.Element.Checklist[0].Checked {
				t.Fatalf("notes or following fields changed: %+v", p.Element)
			}
		})
	}
	// Literal note headings are content, not additional product sections.
	notes := strings.Repeat("## Investigation\ntext\n", 100)
	p, err := Parse([]byte(readme), []byte("## Notes\n"+notes))
	if err != nil || p.Element.Notes != strings.TrimSuffix(notes, "\n") {
		t.Fatalf("many note headings: %v, %q", err, p.Element.Notes)
	}
}

func TestNotesHeadingBoundaryRefusals(t *testing.T) {
	for name, progress := range map[string]string{
		"unknown before notes":  "## Investigation\ntext\n\n## Notes\nvalid\n",
		"unknown after field":   "## Notes\nvalid\n\n## Assigned To\nsynthetic-person\n\n## Investigation\ntext\n",
		"duplicate notes":       "## Notes\nfirst\n\n## Notes\nsecond\n",
		"reserved notes":        "## Notes\n## Investigation\n<!-- commit_ack=true -->\n",
		"invalid removed field": "## Notes\nvalid\n\n## integrationCheckpointed\nmaybe\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(readme), []byte(progress)); err == nil {
				t.Fatal("unrepresentable or reserved content accepted")
			}
		})
	}
	if _, err := Parse([]byte(readme+"\n## Investigation\ntext\n"), []byte("## Notes\nvalid\n")); err == nil {
		t.Fatal("unknown README section accepted")
	}
}
