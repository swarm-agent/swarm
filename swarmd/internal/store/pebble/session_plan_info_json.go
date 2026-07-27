package pebblestore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON keeps the stored/API shape canonical while accepting the
// field-shape variants models have historically emitted in tool arguments.
// The canonical scope and validation_strategy fields are strings; array inputs
// are joined. The canonical decisions field is a string array; a single string
// is promoted instead of failing full document parsing.
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
		Context:         wire.Context,
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
	if valueRaw, ok := fields["scope"]; ok {
		value, err := decodeSessionPlanInfoStringOrArray(valueRaw, "scope")
		if err != nil {
			return err
		}
		out.Scope = value
	}
	if valueRaw, ok := fields["decisions"]; ok {
		values, err := decodeSessionPlanInfoStringSlice(valueRaw, "decisions")
		if err != nil {
			return err
		}
		out.Decisions = values
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
	Context         string   `json:"context,omitempty"`
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

func decodeSessionPlanInfoStringSlice(raw json.RawMessage, field string) ([]string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var values []string
	if err := json.Unmarshal(raw, &values); err == nil {
		return trimSessionPlanInfoStrings(values), nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		if value = strings.TrimSpace(value); value != "" {
			return []string{value}, nil
		}
		return nil, nil
	}

	return nil, fmt.Errorf("session plan info %s must be a string or string array", field)
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
