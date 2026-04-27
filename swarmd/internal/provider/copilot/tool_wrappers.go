package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/github/copilot-sdk/go"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

func buildToolWrappers(ctx context.Context, definitions []provideriface.ToolDefinition, invoker provideriface.ToolInvoker, onRestartTurn func()) ([]sdk.Tool, []string, error) {
	if len(definitions) == 0 {
		return nil, nil, nil
	}
	if invoker == nil {
		return nil, nil, fmt.Errorf("copilot wrapper tools require a tool invoker")
	}

	tools := make([]sdk.Tool, 0, len(definitions))
	available := make([]string, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))

	for _, definition := range definitions {
		originalName := strings.TrimSpace(definition.Name)
		if originalName == "" {
			continue
		}
		wrapperName := wrapperToolName(originalName)
		if wrapperName == "" {
			return nil, nil, fmt.Errorf("invalid copilot wrapper name for tool %q", originalName)
		}
		if _, exists := seen[wrapperName]; exists {
			return nil, nil, fmt.Errorf("duplicate copilot wrapper tool %q", wrapperName)
		}
		seen[wrapperName] = struct{}{}

		description := strings.TrimSpace(definition.Description)
		if description == "" {
			description = fmt.Sprintf("Swarm wrapper for %s", originalName)
		}
		parameters := cloneDefinitionParameters(definition.Parameters)
		tools = append(tools, sdk.Tool{
			Name:        wrapperName,
			Description: description,
			Parameters:  parameters,
			Handler: func(invocation sdk.ToolInvocation) (sdk.ToolResult, error) {
				arguments := "{}"
				if invocation.Arguments != nil {
					encoded, err := json.Marshal(invocation.Arguments)
					if err != nil {
						return sdk.ToolResult{}, fmt.Errorf("marshal tool arguments for %s: %w", originalName, err)
					}
					arguments = string(encoded)
				}

				result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{
					CallID:    strings.TrimSpace(invocation.ToolCallID),
					Name:      originalName,
					Arguments: arguments,
					Metadata: map[string]any{
						"copilot": map[string]any{
							"wrapper_tool_name": strings.TrimSpace(wrapperName),
							"sdk_session_id":    strings.TrimSpace(invocation.SessionID),
							"sdk_tool_call_id":  strings.TrimSpace(invocation.ToolCallID),
						},
					},
				})
				if err != nil {
					return sdk.ToolResult{}, err
				}

				text := strings.TrimSpace(result.TextForModel)
				if text == "" {
					text = strings.TrimSpace(result.Output)
				}
				resultType := "success"
				if strings.TrimSpace(result.Error) != "" {
					resultType = "failure"
				}

				if result.RestartTurn && onRestartTurn != nil {
					onRestartTurn()
				}

				return sdk.ToolResult{
					TextResultForLLM: text,
					ResultType:       resultType,
					Error:            strings.TrimSpace(result.Error),
				}, nil
			},
		})
		available = append(available, wrapperName)
	}

	return tools, available, nil
}

func buildPermissionHandler(allowedToolNames []string) sdk.PermissionHandlerFunc {
	allowed := make(map[string]struct{}, len(allowedToolNames))
	for _, name := range allowedToolNames {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}

	return func(request sdk.PermissionRequest, _ sdk.PermissionInvocation) (sdk.PermissionRequestResult, error) {
		if request.Kind == sdk.PermissionRequestKindCustomTool {
			if name := strings.TrimSpace(stringValue(request.ToolName)); name != "" {
				if _, ok := allowed[name]; ok {
					return sdk.PermissionRequestResult{Kind: sdk.PermissionRequestResultKindApproved}, nil
				}
			}
		}
		return sdk.PermissionRequestResult{Kind: sdk.PermissionRequestResultKindUserNotAvailable}, nil
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func buildCustomAgents(instructions string, availableToolNames []string) []sdk.CustomAgentConfig {
	prompt := strings.TrimSpace(instructions)
	if prompt == "" {
		prompt = "Use only the provided Swarm wrapper tools when tool use is required."
	}
	return []sdk.CustomAgentConfig{
		{
			Name:        "swarm",
			DisplayName: "Swarm",
			Description: "Swarm local workspace agent with explicit wrapper tools",
			Tools:       append([]string(nil), availableToolNames...),
			Prompt:      prompt,
			Infer:       sdk.Bool(true),
		},
	}
}

func wrapperToolName(name string) string {
	name = canonicalToolName(name)
	if name == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("swarm_")
	lastUnderscore := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastUnderscore = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				builder.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_")
}

func canonicalToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ask-user", "ask_user":
		return "ask_user"
	case "exit-plan-mode", "exit_plan_mode":
		return "exit_plan_mode"
	case "plan-manage", "plan_manage":
		return "plan_manage"
	case "skill-use", "skill_use":
		return "skill_use"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func cloneDefinitionParameters(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneDefinitionValue(value)
	}
	return out
}

func cloneDefinitionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneDefinitionParameters(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneDefinitionValue(item))
		}
		return out
	default:
		return value
	}
}
