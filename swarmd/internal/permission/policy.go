package permission

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

type PolicyDecision string

type PolicyRuleKind string

const (
	PolicyDecisionAllow PolicyDecision = "allow"
	PolicyDecisionAsk   PolicyDecision = "ask"
	PolicyDecisionDeny  PolicyDecision = "deny"

	PolicyRuleKindTool       PolicyRuleKind = "tool"
	PolicyRuleKindBashPrefix PolicyRuleKind = "bash_prefix"
	PolicyRuleKindPhrase     PolicyRuleKind = "phrase"
)

type Policy struct {
	Version   int          `json:"version"`
	Rules     []PolicyRule `json:"rules,omitempty"`
	UpdatedAt int64        `json:"updated_at,omitempty"`
}

type PolicyRule struct {
	ID        string         `json:"id"`
	Kind      PolicyRuleKind `json:"kind"`
	Decision  PolicyDecision `json:"decision"`
	Tool      string         `json:"tool,omitempty"`
	Pattern   string         `json:"pattern,omitempty"`
	CreatedAt int64          `json:"created_at,omitempty"`
	UpdatedAt int64          `json:"updated_at,omitempty"`
}

type PolicyExplain struct {
	Decision    PolicyDecision `json:"decision"`
	Source      string         `json:"source"`
	Reason      string         `json:"reason"`
	ToolName    string         `json:"tool_name,omitempty"`
	Command     string         `json:"command,omitempty"`
	Rule        *PolicyRule    `json:"rule,omitempty"`
	RulePreview string         `json:"rule_preview,omitempty"`
}

type policyEvalContext struct {
	ToolName       string
	ToolArguments  string
	NormalizedArgs string
	BashCommand    string
	BashPrefix     string
}

func DefaultPolicy() Policy {
	return Policy{
		Version: 1,
		Rules: []PolicyRule{
			{ID: "default_deny_bash_rm_root", Kind: PolicyRuleKindPhrase, Decision: PolicyDecisionDeny, Tool: "bash", Pattern: "rm -rf /"},
			{ID: "default_deny_bash_rm_root_glob", Kind: PolicyRuleKindPhrase, Decision: PolicyDecisionDeny, Tool: "bash", Pattern: "rm -rf /*"},
		},
	}
}

func NormalizePolicy(policy Policy) Policy {
	if policy.Version <= 0 {
		policy.Version = 1
	}
	now := time.Now().UnixMilli()
	out := make([]PolicyRule, 0, len(policy.Rules))
	for _, rule := range policy.Rules {
		normalized, ok := normalizePolicyRule(rule, now)
		if !ok {
			continue
		}
		out = append(out, normalized)
	}
	policy.Rules = out
	if policy.UpdatedAt < 0 {
		policy.UpdatedAt = 0
	}
	return policy
}

func ExplainPolicy(mode, toolName, toolArguments string, policy Policy) PolicyExplain {
	ctx := buildPolicyEvalContext(toolName, toolArguments)
	policy = NormalizePolicy(policy)
	mode, bypass := splitPolicyMode(mode)
	if explain, ok := explainDangerousBashDeny(ctx); ok {
		return explain
	}
	if explain, ok := explainPhraseDeny(ctx, policy); ok {
		return explain
	}
	if explain, ok := explainExplicitRule(ctx, policy); ok {
		return explain
	}
	if explain, ok := explainBuiltinDeny(mode, ctx); ok {
		return explain
	}
	if explain, ok := explainBuiltinAllow(mode, ctx); ok {
		return explain
	}
	decision := defaultPolicyDecision(policyModeWithBypass(mode, bypass), ctx.ToolName, toolArguments)
	return PolicyExplain{
		Decision:    decision,
		Source:      "default",
		Reason:      defaultPolicyReason(decision, ctx.ToolName),
		ToolName:    ctx.ToolName,
		Command:     ctx.BashCommand,
		RulePreview: previewPolicyRule(policyRuleFromContext(ctx, PolicyDecisionAllow)),
	}
}

func policyRuleFromToolCall(toolName, toolArguments string, decision PolicyDecision) (PolicyRule, bool) {
	ctx := buildPolicyEvalContext(toolName, toolArguments)
	rule := policyRuleFromContext(ctx, decision)
	if strings.TrimSpace(rule.ID) == "" && strings.TrimSpace(rule.Tool) == "" && strings.TrimSpace(rule.Pattern) == "" {
		return PolicyRule{}, false
	}
	return rule, true
}

func previewPolicyRule(rule PolicyRule) string {
	decision := strings.TrimSpace(string(rule.Decision))
	if decision == "" {
		decision = string(PolicyDecisionAllow)
	}
	switch rule.Kind {
	case PolicyRuleKindBashPrefix:
		return fmt.Sprintf("%s bash prefix: %s", decision, strings.TrimSpace(rule.Pattern))
	case PolicyRuleKindPhrase:
		if tool := strings.TrimSpace(rule.Tool); tool != "" {
			return fmt.Sprintf("%s %s phrase: %s", decision, tool, strings.TrimSpace(rule.Pattern))
		}
		return fmt.Sprintf("%s phrase: %s", decision, strings.TrimSpace(rule.Pattern))
	case PolicyRuleKindTool:
		fallthrough
	default:
		return fmt.Sprintf("%s tool: %s", decision, strings.TrimSpace(rule.Tool))
	}
}

func buildPolicyEvalContext(toolName, toolArguments string) policyEvalContext {
	toolName = normalizePolicyToolName(toolName)
	toolArguments = strings.TrimSpace(toolArguments)
	ctx := policyEvalContext{
		ToolName:       toolName,
		ToolArguments:  toolArguments,
		NormalizedArgs: strings.ToLower(toolArguments),
	}
	if toolName == "bash" {
		ctx.BashCommand = extractNormalizedBashCommand(toolArguments)
		ctx.BashPrefix = extractBashCommandPrefix(ctx.BashCommand)
	}
	return ctx
}

func explainDangerousBashDeny(ctx policyEvalContext) (PolicyExplain, bool) {
	blockedTarget, ok := dangerousRecursiveDeleteTarget(ctx.BashCommand)
	if !ok {
		return PolicyExplain{}, false
	}
	return PolicyExplain{
		Decision:    PolicyDecisionDeny,
		Source:      "builtin",
		Reason:      fmt.Sprintf("dangerous recursive delete target is blocked: %s", blockedTarget),
		ToolName:    ctx.ToolName,
		Command:     ctx.BashCommand,
		RulePreview: fmt.Sprintf("deny dangerous bash delete target: %s", blockedTarget),
	}, true
}

func dangerousRecursiveDeleteTarget(command string) (string, bool) {
	command = strings.TrimSpace(strings.ToLower(command))
	if command == "" {
		return "", false
	}
	tokens := strings.Fields(command)
	if len(tokens) == 0 {
		return "", false
	}
	start := bashCommandStartIndex(tokens)
	if start < 0 || start >= len(tokens) {
		return "", false
	}
	if path.Base(cleanShellToken(tokens[start])) != "rm" {
		return "", false
	}

	recursive := false
	parsingFlags := true
	for _, raw := range tokens[start+1:] {
		token := cleanShellToken(raw)
		if token == "" {
			continue
		}
		if parsingFlags {
			if token == "--" {
				parsingFlags = false
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.Contains(token, "r") {
					recursive = true
				}
				continue
			}
			parsingFlags = false
		}
		if recursive && isDangerousRecursiveDeleteTarget(token) {
			return token, true
		}
	}
	return "", false
}

func cleanShellToken(token string) string {
	token = strings.TrimSpace(token)
	for len(token) >= 2 {
		if (strings.HasPrefix(token, "\"") && strings.HasSuffix(token, "\"")) ||
			(strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'")) ||
			(strings.HasPrefix(token, "`") && strings.HasSuffix(token, "`")) {
			token = strings.TrimSpace(token[1 : len(token)-1])
			continue
		}
		break
	}
	switch token {
	case "~/":
		return "~"
	case "./":
		return "."
	case "../":
		return ".."
	case "$home/":
		return "$home"
	case "${home}/":
		return "${home}"
	case "$pwd/":
		return "$pwd"
	case "${pwd}/":
		return "${pwd}"
	default:
		return token
	}
}

func isDangerousRecursiveDeleteTarget(token string) bool {
	switch token {
	case "/", "/*", "~", "~/*", "$home", "$home/*", "${home}", "${home}/*", ".", "./*", "$pwd", "$pwd/*", "${pwd}", "${pwd}/*", "..", "../*", "*":
		return true
	default:
		return false
	}
}

func explainPhraseDeny(ctx policyEvalContext, policy Policy) (PolicyExplain, bool) {
	for _, rule := range policy.Rules {
		if rule.Kind != PolicyRuleKindPhrase || rule.Decision != PolicyDecisionDeny {
			continue
		}
		if !policyRuleMatches(ctx, rule) {
			continue
		}
		matched := rule
		return PolicyExplain{
			Decision:    PolicyDecisionDeny,
			Source:      "rule",
			Reason:      fmt.Sprintf("denied by %s", previewPolicyRule(matched)),
			ToolName:    ctx.ToolName,
			Command:     ctx.BashCommand,
			Rule:        &matched,
			RulePreview: previewPolicyRule(matched),
		}, true
	}
	return PolicyExplain{}, false
}

func explainExplicitRule(ctx policyEvalContext, policy Policy) (PolicyExplain, bool) {
	for _, rule := range policy.Rules {
		if rule.Kind == PolicyRuleKindPhrase && rule.Decision == PolicyDecisionDeny {
			continue
		}
		if !policyRuleMatches(ctx, rule) {
			continue
		}
		matched := rule
		return PolicyExplain{
			Decision:    matched.Decision,
			Source:      "rule",
			Reason:      fmt.Sprintf("matched %s", previewPolicyRule(matched)),
			ToolName:    ctx.ToolName,
			Command:     ctx.BashCommand,
			Rule:        &matched,
			RulePreview: previewPolicyRule(matched),
		}, true
	}
	return PolicyExplain{}, false
}

func explainBuiltinDeny(mode string, ctx policyEvalContext) (PolicyExplain, bool) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "plan" {
		switch ctx.ToolName {
		case "write", "edit":
			return PolicyExplain{
				Decision:    PolicyDecisionDeny,
				Source:      "builtin",
				Reason:      fmt.Sprintf("%s is unavailable in plan mode", ctx.ToolName),
				ToolName:    ctx.ToolName,
				Command:     ctx.BashCommand,
				RulePreview: "",
			}, true
		}
	}
	if mode == "read" {
		switch ctx.ToolName {
		case "write", "edit", "bash":
			return PolicyExplain{
				Decision:    PolicyDecisionDeny,
				Source:      "builtin",
				Reason:      fmt.Sprintf("%s is unavailable for read execution setting", ctx.ToolName),
				ToolName:    ctx.ToolName,
				Command:     ctx.BashCommand,
				RulePreview: "",
			}, true
		}
	}
	if mode == "readwrite" {
		if ctx.ToolName == "bash" {
			return PolicyExplain{
				Decision:    PolicyDecisionDeny,
				Source:      "builtin",
				Reason:      "bash is unavailable for readwrite execution setting",
				ToolName:    ctx.ToolName,
				Command:     ctx.BashCommand,
				RulePreview: "",
			}, true
		}
	}
	return PolicyExplain{}, false
}

func explainBuiltinAllow(mode string, ctx policyEvalContext) (PolicyExplain, bool) {
	if ctx.ToolName != "bash" {
		return PolicyExplain{}, false
	}
	for _, prefix := range []string{"cd", "ls"} {
		if !hasCommandPrefix(ctx.BashCommand, prefix) {
			continue
		}
		return PolicyExplain{
			Decision:    PolicyDecisionAllow,
			Source:      "builtin",
			Reason:      fmt.Sprintf("built-in allow for bash command prefix: %s", prefix),
			ToolName:    ctx.ToolName,
			Command:     ctx.BashCommand,
			RulePreview: fmt.Sprintf("allow bash command prefix: %s", prefix),
		}, true
	}
	return PolicyExplain{}, false
}

func policyRuleFromContext(ctx policyEvalContext, decision PolicyDecision) PolicyRule {
	now := time.Now().UnixMilli()
	rule := PolicyRule{Decision: decision, CreatedAt: now, UpdatedAt: now}
	if ctx.ToolName == "bash" && strings.TrimSpace(ctx.BashPrefix) != "" {
		rule.Kind = PolicyRuleKindBashPrefix
		rule.Tool = "bash"
		rule.Pattern = ctx.BashPrefix
		return rule
	}
	if ctx.ToolName != "" {
		rule.Kind = PolicyRuleKindTool
		rule.Tool = ctx.ToolName
		return rule
	}
	return PolicyRule{}
}

func policyRuleMatches(ctx policyEvalContext, rule PolicyRule) bool {
	switch rule.Kind {
	case PolicyRuleKindPhrase:
		if rule.Tool != "" && normalizePolicyToolName(rule.Tool) != ctx.ToolName {
			return false
		}
		phrase := strings.ToLower(strings.TrimSpace(rule.Pattern))
		if phrase == "" {
			return false
		}
		haystack := ctx.NormalizedArgs
		if ctx.BashCommand != "" {
			haystack = strings.ToLower(ctx.BashCommand)
		}
		return strings.Contains(haystack, phrase)
	case PolicyRuleKindBashPrefix:
		if ctx.ToolName != "bash" {
			return false
		}
		return hasBashCommandPrefix(ctx.BashCommand, rule.Pattern)
	case PolicyRuleKindTool:
		return ctx.ToolName != "" && ctx.ToolName == normalizePolicyToolName(rule.Tool)
	default:
		return false
	}
}

func normalizePolicyRule(rule PolicyRule, now int64) (PolicyRule, bool) {
	rule.Kind = PolicyRuleKind(strings.TrimSpace(strings.ToLower(string(rule.Kind))))
	rule.Decision = PolicyDecision(strings.TrimSpace(strings.ToLower(string(rule.Decision))))
	rule.Tool = normalizePolicyToolName(rule.Tool)
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	if rule.Kind == "" || rule.Decision == "" {
		return PolicyRule{}, false
	}
	switch rule.Decision {
	case PolicyDecisionAllow, PolicyDecisionAsk, PolicyDecisionDeny:
	default:
		return PolicyRule{}, false
	}
	switch rule.Kind {
	case PolicyRuleKindTool:
		if rule.Tool == "" {
			return PolicyRule{}, false
		}
		rule.Pattern = ""
	case PolicyRuleKindBashPrefix:
		rule.Tool = "bash"
		rule.Pattern = strings.ToLower(strings.Join(strings.Fields(rule.Pattern), " "))
		if rule.Pattern == "" {
			return PolicyRule{}, false
		}
	case PolicyRuleKindPhrase:
		rule.Pattern = strings.ToLower(rule.Pattern)
		if rule.Pattern == "" {
			return PolicyRule{}, false
		}
	default:
		return PolicyRule{}, false
	}
	rule.ID = strings.TrimSpace(rule.ID)
	if rule.CreatedAt <= 0 {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now
	return rule, true
}

func normalizePolicyToolName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if dot := strings.LastIndex(name, "."); dot >= 0 && dot+1 < len(name) {
		name = name[dot+1:]
	}
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuser":
		return "ask_user"
	case "exitplanmode":
		return "exit_plan_mode"
	case "managetheme":
		return "manage_theme"
	case "manageimage":
		return "manage_image"
	default:
		return name
	}
}

func policyRuleSignature(rule PolicyRule) string {
	normalized := NormalizePolicy(Policy{Rules: []PolicyRule{rule}}).Rules
	if len(normalized) == 0 {
		return ""
	}
	rule = normalized[0]
	return strings.Join([]string{
		string(rule.Kind),
		string(rule.Decision),
		rule.Tool,
		rule.Pattern,
	}, "\x00")
}

func extractNormalizedBashCommand(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return ""
	}
	return strings.ToLower(strings.Join(strings.Fields(payload.Command), " "))
}

func extractBashCommandPrefix(command string) string {
	command = strings.TrimSpace(strings.ToLower(command))
	if command == "" {
		return ""
	}
	tokens := strings.Fields(command)
	start := bashScriptStartIndex(tokens, bashCommandStartIndex(tokens))
	if start < 0 || start >= len(tokens) {
		return ""
	}
	return path.Base(cleanShellToken(tokens[start]))
}

func bashCommandStartIndex(tokens []string) int {
	for i := 0; i < len(tokens); i++ {
		token := cleanShellToken(tokens[i])
		if token == "" {
			continue
		}
		if isCommandWrapper(token) {
			continue
		}
		if strings.Contains(token, "=") && !strings.HasPrefix(token, "-") {
			continue
		}
		return i
	}
	return -1
}

func bashScriptStartIndex(tokens []string, commandStart int) int {
	for i := commandStart; i >= 0 && i < len(tokens); i++ {
		token := cleanShellToken(tokens[i])
		if token == "" {
			continue
		}
		if isCommandWrapper(token) || (strings.Contains(token, "=") && !strings.HasPrefix(token, "-")) {
			continue
		}
		if !isShellInterpreter(token) {
			return i
		}
		for j := i + 1; j < len(tokens); j++ {
			token = cleanShellToken(tokens[j])
			if token == "" {
				continue
			}
			if token == "--" {
				continue
			}
			if isShellOptionWithValue(token) {
				j++
				continue
			}
			if strings.HasPrefix(token, "-") {
				continue
			}
			return j
		}
		return -1
	}
	return -1
}

func isCommandWrapper(token string) bool {
	switch path.Base(cleanShellToken(token)) {
	case "sudo", "env", "command":
		return true
	default:
		return false
	}
}

func isShellInterpreter(token string) bool {
	switch path.Base(cleanShellToken(token)) {
	case "bash", "sh", "zsh", "dash", "ksh":
		return true
	default:
		return false
	}
}

func isShellOptionWithValue(token string) bool {
	switch token {
	case "-c", "--command", "-o", "--option", "--init-file", "--rcfile":
		return true
	default:
		return false
	}
}

func hasCommandPrefix(command, prefix string) bool {
	command = strings.TrimSpace(strings.ToLower(command))
	prefix = strings.TrimSpace(strings.ToLower(prefix))
	if command == "" || prefix == "" {
		return false
	}
	return command == prefix || strings.HasPrefix(command, prefix+" ")
}

func hasBashCommandPrefix(command, prefix string) bool {
	command = strings.TrimSpace(strings.ToLower(command))
	prefix = path.Base(cleanShellToken(strings.TrimSpace(strings.ToLower(prefix))))
	if command == "" || prefix == "" {
		return false
	}
	tokens := strings.Fields(command)
	start := bashScriptStartIndex(tokens, bashCommandStartIndex(tokens))
	if start < 0 || start >= len(tokens) {
		return false
	}
	script := path.Base(cleanShellToken(tokens[start]))
	return script == prefix || strings.HasPrefix(script, prefix+" ")
}

func splitPolicyMode(mode string) (string, bool) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	bypass := false
	if !strings.Contains(mode, "+") {
		return mode, false
	}
	parts := strings.Split(mode, "+")
	mode = strings.TrimSpace(parts[0])
	for _, part := range parts[1:] {
		if strings.TrimSpace(part) == "bypass_permissions" {
			bypass = true
		}
	}
	return mode, bypass
}

func policyModeWithBypass(mode string, bypass bool) string {
	if !bypass {
		return mode
	}
	if mode == "" {
		return "bypass_permissions"
	}
	return mode + "+bypass_permissions"
}

func defaultPolicyDecision(mode, toolName, toolArguments string) PolicyDecision {
	toolName = normalizePolicyToolName(toolName)
	mode, bypass := splitPolicyMode(mode)
	switch toolName {
	case "read", "search", "websearch", "webfetch", "agentic_search", "list", "skill_use", "manage_worktree", "manage_todos", "manage_theme":
		return PolicyDecisionAllow
	case "manage_image":
		if ShouldApproveManageImage(toolArguments) {
			return PolicyDecisionAsk
		}
		return PolicyDecisionAllow
	case "plan_manage":
		if ShouldApprovePlanManageUpdate(toolArguments) {
			return PolicyDecisionAsk
		}
		return PolicyDecisionAllow
	case "manage_skill":
		if bypass {
			return PolicyDecisionAllow
		}
		return PolicyDecisionAsk
	case "manage_agent":
		if ShouldApproveManageAgentMutation(toolArguments) {
			return PolicyDecisionAsk
		}
		return PolicyDecisionAllow
	case "task":
		return PolicyDecisionAsk
	case "ask_user", "exit_plan_mode":
		return PolicyDecisionAsk
	case "write", "edit":
		if mode == "read" {
			return PolicyDecisionDeny
		}
		return PolicyDecisionAllow
	case "bash":
		if mode == "read" || mode == "readwrite" {
			return PolicyDecisionDeny
		}
		if bypass {
			return PolicyDecisionAllow
		}
		return PolicyDecisionAsk
	default:
		if bypass {
			return PolicyDecisionAllow
		}
		return PolicyDecisionAsk
	}
}

func defaultPolicyReason(decision PolicyDecision, toolName string) string {
	switch decision {
	case PolicyDecisionAllow:
		return fmt.Sprintf("default allow for %s", toolName)
	case PolicyDecisionDeny:
		return fmt.Sprintf("default deny for %s", toolName)
	default:
		return fmt.Sprintf("default ask for %s", toolName)
	}
}
