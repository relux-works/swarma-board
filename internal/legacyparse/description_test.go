package legacyparse

import (
	"strings"
	"testing"
)

func TestDescriptionPreservesEmbeddedMarkdown(t *testing.T) {
	// Embedded headings stay in Description, with exact decoded text and no progress authority.
	for _, eol := range []string{"\n", "\r\n"} {
		for _, body := range []string{
			"Plain description.",
			"Quoted requirement:\n\n## Background\n> Keep «quoted words» verbatim.\n\n### Detail\nLine two.",
			"## Status\ndone\n## Assigned To\nother-label\n## Notes\ntext",
			"## Background\none\n## Background\ntwo\n## \nempty heading",
			strings.Repeat("## Background\ntext\n", 100),
			"\\## Scope\nliteral\n```text\n## Acceptance Criteria\nliteral too\n```\n",
		} {
			body = strings.ReplaceAll(body, "\n", eol)
			rd := "# Title" + eol + eol + "## Description" + eol + body + eol + eol + "## Scope" + eol + "Bounded" + eol + eol + "## Acceptance Criteria" + eol + "Preserved" + eol
			p, err := Parse([]byte(rd), []byte("## Status\nanalysis\n\n## Assigned To\noriginal-label\n"))
			if err != nil {
				t.Fatalf("embedded Markdown refused: %v", err)
			}
			want := strings.ReplaceAll(body, "\\## Scope", "## Scope")
			if p.Element.Description != want || p.Element.Title != "Title" || p.Element.Scope != "Bounded" || p.Element.AcceptanceCriteria != "Preserved" || p.Element.Status != "analysis" || p.Element.AssignedTo != "original-label" || p.Element.Notes != "" || len(p.Exclusions) != 0 {
				t.Fatalf("body or neighboring fields changed: %+v", p)
			}
		}
	}
}

func TestDescriptionKeepsOuterGrammarClosed(t *testing.T) {
	// Unknown outer sections, duplicate product fields and malformed framing still refuse specifically.
	for _, tc := range []struct{ readme, want string }{
		{"# T\n\n## Unknown\nvalue\n\n## Description\ntext\n", "unknown README section"},
		{"# T\n\n## Description\ntext\n\n## Scope\ntext\n\n## Unknown\nvalue\n", "unknown README section"},
		{"# T\n\n## Description\ntext\n\n## Acceptance Criteria\ntext\n\n## Unknown\nvalue\n", "unknown README section"},
		{"# T\n\n## Description\ntext\n\n## Description\nsecond\n", "duplicate README section"},
		{"# T\n\n## Description\ntext\n# Injected title\n", "duplicate title heading"},
		{"# T\n\n## Description\n```\ntext\n", "unclosed ` fence"},
	} {
		if _, err := Parse([]byte(tc.readme), nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("wanted %q, got %v", tc.want, err)
		}
	}
}

func TestEscapedFieldNamesCannotChangeNeighbors(t *testing.T) {
	// Decoding payload escapes happens after framing: title, Description and Notes cannot inject outer fields.
	rd := "# Title\\n## Status\\ndone\n\n## Description\n\\## Scope\nliteral scope\n\\## Description\nliteral description\n\n## Scope\nactual scope\n"
	progress := "## Notes\n\\## Status\ndone\n\\## Assigned To\nother-label\n\n## Status\nanalysis\n\n## Assigned To\noriginal-label\n"
	p, err := Parse([]byte(rd), []byte(progress))
	if err != nil {
		t.Fatal(err)
	}
	if p.Element.Title != "Title\n## Status\ndone" || p.Element.Description != "## Scope\nliteral scope\n## Description\nliteral description" || p.Element.Scope != "actual scope" || p.Element.Notes != "## Status\ndone\n## Assigned To\nother-label" || p.Element.Status != "analysis" || p.Element.AssignedTo != "original-label" {
		t.Fatalf("decoded payload changed field boundaries: %+v", p.Element)
	}
}
