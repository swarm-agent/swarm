package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A confirmed client tool's call must carry a short summary the person reads
// before approving it. Swarm adds this required argument to the schema the
// model is offered and removes it before the call reaches the client, so the
// client receives exactly the arguments its own schema declares.
const (
	ClientToolSummaryArgument = "summary"
	ClientToolSummaryMaxChars = 300
)

const clientToolSummaryDescription = "One short sentence, shown to the person before anything happens, saying exactly what this call will do. They must approve it first."

// RejectClientToolSummaryCollision refuses a confirmed tool whose own schema
// already defines the argument Swarm adds.
func RejectClientToolSummaryCollision(schema map[string]any) error {
	if properties, ok := schema["properties"].(map[string]any); ok {
		if _, taken := properties[ClientToolSummaryArgument]; taken {
			return fmt.Errorf("a confirmed client tool's input_schema cannot define %q: Swarm adds that argument for the confirmation card", ClientToolSummaryArgument)
		}
	}
	return nil
}

// ConfirmedClientToolSchema returns schema with the required summary argument
// added. schema itself is not modified.
func ConfirmedClientToolSchema(schema map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("client tool input_schema is not JSON: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errors.New("client tool input_schema is not a JSON object")
	}
	if err := RejectClientToolSummaryCollision(out); err != nil {
		return nil, err
	}
	properties, _ := out["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties[ClientToolSummaryArgument] = map[string]any{
		"type": "string", "minLength": 1, "maxLength": ClientToolSummaryMaxChars,
		"description": clientToolSummaryDescription,
	}
	out["properties"] = properties
	required, _ := out["required"].([]any)
	out["required"] = append(append([]any(nil), required...), ClientToolSummaryArgument)
	return out, nil
}

// ValidateConfirmedClientToolArguments checks a model's raw arguments for a
// confirmed tool and splits them into the tool's own arguments and the
// summary. The returned arguments are valid against the declared schema.
func ValidateConfirmedClientToolArguments(schema map[string]any, arguments string) (map[string]any, string, error) {
	withSummary, err := ConfirmedClientToolSchema(schema)
	if err != nil {
		return nil, "", err
	}
	instance, err := ValidateClientToolArguments(withSummary, arguments)
	if err != nil {
		return nil, "", err
	}
	summary, _ := instance[ClientToolSummaryArgument].(string)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, "", errors.New("arguments do not match the tool's schema: summary must not be blank")
	}
	delete(instance, ClientToolSummaryArgument)
	raw, err := json.Marshal(instance)
	if err != nil {
		return nil, "", err
	}
	// The client receives exactly these arguments: check them against the
	// declared schema as well.
	toolArgs, err := ValidateClientToolArguments(schema, string(raw))
	if err != nil {
		return nil, "", err
	}
	return toolArgs, summary, nil
}
