//go:build linux

package fs

// Canonical write-only renderer extracted from skill-project-management
// 09bac3d89822c9b83c04dff6483d7b2074097a82 (Apache-2.0, see NOTICE).
// Parser/rewrite paths are deliberately absent. Types and display-only adapters
// bridge the unchanged common record; no rendered default becomes stored state.
import (
	"fmt"
	"strings"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type readmeView struct{ Title, Description, Scope, AC string }
type progressView struct {
	Status, Review, TaskClass, AssignedTo, Notes string
	Estimate                                     *dp.Estimate
	CreatedAt, LastUpdate                        time.Time
	BlockedBy, Blocks                            []string
	Checklist                                    []dp.ChecklistItem
	PreconditionResources, OutcomeResources      []dp.ResourceRef
}
type sourceLine struct{ start, contentEnd, end int }

func displayReview(v string) string {
	if strings.TrimSpace(v) == "" {
		return "required"
	}
	return v
}
func displayTaskClass(v string) string {
	if strings.TrimSpace(v) == "" {
		return "code"
	}
	return v
}
func displayEstimate(v *dp.Estimate) string {
	if v != nil && v.Kind == "estimated" && v.Scale != nil {
		return fmt.Sprintf("estimated(%s(%d))", v.Scale.Kind, v.Scale.Number)
	}
	return "notEstimated"
}

func writeCanonicalReadme(rd *readmeView) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(encodeHeadingInline(rd.Title))
	b.WriteString("\n\n")
	b.WriteString("## Description\n")
	b.WriteString(encodeMarkdownBlock(rd.Description))
	b.WriteString("\n\n")
	b.WriteString("## Scope\n")
	b.WriteString(encodeMarkdownBlock(rd.Scope))
	b.WriteString("\n\n")
	b.WriteString("## Acceptance Criteria\n")
	b.WriteString(encodeMarkdownBlock(rd.AC))
	b.WriteByte('\n')
	return b.String()
}

func writeCanonicalProgress(pd *progressView) string {
	type progressSection struct {
		heading string
		body    string
	}
	sections := []progressSection{
		{"Status", normalizedStatus(pd.Status)},
		{"Review", string(displayReview(pd.Review))},
		{"Task Class", string(displayTaskClass(pd.TaskClass))},
	}
	if pd.Estimate != nil {
		sections = append(sections, progressSection{"Estimate", displayEstimate(pd.Estimate)})
	}
	sections = append(sections,
		progressSection{"Assigned To", assignedToBody(pd.AssignedTo)},
		progressSection{"Created", timeBody(pd.CreatedAt)},
		progressSection{"Last Update", timeBody(pd.LastUpdate)},
		progressSection{"Blocked By", stringListBody(pd.BlockedBy)},
		progressSection{"Blocks", stringListBody(pd.Blocks)},
		progressSection{"Checklist", checklistBody(pd.Checklist)},
		progressSection{"Notes", encodeMarkdownBlock(pd.Notes)},
		progressSection{"Precondition Resources", resourceListBody(pd.PreconditionResources)},
		progressSection{"Outcome Resources", resourceListBody(pd.OutcomeResources)},
	)

	var b strings.Builder
	for index, section := range sections {
		b.WriteString("## ")
		b.WriteString(section.heading)
		b.WriteByte('\n')
		b.WriteString(section.body)
		b.WriteByte('\n')
		if index+1 < len(sections) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func normalizedStatus(status string) string {
	if status == "" {
		return string("backlog")
	}
	return string(status)
}

func assignedToBody(assignedTo string) string {
	if assignedTo == "" {
		return "(none)"
	}
	return encodeMarkdownBlock(assignedTo)
}

func timeBody(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func stringListBody(values []string) string {
	if len(values) == 0 {
		return "- (none)"
	}
	var lines []string
	for _, value := range values {
		lines = append(lines, "- "+encodeInline(value, ""))
	}
	return strings.Join(lines, "\n")
}

func checklistBody(items []dp.ChecklistItem) string {
	if len(items) == 0 {
		return "(empty)"
	}
	var lines []string
	for _, item := range items {
		mark := " "
		if item.Checked {
			mark = "x"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", mark, encodeInline(item.Text, "")))
	}
	return strings.Join(lines, "\n")
}

func resourceListBody(refs []dp.ResourceRef) string {
	if len(refs) == 0 {
		return "(none)"
	}
	var lines []string
	for _, ref := range refs {
		line := fmt.Sprintf("- [%s](%s)", encodeInline(ref.Name, "]"), encodeInline(ref.URI, "()"))
		if ref.Description != "" {
			line += " — " + encodeInlineBoundaryWhitespace(ref.Description, "")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func splitSourceLines(content string) []sourceLine {
	if content == "" {
		return nil
	}

	lines := make([]sourceLine, 0, strings.Count(content, "\n")+1)
	for start := 0; start < len(content); {
		relativeEnd := strings.IndexByte(content[start:], '\n')
		if relativeEnd < 0 {
			contentEnd := len(content)
			if contentEnd > start && content[contentEnd-1] == '\r' {
				contentEnd--
			}
			lines = append(lines, sourceLine{start: start, contentEnd: contentEnd, end: len(content)})
			break
		}

		newline := start + relativeEnd
		contentEnd := newline
		if contentEnd > start && content[contentEnd-1] == '\r' {
			contentEnd--
		}
		lines = append(lines, sourceLine{start: start, contentEnd: contentEnd, end: newline + 1})
		start = newline + 1
	}
	return lines
}

func atxHeading(line string) (level int, name string, valueStart, valueEnd int, ok bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' && indent < 4 {
		indent++
	}
	if indent > 3 || indent >= len(line) || line[indent] != '#' {
		return 0, "", 0, 0, false
	}

	hashEnd := indent
	for hashEnd < len(line) && line[hashEnd] == '#' {
		hashEnd++
	}
	level = hashEnd - indent
	if level != 1 && level != 2 {
		return 0, "", 0, 0, false
	}
	if hashEnd < len(line) && line[hashEnd] != ' ' && line[hashEnd] != '\t' {
		return 0, "", 0, 0, false
	}

	valueStart = hashEnd
	for valueStart < len(line) && (line[valueStart] == ' ' || line[valueStart] == '\t') {
		valueStart++
	}
	valueEnd = len(line)
	for valueEnd > valueStart && (line[valueEnd-1] == ' ' || line[valueEnd-1] == '\t') {
		valueEnd--
	}
	if valueEnd > valueStart && line[valueEnd-1] == '#' {
		closingStart := valueEnd
		for closingStart > valueStart && line[closingStart-1] == '#' {
			closingStart--
		}
		if closingStart > valueStart && (line[closingStart-1] == ' ' || line[closingStart-1] == '\t') {
			valueEnd = closingStart - 1
			for valueEnd > valueStart && (line[valueEnd-1] == ' ' || line[valueEnd-1] == '\t') {
				valueEnd--
			}
		}
	}

	name = line[valueStart:valueEnd]
	return level, name, valueStart, valueEnd, true
}

func fenceMarker(line string) (marker byte, length int, ok bool) {
	trimmed, ok := structuralPrefix(line)
	if !ok || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	marker = trimmed[0]
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return 0, 0, false
	}
	return marker, length, true
}

func structuralPrefix(line string) (string, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' && indent < 4 {
		indent++
	}
	if indent > 3 {
		return "", false
	}
	return line[indent:], true
}

func encodeMarkdownBlock(value string) string {
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range splitSourceLines(value) {
		text := value[line.start:line.contentEnd]
		b.WriteString(escapeStructuralLine(text))
		b.WriteString(value[line.contentEnd:line.end])
	}
	return b.String()
}

func escapeStructuralLine(line string) string {
	prefixLength := 0
	for prefixLength < len(line) && line[prefixLength] == ' ' && prefixLength < 4 {
		prefixLength++
	}
	if prefixLength > 3 {
		return line
	}
	rest := line[prefixLength:]
	slashCount := leadingBackslashes(rest)
	if isStructuralMarker(rest[slashCount:]) {
		return line[:prefixLength] + "\\" + rest
	}
	return line
}

func leadingBackslashes(value string) int {
	count := 0
	for count < len(value) && value[count] == '\\' {
		count++
	}
	return count
}

func isStructuralMarker(value string) bool {
	if marker, length, ok := fenceMarker(value); ok && length >= 3 && (marker == '`' || marker == '~') {
		return true
	}
	level, _, _, _, ok := atxHeading(value)
	return ok && (level == 1 || level == 2)
}

func encodeInline(value, delimiters string) string {
	var b strings.Builder
	for index := 0; index < len(value); index++ {
		writeEncodedInlineByte(&b, value[index], delimiters)
	}
	return b.String()
}

func encodeInlineBoundaryWhitespace(value, delimiters string) string {
	start := 0
	for start < len(value) && isHorizontalWhitespace(value[start]) {
		start++
	}
	end := len(value)
	for end > start && isHorizontalWhitespace(value[end-1]) {
		end--
	}

	var b strings.Builder
	for index := 0; index < len(value); index++ {
		if index < start || index >= end {
			switch value[index] {
			case ' ':
				b.WriteString("\\s")
				continue
			case '\t':
				b.WriteString("\\t")
				continue
			}
		}
		writeEncodedInlineByte(&b, value[index], delimiters)
	}
	return b.String()
}

func encodeHeadingInline(value string) string {
	return encodeInlineBoundaryWhitespace(value, "#")
}

func isHorizontalWhitespace(value byte) bool {
	return value == ' ' || value == '\t'
}

func writeEncodedInlineByte(b *strings.Builder, value byte, delimiters string) {
	switch value {
	case '\\':
		b.WriteString("\\\\")
	case '\n':
		b.WriteString("\\n")
	case '\r':
		b.WriteString("\\r")
	default:
		if strings.IndexByte(delimiters, value) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(value)
	}
}
