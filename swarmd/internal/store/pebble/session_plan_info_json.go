package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON keeps the stored/API shape canonical while accepting the
// validation aliases models have historically emitted in tool arguments. The
// canonical field is validation_strategy as a string; array inputs are joined
// into a readable string instead of failing full document parsing.
func (info *SessionPlanInfo) UnmarshalJSON(raw []byte) error {
	if info == nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		*info = SessionPlanInfo{}
		return nil
	}

	var wire sessionPlanInfoWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	out := SessionPlanInfo{
		Goal:            wire.Goal,
		Scope:           wire.Scope,
		Context:         wire.Context,
		Decisions:       wire.Decisions,
		Constraints:     wire.Constraints,
		Assumptions:     wire.Assumptions,
		OpenQuestions:   wire.OpenQuestions,
		RelevantFiles:   wire.RelevantFiles,
		SuccessCriteria: wire.SuccessCriteria,
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"validation_strategy", "validationStrategy", "validation"} {
		valueRaw, ok := fields[key]
		if !ok {
			continue
		}
		value, err := decodeSessionPlanInfoStringOrArray(valueRaw, key)
		if err != nil {
			return err
		}
		out.ValidationStrategy = value
		break
	}

	*info = out
	return nil
}

type sessionPlanInfoWire struct {
	Goal            string   `json:"goal,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	Context         string   `json:"context,omitempty"`
	Decisions       []string `json:"decisions,omitempty"`
	Constraints     []string `json:"constraints,omitempty"`
	Assumptions     []string `json:"assumptions,omitempty"`
	OpenQuestions   []string `json:"open_questions,omitempty"`
	RelevantFiles   []string `json:"relevant_files,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
}

func decodeSessionPlanInfoStringOrArray(raw json.RawMessage, field string) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value), nil
	}

	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return strings.Join(trimSessionPlanInfoStrings(values), "; "), nil
	}

	return "", fmt.Errorf("session plan info %s must be a string or string array", field)
}

func trimSessionPlanInfoStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
