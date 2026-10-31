package claudecode

import (
	"sort"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

// The Claude Code CLI knows Swarm's tools only by the names it gives MCP
// tools, mcpToolPrefix + the tool's name (mcp__swarm__manage_projects). A
// call by the bare name fails in the CLI itself ("No such tool available")
// before it reaches the endpoint, so the endpoint cannot take the bare name
// as an alias; the instructions must name the tools as the CLI does instead.

// mcpToolInstructions is the system prompt as the CLI's model must read it:
// every tool name of the turn (an identifier with "_" or "-", such as
// manage_projects) written with its prefix, and one closing sentence that
// says the prefix is required and lists every tool by its prefixed name.
// Tool names that are plain words (bash, read, list) are left in the prose,
// where they are ordinary English, and are named prefixed in that list.
func mcpToolInstructions(instructions string, tools []provideriface.ToolDefinition) string {
	names := mcpToolNames(tools)
	if len(names) == 0 {
		return instructions
	}
	// Longest first, so a name that contains another is prefixed whole.
	byLength := append([]string(nil), names...)
	sort.SliceStable(byLength, func(i, j int) bool { return len(byLength[i]) > len(byLength[j]) })
	for _, name := range byLength {
		if strings.ContainsAny(name, "_-") {
			instructions = prefixToolName(instructions, name)
		}
	}
	prefixed := make([]string, 0, len(names))
	for _, name := range names {
		prefixed = append(prefixed, mcpToolPrefix+name)
	}
	note := "Tool names: the CLI knows your tools only by their " + mcpToolPrefix + " names, and a call without the prefix fails. Call them as: " + strings.Join(prefixed, ", ") + "."
	if strings.TrimSpace(instructions) == "" {
		return note
	}
	return strings.TrimRight(instructions, "\n") + "\n\n" + note
}

// mcpToolNames are the turn's tool names as the endpoint lists them, sorted.
func mcpToolNames(tools []provideriface.ToolDefinition) []string {
	seen := map[string]bool{}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" || seen[name] || !mcpToolNamePattern.MatchString(name) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// prefixToolName writes each standalone occurrence of name with the CLI's
// prefix: not inside a longer identifier, and not already prefixed.
func prefixToolName(text, name string) string {
	var out strings.Builder
	for {
		i := strings.Index(text, name)
		if i < 0 {
			out.WriteString(text)
			return out.String()
		}
		end := i + len(name)
		standalone := (i == 0 || !toolNameByte(text[i-1])) && (end == len(text) || !toolNameByte(text[end]))
		out.WriteString(text[:i])
		if standalone {
			out.WriteString(mcpToolPrefix)
		}
		out.WriteString(name)
		text = text[end:]
	}
}

func toolNameByte(b byte) bool {
	return b == '_' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
