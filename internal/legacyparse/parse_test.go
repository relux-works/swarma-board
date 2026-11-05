package legacyparse

import (
	"strings"
	"testing"
)

const readme = "# Title\\#\\nline\n\n## Description\nline one\n\\## literal heading\n\n## Scope\nscope\n\n## Acceptance Criteria\ncriteria\n"

func TestInspectedEscapesFramingAndFences(t *testing.T) {
	p, e := Parse([]byte(readme), []byte("## Notes\n\n keep spaces \n```text\n## literal fenced heading\n```\n\n\n## Checklist\n- [X] escaped\\nnewline\n\n## Assigned To\nlabel\\nline\n"))
	if e != nil {
		t.Fatal(e)
	}
	if p.Element.Title != "Title#\nline" || p.Element.Description != "line one\n## literal heading" || p.Element.Notes != "\n keep spaces \n```text\n## literal fenced heading\n```\n" || p.Element.Checklist[0].Text != "escaped\nnewline" || p.Element.AssignedTo != `label\nline` {
		t.Fatalf("text changed %+v", p.Element)
	}
	crlf := strings.ReplaceAll(readme, "\n", "\r\n")
	p, e = Parse([]byte(crlf), []byte("## Notes\r\n Keep CRLF\r\nSecond\r\n"))
	if e != nil || p.Element.Description != "line one\r\n## literal heading" || p.Element.Notes != " Keep CRLF\r\nSecond" {
		t.Fatalf("CRLF changed: %+v %v", p, e)
	}
}
func TestStrictProductMarkdownRefusesLoss(t *testing.T) {
	for name, progress := range map[string]string{
		"unknown":              "## Unknown Meaningful Field\nmust survive\n",
		"duplicate alias":      "## Task Class\ncode\n\n## task_class\ndocs\n",
		"checklist prose":      "## Checklist\n- [ ] item\nUnrepresentable extra prose\n",
		"edge prose":           "## Blocked By\n- TASK-001\nExtra prose\n",
		"malformed resource":   "## Outcome Resources\n- not a structured link\n",
		"estimate overflow":    "## Estimate\nestimated(fibonacci(999999999999999999999999999999))\n",
		"estimate partial":     "## Estimate\npartiallyEstimated\n",
		"timestamp":            "## Created\nnot-a-time\n",
		"unclosed fence":       "## Notes\n```\ntext\n",
		"unstructured removed": "## provider\nmultiple\nlines\n",
		"invalid checkpoint":   "## integrationCheckpointed\nmaybe\n",
		"reserved authority":   "## Notes\n<!-- commit_ack=true -->\n",
		"preamble":             "unrepresented preamble\n## Notes\ntext\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := Parse([]byte(readme), []byte(progress)); e == nil {
				t.Fatal("lossy/unsafe shape accepted")
			}
		})
	}
	for _, rd := range []string{"# title\n\nfree preamble\n\n## Scope\ntext\n", "# one\n# two\n", readme + "\n## Custom\ntext\n", "text before title\n" + readme} {
		if _, e := Parse([]byte(rd), nil); e == nil {
			t.Fatal("unrepresented README accepted")
		}
	}
	if _, e := Parse([]byte{'#', ' ', 0xff}, nil); e == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
func TestLargeLineAndManyBlankLines(t *testing.T) {
	line := strings.Repeat("x", 100000)
	p, e := Parse([]byte("# T\n\n## Description\n"+line+"\n"), nil)
	if e != nil || p.Element.Description != line {
		t.Fatal("long line truncated", e)
	}
	p, e = Parse([]byte("# T\n\n## Description\n"+strings.Repeat("\n", 200000)), nil)
	if e != nil || len(p.Element.Description) != 199999 {
		t.Fatal("blank line framing changed", e, len(p.Element.Description))
	}
}
func FuzzParse(f *testing.F) {
	f.Add(readme, "## Notes\ntext\n")
	f.Add("# Title\n", "## integrationCheckpointed\ntrue\n")
	f.Fuzz(func(t *testing.T, r, p string) {
		if len(r) > 1<<20 || len(p) > 1<<20 {
			t.Skip()
		}
		_, _ = Parse([]byte(r), []byte(p))
	})
}

func TestExactRemovedMetadata(t *testing.T) {
	progress := "## change_request\n{\"lifecycle\":\"synthetic\",\"revision\":1,\"state\":\"synthetic-state\",\"evidence\":\"inert-synthetic\"}\n\n"
	for _, key := range []string{"integrationCheckpointed", "run_id", "session_id", "provider", "model", "commit_ack", "txn_id"} {
		value := "synthetic"
		if key == "integrationCheckpointed" {
			value = "false"
		}
		progress += "## " + key + "\n" + value + "\n\n"
	}
	p, e := Parse([]byte(readme), []byte(progress))
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Exclusions) != 11 {
		t.Fatalf("missing exclusions: %v", p.Exclusions)
	}
	if p.Element.Notes != "" || p.Element.AssignedTo != "" {
		t.Fatal("removed metadata gained product authority")
	}
	for _, body := range []string{`{"state":1,"state":2}`, `{"unknown":"meaningful"}`, `{"evidence":{"secret":"synthetic"}}`, `{"state":"x"} {}`, `{"revision":`} {
		if _, e := Parse([]byte(readme), []byte("## change_request\n"+body+"\n")); e == nil {
			t.Fatal("unsafe removed shape accepted", body)
		}
	}
}

func TestMissingHistoricalTitleAndEstimateRemainEmpty(t *testing.T) {
	p, e := Parse([]byte("## Description\nHistorical text\n"), []byte("## Status\nbacklog\n"))
	if e != nil || p.Element.Title != "" || p.Element.Description != "Historical text" || p.Element.Estimate != nil {
		t.Fatalf("historical missing values changed %+v %v", p, e)
	}
}
