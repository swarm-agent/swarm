package copyblock

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Block is one assistant-provided payload wrapped in a <copy> tag.
type Block struct {
	Label   string
	Content string
}

// Segment preserves ordinary assistant text around copy blocks.
type Segment struct {
	Text string
	Copy *Block
}

var (
	openTagPattern = regexp.MustCompile(`(?is)<copy(?:\s+[^>]*)?>`)
	attrPattern    = regexp.MustCompile(`(?is)\b(?:label|title|name)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
)

// Split parses copy blocks while leaving tags inside Markdown fences untouched.
func Split(text string) []Segment {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	if !mayContainOpenTag(text) {
		return []Segment{{Text: text}}
	}

	protectedRanges := markdownProtectedRanges(text)
	segments := make([]Segment, 0, 4)
	cursor := 0
	for cursor < len(text) {
		loc := nextOpenTag(text, cursor, protectedRanges)
		if loc == nil {
			segments = appendTextSegment(segments, text[cursor:])
			break
		}
		if loc[0] > cursor {
			segments = appendTextSegment(segments, text[cursor:loc[0]])
		}

		openTag := text[loc[0]:loc[1]]
		afterOpen := text[loc[1]:]
		closeIndex := strings.Index(strings.ToLower(afterOpen), "</copy>")
		if closeIndex < 0 {
			segments = appendTextSegment(segments, text[loc[0]:])
			break
		}

		segments = append(segments, Segment{Copy: &Block{
			Label:   tagLabel(openTag),
			Content: Normalize(afterOpen[:closeIndex]),
		}})
		cursor = loc[1] + closeIndex + len("</copy>")
	}
	return segments
}

// Count returns the number of valid copy blocks in text.
func Count(text string) int {
	count := 0
	for _, segment := range Split(text) {
		if segment.Copy != nil {
			count++
		}
	}
	return count
}

// Normalize keeps payload bytes stable apart from line-ending normalization and
// the newlines directly surrounding the tag payload.
func Normalize(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.Trim(content, "\n")
}

// CommandPreview returns a compact single-line palette hint.
func CommandPreview(content string) string {
	content = strings.ReplaceAll(Normalize(content), "\t", " ")
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "(empty copy block)"
	}
	return strings.Join(fields, " ")
}

// ParseIndexArg validates the one-based block number accepted by /copy N.
func ParseIndexArg(args []string) (int, bool) {
	if len(args) != 1 {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || index <= 0 {
		return 0, false
	}
	return index, true
}

// PreviewStatus returns the established success status for /copy N.
func PreviewStatus(index int, text string) string {
	first := strings.TrimSpace(strings.Split(Normalize(text), "\n")[0])
	if first == "" {
		return fmt.Sprintf("copied /copy %d", index)
	}
	if utf8.RuneCountInString(first) > 48 {
		first = truncateRunes(first, 47) + "…"
	}
	return fmt.Sprintf("copied /copy %d: %s", index, first)
}

func mayContainOpenTag(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<copy>") || strings.Contains(lower, "<copy ") || strings.Contains(lower, "<copy\t") || strings.Contains(lower, "<copy\n")
}

type byteRange struct {
	Start int
	End   int
}

func nextOpenTag(text string, start int, protectedRanges []byteRange) []int {
	for start < len(text) {
		loc := openTagPattern.FindStringIndex(text[start:])
		if loc == nil {
			return nil
		}
		loc[0] += start
		loc[1] += start
		if protected, end := indexProtected(loc[0], protectedRanges); protected {
			start = max(end, loc[1])
			continue
		}
		return loc
	}
	return nil
}

func indexProtected(index int, protectedRanges []byteRange) (bool, int) {
	for _, protected := range protectedRanges {
		if index < protected.Start {
			return false, 0
		}
		if index >= protected.Start && index < protected.End {
			return true, protected.End
		}
	}
	return false, 0
}

func markdownProtectedRanges(text string) []byteRange {
	ranges := make([]byteRange, 0, 2)
	var fence markdownFence
	fenceStart := 0
	lineStart := 0
	for lineStart < len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		nextLineStart := len(text)
		if lineEnd >= 0 {
			lineEnd += lineStart
			nextLineStart = lineEnd + 1
		} else {
			lineEnd = len(text)
		}
		line, ok := parseFenceLine(strings.TrimSpace(strings.TrimRight(text[lineStart:lineEnd], "\t \r")))
		if ok {
			if !fence.active() && line.Count >= 3 {
				fence = markdownFence{Active: true, Marker: line.Marker, Count: line.Count}
				fenceStart = lineStart
			} else if fence.canClose(line) {
				ranges = append(ranges, byteRange{Start: fenceStart, End: nextLineStart})
				fence = markdownFence{}
			}
		}
		lineStart = nextLineStart
	}
	if fence.active() {
		ranges = append(ranges, byteRange{Start: fenceStart, End: len(text)})
	}
	return ranges
}

type markdownFenceLine struct {
	Marker byte
	Count  int
	Info   string
}

type markdownFence struct {
	Active bool
	Marker byte
	Count  int
}

func parseFenceLine(line string) (markdownFenceLine, bool) {
	if line == "" || line[0] != '`' && line[0] != '~' {
		return markdownFenceLine{}, false
	}
	marker := line[0]
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	return markdownFenceLine{Marker: marker, Count: count, Info: strings.TrimSpace(line[count:])}, true
}

func (f markdownFence) active() bool {
	return f.Active && f.Count >= 3 && (f.Marker == '`' || f.Marker == '~')
}

func (f markdownFence) canClose(line markdownFenceLine) bool {
	return f.active() && line.Marker == f.Marker && strings.TrimSpace(line.Info) == "" && line.Count >= f.Count
}

func appendTextSegment(segments []Segment, text string) []Segment {
	if text != "" {
		segments = append(segments, Segment{Text: text})
	}
	return segments
}

func tagLabel(openTag string) string {
	match := attrPattern.FindStringSubmatch(openTag)
	for _, candidate := range match[2:] {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
