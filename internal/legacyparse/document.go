// Parsing and escape decoding adapted from pkg/board/markdown/document.go at
// skill-project-management 09bac3d89822c9b83c04dff6483d7b2074097a82.
// Apache-2.0; see repository LICENSE and NOTICE.
package legacyparse

import (
	"fmt"
	"iter"
	"strings"
)

type sourceLine struct {
	start      int
	contentEnd int
	end        int
}

type markdownSection struct {
	name         string
	normalized   string
	headingStart int
	headingEnd   int
	bodyStart    int
	bodyEnd      int
}

type markdownTitle struct {
	valueStart int
	valueEnd   int
}

type markdownDocument struct {
	source   string
	eol      string
	title    *markdownTitle
	sections []markdownSection
}

type fenceState struct {
	marker byte
	length int
	line   int
}

// parseMarkdownDocument identifies top-level ATX headings without allowing
// headings inside fenced code blocks to change parser state. It deliberately
// works on source offsets instead of bufio.Scanner so large Markdown lines are
// accepted without truncation; field bodies retain exact framing and escapes.
func parseMarkdownDocument(content string, allowTitle bool) (*markdownDocument, error) {
	doc := &markdownDocument{source: content, eol: detectLineEnding(content)}
	lines := splitSourceLines(content)
	var fence *fenceState

	for lineIndex, line := range lines {
		text := content[line.start:line.contentEnd]

		if fence != nil {
			if isFenceClose(text, fence.marker, fence.length) {
				fence = nil
			}
			continue
		}

		if marker, length, ok := fenceMarker(text); ok {
			fence = &fenceState{marker: marker, length: length, line: lineIndex + 1}
			continue
		}

		level, name, valueStart, valueEnd, ok := atxHeading(text)
		if !ok {
			continue
		}

		switch level {
		case 1:
			if !allowTitle {
				continue
			}
			if doc.title != nil {
				return nil, fmt.Errorf("duplicate title heading on line %d", lineIndex+1)
			}
			doc.title = &markdownTitle{
				valueStart: line.start + valueStart,
				valueEnd:   line.start + valueEnd,
			}
		case 2:
			normalized := normalizeHeading(name)
			if normalized == "" {
				return nil, fmt.Errorf("empty section heading on line %d", lineIndex+1)
			}
			if len(doc.sections) >= 64 {
				return nil, fmt.Errorf("too many Markdown sections")
			}
			doc.sections = append(doc.sections, markdownSection{
				name:         name,
				normalized:   normalized,
				headingStart: line.start,
				headingEnd:   line.end,
				bodyStart:    line.end,
			})
		}
	}

	if fence != nil {
		return nil, fmt.Errorf("unclosed %c fence opened on line %d", fence.marker, fence.line)
	}

	for i := range doc.sections {
		if i+1 < len(doc.sections) {
			doc.sections[i].bodyEnd = doc.sections[i+1].headingStart
		} else {
			doc.sections[i].bodyEnd = len(content)
		}
	}

	return doc, nil
}

// Iterate offsets without allocating a slice proportional to line count.
func splitSourceLines(content string) iter.Seq2[int, sourceLine] {
	return func(yield func(int, sourceLine) bool) {
		index := 0
		for start := 0; start < len(content); index++ {
			newline := strings.IndexByte(content[start:], '\n')
			end := len(content)
			if newline >= 0 {
				end = start + newline + 1
			}
			contentEnd := end
			if newline >= 0 {
				contentEnd--
			}
			if contentEnd > start && content[contentEnd-1] == '\r' {
				contentEnd--
			}
			if !yield(index, sourceLine{start: start, contentEnd: contentEnd, end: end}) {
				return
			}
			start = end
		}
	}
}

func detectLineEnding(content string) string {
	if index := strings.IndexByte(content, '\n'); index >= 0 && index > 0 && content[index-1] == '\r' {
		return "\r\n"
	}
	return "\n"
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

func isFenceClose(line string, marker byte, minimumLength int) bool {
	trimmed, ok := structuralPrefix(line)
	if !ok || len(trimmed) < minimumLength || trimmed[0] != marker {
		return false
	}
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	return length >= minimumLength && strings.TrimSpace(trimmed[length:]) == ""
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

func normalizeHeading(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (doc *markdownDocument) sectionBody(section *markdownSection) string {
	body := doc.source[section.bodyStart:section.bodyEnd]
	followedBySection := section.bodyEnd < len(doc.source)
	framing := doc.eol
	if followedBySection {
		framing += doc.eol
	}
	if framing != "" && strings.HasSuffix(body, framing) {
		return body[:len(body)-len(framing)]
	}

	// Non-canonical hand-authored documents may use mixed line endings or omit
	// the blank separator. Retain the permissive fallback for those inputs, but
	// prefer exact framing above so a terminal lone CR remains payload rather
	// than combining with an appended LF into an apparent CRLF.
	trimCount := 1
	if followedBySection && body != "" {
		trimCount = 2
	}
	for range trimCount {
		var trimmed bool
		body, trimmed = trimOneLineEnding(body)
		if !trimmed {
			break
		}
	}
	return body
}

func trimOneLineEnding(value string) (string, bool) {
	switch {
	case strings.HasSuffix(value, "\r\n"):
		return value[:len(value)-2], true
	case strings.HasSuffix(value, "\n") || strings.HasSuffix(value, "\r"):
		return value[:len(value)-1], true
	default:
		return value, false
	}
}

func decodeMarkdownBlock(value string) string {
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range splitSourceLines(value) {
		text := value[line.start:line.contentEnd]
		b.WriteString(unescapeStructuralLine(text))
		b.WriteString(value[line.contentEnd:line.end])
	}
	return b.String()
}

func unescapeStructuralLine(line string) string {
	prefixLength := 0
	for prefixLength < len(line) && line[prefixLength] == ' ' && prefixLength < 4 {
		prefixLength++
	}
	if prefixLength > 3 {
		return line
	}
	rest := line[prefixLength:]
	slashCount := leadingBackslashes(rest)
	if slashCount == 0 || !isStructuralMarker(rest[slashCount:]) {
		return line
	}
	return line[:prefixLength] + rest[1:]
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

func decodeInline(value string) string {
	return decodeInlineEscapes(value, false)
}

func decodeInlineBoundaryWhitespace(value string) string {
	return decodeInlineEscapes(value, true)
}

func decodeInlineEscapes(value string, boundaryWhitespace bool) string {
	var b strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			b.WriteByte(value[index])
			continue
		}

		index++
		switch value[index] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 's':
			if boundaryWhitespace {
				b.WriteByte(' ')
			} else {
				b.WriteString("\\s")
			}
		case 't':
			if boundaryWhitespace {
				b.WriteByte('\t')
			} else {
				b.WriteString("\\t")
			}
		case '\\', '[', ']', '(', ')', '#':
			b.WriteByte(value[index])
		default:
			b.WriteByte('\\')
			b.WriteByte(value[index])
		}
	}
	return b.String()
}
