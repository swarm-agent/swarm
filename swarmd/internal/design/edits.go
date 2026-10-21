package design

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Edit bounds: a repair or edit is a few exact replacements, never a rewrite.
const (
	maxEdits        = 20
	maxEditOldBytes = 8 << 10
	maxEditNewBytes = 128 << 10 // total across all edits
)

// Edit replaces one exact, unique piece of the document.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

var (
	ErrEditsMalformed = errors.New("the reply is not {\"edits\":[{\"old\",\"new\"}]} JSON")
	ErrEditsEmpty     = errors.New("the reply contains no edits")
	ErrEditsTooLarge  = errors.New("the edits exceed the size limits")
	ErrEditNotFound   = errors.New("an edit's old text does not appear in the document")
	ErrEditAmbiguous  = errors.New("an edit's old text appears more than once in the document")
	ErrEditOverlap    = errors.New("two edits change overlapping text")
)

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

// ParseEdits reads {"edits":[...]} from a model reply, fenced or bare.
func ParseEdits(raw string) ([]Edit, error) {
	text := strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(text); len(m) > 1 {
		text = m[1]
	} else if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	var body struct {
		Edits []Edit `json:"edits"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		return nil, ErrEditsMalformed
	}
	if len(body.Edits) == 0 {
		return nil, ErrEditsEmpty
	}
	if len(body.Edits) > maxEdits {
		return nil, fmt.Errorf("%w: more than %d edits", ErrEditsTooLarge, maxEdits)
	}
	total := 0
	for _, e := range body.Edits {
		if e.Old == "" {
			return nil, fmt.Errorf("%w: an edit has empty old text", ErrEditsMalformed)
		}
		if len(e.Old) > maxEditOldBytes {
			return nil, fmt.Errorf("%w: an edit's old text is over %d bytes", ErrEditsTooLarge, maxEditOldBytes)
		}
		total += len(e.New)
	}
	if total > maxEditNewBytes {
		return nil, fmt.Errorf("%w: replacement text is over %d bytes", ErrEditsTooLarge, maxEditNewBytes)
	}
	return body.Edits, nil
}

// ApplyEdits applies every edit to src or none. Each old text must appear
// exactly once in src, and no two edits may overlap.
func ApplyEdits(src string, edits []Edit) (string, error) {
	type span struct {
		start, end int
		repl       string
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		switch strings.Count(src, e.Old) {
		case 0:
			return "", ErrEditNotFound
		case 1:
		default:
			return "", ErrEditAmbiguous
		}
		at := strings.Index(src, e.Old)
		spans = append(spans, span{at, at + len(e.Old), e.New})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", ErrEditOverlap
		}
	}
	var b strings.Builder
	prev := 0
	for _, s := range spans {
		b.WriteString(src[prev:s.start])
		b.WriteString(s.repl)
		prev = s.end
	}
	b.WriteString(src[prev:])
	out := b.String()
	if len(out) > MaxHTMLBytes {
		return "", fmt.Errorf("%w: the edited document is over 4 MiB", ErrEditsTooLarge)
	}
	if !utf8.ValidString(out) {
		return "", fmt.Errorf("%w: the edited document is not valid UTF-8", ErrEditsMalformed)
	}
	return out, nil
}
