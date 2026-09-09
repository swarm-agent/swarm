package htmlcapture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// The absolute duration bound is derived from the frame budget at minimum FPS.
// Admission at the actual frame rate uses AnimationFrameBudget.
const (
	MinAuthoredAnimationDurationMS = 100
	MaxAuthoredAnimationDurationMS = MaxAnimationDurationMS
)

// AnimationTiming reads the authored declaration without executing or rewriting
// source. Absence is distinct from an invalid declaration: only absence permits
// a caller's legacy timing policy.
func AnimationTiming(body []byte) (duration, fps int, present bool, err error) {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				err = z.Err()
			}
			return
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		id, typ := "", ""
		for _, a := range token.Attr {
			if a.Key == "id" {
				id = a.Val
			}
			if a.Key == "type" {
				typ = a.Val
			}
		}
		if id != "swarm-animation-manifest" {
			continue
		}
		if present || token.Data != "script" || !strings.EqualFold(typ, "application/json") || kind == html.SelfClosingTagToken {
			return 0, 0, true, errors.New("invalid or duplicate animation manifest")
		}
		present = true
		if z.Next() != html.TextToken {
			return 0, 0, true, errors.New("empty animation manifest")
		}
		var value struct {
			Version  string `json:"version"`
			Duration int    `json:"duration_ms"`
			FPS      int    `json:"fps"`
		}
		decoder := json.NewDecoder(bytes.NewReader(z.Text()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Version != AnimationVersion || value.Duration < MinAuthoredAnimationDurationMS || value.Duration > MaxAuthoredAnimationDurationMS || value.FPS < 1 || value.FPS > MaxAnimationFPS {
			return 0, 0, true, errors.New("invalid animation timing declaration")
		}
		if _, err := AnimationFrameBudget(int64(value.Duration), value.FPS); err != nil {
			return 0, 0, true, err
		}
		duration, fps = value.Duration, value.FPS
	}
}
