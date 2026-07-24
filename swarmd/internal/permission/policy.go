package permission

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
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
	Version        int                  `json:"version"`
	BashProfile    BashApprovalProfile  `json:"bash_profile"`
	Rules          []PolicyRule         `json:"rules,omitempty"`
	Subagents      SubagentPolicy       `json:"subagents"`
	SessionDeploy  SessionDeployPolicy  `json:"session_deploy"`
	PlanAcceptance PlanAcceptancePolicy `json:"plan_acceptance"`
	UpdatedAt      int64                `json:"updated_at,omitempty"`
}

type SubagentOrchestrationMode string

type SubagentOverBudgetAction string

type CapabilityPolicyMode string

type BashApprovalProfile string

type BashEffectCategory string

type SessionDeployOverLimitAction string

const (
	SubagentModeDirect  SubagentOrchestrationMode = "direct"
	SubagentModeAsk     SubagentOrchestrationMode = "ask"
	SubagentModeBounded SubagentOrchestrationMode = "bounded"

	SubagentOverBudgetAsk  SubagentOverBudgetAction = "ask"
	SubagentOverBudgetDeny SubagentOverBudgetAction = "deny"

	CapabilityModeAsk         CapabilityPolicyMode = "ask"
	CapabilityModeAlwaysAllow CapabilityPolicyMode = "always_allow"
	CapabilityModeBounded     CapabilityPolicyMode = "bounded"

	BashApprovalProfileCurrentRules        BashApprovalProfile = "current_rules"
	BashApprovalProfileAllowEveryRead      BashApprovalProfile = "allow_every_read"
	BashApprovalProfileAllowSafeReads      BashApprovalProfile = "allow_safe_reads"
	BashApprovalProfileOnlyCriticalPrompts BashApprovalProfile = "only_critical_prompts"

	BashEffectRead   BashEffectCategory = "read"
	BashEffectWrite  BashEffectCategory = "write"
	BashEffectUpdate BashEffectCategory = "update"
	BashEffectDelete BashEffectCategory = "delete"

	SessionDeployOverLimitAsk  SessionDeployOverLimitAction = "ask"
	SessionDeployOverLimitDeny SessionDeployOverLimitAction = "deny"

	// These are validation safety bounds, not orchestration defaults. Account policy
	// remains authoritative within them and can support substantial refactor waves.
	MaxSubagentWaveSize = 256
	MaxSubagentDepth    = 16
)

// SessionDeployPolicy controls only durable manage-sessions deployment. It is
// intentionally separate from generic manage_sessions tool rules.
type SessionDeployPolicy struct {
	Mode                             CapabilityPolicyMode         `json:"mode"`
	AutomaticDeploymentsPerParentRun int                          `json:"automatic_deployments_per_parent_run"`
	OverLimitAction                  SessionDeployOverLimitAction `json:"over_limit_action"`
}

// PlanAcceptancePolicy controls only structured plan acceptance boundaries. The
// validated backend-owned document and canonical arguments remain authoritative.
type PlanAcceptancePolicy struct {
	Mode CapabilityPolicyMode `json:"mode"`
}

// SubagentPolicy is the single account-scoped delegation policy. Launches share one
// budget regardless of child purpose (for example Explorer or Clone).
type SubagentPolicy struct {
	Mode                          SubagentOrchestrationMode `json:"mode"`
	AutomaticLaunchesPerParentRun int                       `json:"automatic_launches_per_parent_run"`
	ActiveChildLimit              int                       `json:"active_child_limit"`
	OverBudgetAction              SubagentOverBudgetAction  `json:"over_budget_action"`
	AbsoluteWaveMaximum           int                       `json:"absolute_wave_maximum"`
	MaxDepth                      int                       `json:"max_depth"`
	RequireWriteIsolation         bool                      `json:"require_write_isolation"`
}

func DefaultSessionDeployPolicy() SessionDeployPolicy {
	return SessionDeployPolicy{Mode: CapabilityModeAsk, AutomaticDeploymentsPerParentRun: 0, OverLimitAction: SessionDeployOverLimitAsk}
}

func DefaultPlanAcceptancePolicy() PlanAcceptancePolicy {
	return PlanAcceptancePolicy{Mode: CapabilityModeAsk}
}

func DefaultBashApprovalProfile() BashApprovalProfile {
	return BashApprovalProfileCurrentRules
}

func ValidateBashApprovalProfile(profile BashApprovalProfile) error {
	switch profile {
	case BashApprovalProfileCurrentRules, BashApprovalProfileAllowEveryRead, BashApprovalProfileAllowSafeReads, BashApprovalProfileOnlyCriticalPrompts:
		return nil
	default:
		return fmt.Errorf("unsupported bash approval profile %q", profile)
	}
}

func ValidateSessionDeployPolicy(policy SessionDeployPolicy) error {
	switch policy.Mode {
	case CapabilityModeAsk, CapabilityModeAlwaysAllow, CapabilityModeBounded:
	default:
		return fmt.Errorf("unsupported session deployment policy mode %q", policy.Mode)
	}
	if policy.AutomaticDeploymentsPerParentRun < 0 || policy.AutomaticDeploymentsPerParentRun > manageSessionDeployPolicyMaximum {
		return fmt.Errorf("automatic deployments per parent run must be between 0 and %d", manageSessionDeployPolicyMaximum)
	}
	switch policy.OverLimitAction {
	case SessionDeployOverLimitAsk, SessionDeployOverLimitDeny:
	default:
		return fmt.Errorf("unsupported session deployment over-limit action %q", policy.OverLimitAction)
	}
	return nil
}

func ValidatePlanAcceptancePolicy(policy PlanAcceptancePolicy) error {
	switch policy.Mode {
	case CapabilityModeAsk, CapabilityModeAlwaysAllow:
		return nil
	default:
		return fmt.Errorf("unsupported plan acceptance policy mode %q", policy.Mode)
	}
}

const manageSessionDeployPolicyMaximum = 256

func DefaultSubagentPolicy() SubagentPolicy {
	return SubagentPolicy{
		Mode:                          SubagentModeBounded,
		AutomaticLaunchesPerParentRun: 5,
		ActiveChildLimit:              5,
		OverBudgetAction:              SubagentOverBudgetAsk,
		AbsoluteWaveMaximum:           16,
		MaxDepth:                      2,
		RequireWriteIsolation:         true,
	}
}

func ValidateSubagentPolicy(policy SubagentPolicy) error {
	switch policy.Mode {
	case SubagentModeDirect, SubagentModeAsk, SubagentModeBounded:
	default:
		return fmt.Errorf("unsupported subagent orchestration mode %q", policy.Mode)
	}
	switch policy.OverBudgetAction {
	case SubagentOverBudgetAsk, SubagentOverBudgetDeny:
	default:
		return fmt.Errorf("unsupported subagent over-budget action %q", policy.OverBudgetAction)
	}
	if policy.AutomaticLaunchesPerParentRun < 0 {
		return fmt.Errorf("automatic launches per parent run cannot be negative")
	}
	if policy.ActiveChildLimit < 1 {
		return fmt.Errorf("active child limit must be at least 1")
	}
	if policy.AbsoluteWaveMaximum < 1 || policy.AbsoluteWaveMaximum > MaxSubagentWaveSize {
		return fmt.Errorf("absolute wave maximum must be between 1 and %d", MaxSubagentWaveSize)
	}
	if policy.ActiveChildLimit > MaxSubagentWaveSize {
		return fmt.Errorf("active child limit cannot exceed %d", MaxSubagentWaveSize)
	}
	if policy.AutomaticLaunchesPerParentRun > MaxSubagentWaveSize {
		return fmt.Errorf("automatic launches per parent run cannot exceed %d", MaxSubagentWaveSize)
	}
	if policy.MaxDepth < 0 || policy.MaxDepth > MaxSubagentDepth {
		return fmt.Errorf("subagent delegation depth must be between 0 and %d", MaxSubagentDepth)
	}
	return nil
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
	Decision        PolicyDecision        `json:"decision"`
	Source          string                `json:"source"`
	Reason          string                `json:"reason"`
	ToolName        string                `json:"tool_name,omitempty"`
	Command         string                `json:"command,omitempty"`
	Rule            *PolicyRule           `json:"rule,omitempty"`
	RulePreview     string                `json:"rule_preview,omitempty"`
	BashEffect      *BashEffectAssessment `json:"bash_effect,omitempty"`
	BashProfile     BashApprovalProfile   `json:"bash_profile,omitempty"`
	ProfileDecision PolicyDecision        `json:"profile_decision,omitempty"`
	ProfileReason   string                `json:"profile_reason,omitempty"`
}

type policyEvalContext struct {
	ToolName       string
	ToolArguments  string
	NormalizedArgs string
	BashCommand    string
	BashPrefix     string
	BashEffect     BashEffectAssessment
}

type BashEffectAssessment struct {
	DeclaredCategory BashEffectCategory `json:"declared_category,omitempty"`
	Category         BashEffectCategory `json:"category"`
	Critical         bool               `json:"critical"`
	Valid            bool               `json:"valid"`
	Promoted         bool               `json:"promoted,omitempty"`
	Reason           string             `json:"reason,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Version:        1,
		BashProfile:    DefaultBashApprovalProfile(),
		Subagents:      DefaultSubagentPolicy(),
		SessionDeploy:  DefaultSessionDeployPolicy(),
		PlanAcceptance: DefaultPlanAcceptancePolicy(),
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
	policy.BashProfile = BashApprovalProfile(strings.TrimSpace(strings.ToLower(string(policy.BashProfile))))
	if err := ValidateBashApprovalProfile(policy.BashProfile); err != nil {
		policy.BashProfile = DefaultBashApprovalProfile()
	}
	if err := ValidateSubagentPolicy(policy.Subagents); err != nil {
		policy.Subagents = DefaultSubagentPolicy()
	}
	if err := ValidateSessionDeployPolicy(policy.SessionDeploy); err != nil {
		policy.SessionDeploy = DefaultSessionDeployPolicy()
	}
	if err := ValidatePlanAcceptancePolicy(policy.PlanAcceptance); err != nil {
		policy.PlanAcceptance = DefaultPlanAcceptancePolicy()
	}
	if policy.UpdatedAt < 0 {
		policy.UpdatedAt = 0
	}
	return policy
}

func ExplainPolicy(mode, toolName, toolArguments string, policy Policy) PolicyExplain {
	policy = NormalizePolicy(policy)
	ctx := buildPolicyEvalContext(toolName, toolArguments)
	explain := explainPolicyDecision(mode, toolName, toolArguments, policy)
	if ctx.ToolName != "bash" {
		return explain
	}
	assessment := ctx.BashEffect
	explain.BashEffect = &assessment
	explain.BashProfile = policy.BashProfile
	if profileExplain, ok := explainBashProfile(ctx, policy.BashProfile); ok {
		explain.ProfileDecision = profileExplain.Decision
		explain.ProfileReason = profileExplain.Reason
	} else {
		explain.ProfileDecision = explain.Decision
		if policy.BashProfile == BashApprovalProfileCurrentRules {
			explain.ProfileReason = "current rules use existing granular and default bash authorization"
		} else {
			explain.ProfileReason = "selected bash profile leaves this operation to granular and default authorization"
		}
	}
	return explain
}

func explainPolicyDecision(mode, toolName, toolArguments string, policy Policy) PolicyExplain {
	ctx := buildPolicyEvalContext(toolName, toolArguments)
	policy = NormalizePolicy(policy)
	mode, bypass := splitPolicyMode(mode)
	if explain, ok := explainDangerousBashDeny(ctx); ok {
		return explain
	}
	if explain, ok := explainPhraseDeny(ctx, policy); ok {
		return explain
	}
	// Dedicated capabilities are evaluated before generic rules and bypass so a
	// broad manage_sessions/plan_manage rule cannot authorize either boundary.
	if ctx.ToolName == "session_deploy" {
		decision := PolicyDecisionAsk
		reason := "session deployment policy requires approval"
		switch policy.SessionDeploy.Mode {
		case CapabilityModeAlwaysAllow:
			decision, reason = PolicyDecisionAllow, "session deployment capability is always allowed"
		case CapabilityModeBounded:
			// Durable per-run accounting resolves bounded calls at the approval boundary.
			decision, reason = PolicyDecisionAllow, "session deployment uses bounded per-run accounting"
		}
		return PolicyExplain{Decision: decision, Source: "session_deploy_policy", Reason: reason, ToolName: ctx.ToolName}
	}
	if ctx.ToolName == "plan_acceptance" {
		decision := PolicyDecisionAsk
		reason := "plan acceptance policy requires approval"
		if policy.PlanAcceptance.Mode == CapabilityModeAlwaysAllow {
			decision, reason = PolicyDecisionAllow, "plan acceptance capability is always allowed"
		}
		return PolicyExplain{Decision: decision, Source: "plan_acceptance_policy", Reason: reason, ToolName: ctx.ToolName}
	}
	if explain, ok := explainBuiltinDeny(mode, ctx); ok {
		return explain
	}
	if explain, ok := explainExplicitDeny(ctx, policy); ok {
		return explain
	}
	if ctx.ToolName == "bash" && !ctx.BashEffect.Valid {
		return bashProfileExplain(ctx, PolicyDecisionAsk, "bash effect metadata is malformed or contradictory: "+ctx.BashEffect.Reason)
	}
	if explain, ok := explainBashProfile(ctx, policy.BashProfile); ok {
		return explain
	}
	if explain, ok := explainExplicitRule(ctx, policy); ok {
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
	// Session mutations are separate policy capabilities even though they share the
	// manage_sessions transport tool. This prevents a generic manage_sessions rule,
	// or a rule for another mutation, from authorizing this action.
	if toolName == "exit_plan_mode" {
		toolName = "plan_acceptance"
	} else if toolName == "plan_manage" && IsPlanAcceptanceLifecycleRequirement(PlanManageLifecycleRequirement(toolArguments)) {
		toolName = "plan_acceptance"
	}
	if toolName == "manage_sessions" {
		switch {
		case ShouldApproveManageSessionsDeploy(toolArguments):
			toolName = "session_deploy"
		case ShouldApproveManageSessionsCommit(toolArguments):
			toolName = "session_commit"
		case ShouldApproveManageSessionsArchive(toolArguments):
			toolName = "session_archive"
		case ShouldApproveManageSessionsUnarchive(toolArguments):
			toolName = "session_unarchive"
		}
	}
	ctx := policyEvalContext{
		ToolName:       toolName,
		ToolArguments:  toolArguments,
		NormalizedArgs: strings.ToLower(toolArguments),
	}
	if toolName == "bash" {
		ctx.BashCommand = extractNormalizedBashCommand(toolArguments)
		ctx.BashPrefix = extractBashCommandPrefix(ctx.BashCommand)
		ctx.BashEffect = assessBashEffect(toolArguments, ctx.BashCommand)
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

func explainExplicitDeny(ctx policyEvalContext, policy Policy) (PolicyExplain, bool) {
	for _, rule := range policy.Rules {
		if rule.Decision != PolicyDecisionDeny || !policyRuleMatches(ctx, rule) {
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

func explainBashProfile(ctx policyEvalContext, profile BashApprovalProfile) (PolicyExplain, bool) {
	if ctx.ToolName != "bash" || profile == BashApprovalProfileCurrentRules {
		return PolicyExplain{}, false
	}
	assessment := ctx.BashEffect
	if !assessment.Valid {
		reason := "bash effect metadata is malformed or contradictory"
		if strings.TrimSpace(assessment.Reason) != "" {
			reason += ": " + assessment.Reason
		}
		return bashProfileExplain(ctx, PolicyDecisionAsk, reason), true
	}
	if assessment.Category == BashEffectDelete {
		return bashProfileExplain(ctx, PolicyDecisionAsk, "every bash delete requires approval"), true
	}
	if assessment.Critical {
		if profile == BashApprovalProfileAllowEveryRead && assessment.Category == BashEffectRead {
			return bashProfileExplain(ctx, PolicyDecisionAllow, "allow every read profile includes critical bash reads"), true
		}
		return bashProfileExplain(ctx, PolicyDecisionAsk, "critical bash operation requires approval"), true
	}
	switch profile {
	case BashApprovalProfileAllowEveryRead, BashApprovalProfileAllowSafeReads:
		if assessment.Category == BashEffectRead {
			return bashProfileExplain(ctx, PolicyDecisionAllow, "bash read is auto-approved by the selected profile"), true
		}
	case BashApprovalProfileOnlyCriticalPrompts:
		switch assessment.Category {
		case BashEffectRead, BashEffectWrite, BashEffectUpdate:
			return bashProfileExplain(ctx, PolicyDecisionAllow, "noncritical bash operation is auto-approved by the selected profile"), true
		}
	}
	return PolicyExplain{}, false
}

func bashProfileExplain(ctx policyEvalContext, decision PolicyDecision, reason string) PolicyExplain {
	return PolicyExplain{
		Decision: decision,
		Source:   "bash_profile",
		Reason:   reason,
		ToolName: ctx.ToolName,
		Command:  ctx.BashCommand,
	}
}

func explainExplicitRule(ctx policyEvalContext, policy Policy) (PolicyExplain, bool) {
	for _, rule := range policy.Rules {
		if rule.Decision == PolicyDecisionDeny {
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

func assessBashEffect(arguments, normalizedCommand string) BashEffectAssessment {
	invalid := func(reason string) BashEffectAssessment {
		return BashEffectAssessment{Valid: false, Reason: reason}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &payload); err != nil || payload == nil {
		return invalid("arguments must be a JSON object")
	}
	command, ok := payload["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return invalid("command is required")
	}
	explanation, ok := payload["explanation"].([]any)
	if !ok || len(explanation) == 0 {
		return invalid("explanation must contain at least one item")
	}
	for _, item := range explanation {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return invalid("explanation items must be non-empty strings")
		}
	}
	categoryText, ok := payload["category"].(string)
	if !ok {
		return invalid("category is required")
	}
	category := BashEffectCategory(strings.TrimSpace(strings.ToLower(categoryText)))
	switch category {
	case BashEffectRead, BashEffectWrite, BashEffectUpdate, BashEffectDelete:
	default:
		return invalid("category must be read, write, update, or delete")
	}
	critical, ok := payload["critical"].(bool)
	if !ok {
		return invalid("critical must be a boolean")
	}
	if category == BashEffectDelete && !critical {
		return invalid("delete requires critical=true")
	}

	assessment := BashEffectAssessment{DeclaredCategory: category, Category: category, Critical: critical, Valid: true}
	commandLower := strings.TrimSpace(strings.ToLower(normalizedCommand))
	if commandLower == "" {
		commandLower = strings.ToLower(strings.Join(strings.Fields(command), " "))
	}
	correctCategory := func(next BashEffectCategory, reason string) {
		if assessment.Category != next {
			assessment.Category = next
			assessment.Promoted = true
		}
		if assessment.Reason == "" {
			assessment.Reason = reason
		}
	}
	promote := func(next BashEffectCategory, reason string) {
		correctCategory(next, reason)
		if !assessment.Critical {
			assessment.Critical = true
			assessment.Promoted = true
		}
	}
	if obviousBashDelete(commandLower) {
		promote(BashEffectDelete, "backend detected a delete operation")
		return assessment
	}
	if redirect, ok := obviousBashOutputRedirect(commandLower); ok && assessment.Category == BashEffectRead {
		correctCategory(BashEffectWrite, "backend corrected declared read to effective write after detecting output redirect "+redirect)
	}
	if obviousBashMutation(commandLower) && assessment.Category == BashEffectRead {
		return invalid("read category contradicts a mutating command")
	}
	if obviousCriticalBash(commandLower) {
		if !assessment.Critical {
			assessment.Critical = true
			assessment.Promoted = true
		}
		if assessment.Reason == "" {
			assessment.Reason = "backend detected a sensitive, privileged, expensive, or outbound operation"
		}
	}
	return assessment
}

var obviousBashDeleteCommand = regexp.MustCompile(`(?:^|[;&|]\s*)(?:sudo\s+)?(?:[^\s;&|]+/)?(?:rm|rmdir|unlink|shred)(?:\s|$)`)

func obviousBashDelete(command string) bool {
	if obviousBashDeleteCommand.MatchString(command) {
		return true
	}
	for _, marker := range []string{" -delete", "git clean ", "truncate -s 0 "} {
		if strings.Contains(command, marker) || strings.HasPrefix(command, strings.TrimSpace(marker)+" ") {
			return true
		}
	}
	return false
}

func obviousBashMutation(command string) bool {
	if _, ok := obviousBashOutputRedirect(command); ok {
		return true
	}
	for _, marker := range []string{"tee ", "touch ", "mkdir ", "mv ", "cp ", "install ", "chmod ", "chown ", "sed -i", "git add ", "git commit ", "git checkout ", "git switch ", "git merge ", "git rebase ", "docker run ", "kubectl apply ", "terraform apply"} {
		if strings.HasPrefix(command, marker) || strings.Contains(command, marker) {
			return true
		}
	}
	return obviousMutatingSystemctl(command)
}

func obviousBashOutputRedirect(command string) (string, bool) {
	var quote byte
	escaped := false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch != '>' || (i > 0 && command[i-1] == '<') {
			continue
		}

		start := i
		for start > 0 && command[start-1] >= '0' && command[start-1] <= '9' {
			start--
		}
		if start > 0 && command[start-1] == '&' {
			start--
		}
		end := i + 1
		if end < len(command) && command[end] == '>' {
			end++
		}
		for end < len(command) && (command[end] == ' ' || command[end] == '\t') {
			end++
		}
		targetStart := end
		for end < len(command) && !strings.ContainsRune(" \t\r\n;&|", rune(command[end])) {
			end++
		}
		target := strings.Trim(strings.TrimSpace(command[targetStart:end]), "'\"")
		if target == "" || target == "/dev/null" || strings.HasPrefix(target, "&") {
			continue
		}
		return strings.TrimSpace(command[start:end]), true
	}
	return "", false
}

var mutatingSystemctlCommand = regexp.MustCompile(`(?:^|[;&|]\s*)(?:sudo\s+)?(?:[^\s;&|]+/)?systemctl(?:\s+[^\s;&|]+)*\s+(?:start|stop|reload|restart|try-restart|reload-or-restart|reload-or-try-restart|isolate|kill|clean|freeze|thaw|set-property|bind|mount-image|service-log-level|service-log-target|reset-failed|enable|disable|reenable|preset|preset-all|mask|unmask|link|revert|add-wants|add-requires|edit|set-default|import-environment|unset-environment|daemon-reload|daemon-reexec|cancel|emergency|rescue|halt|poweroff|reboot|kexec|exit|switch-root|suspend|hibernate|hybrid-sleep|suspend-then-hibernate|soft-reboot)(?:\s|$)`)

func obviousMutatingSystemctl(command string) bool {
	return mutatingSystemctlCommand.MatchString(command)
}

const (
	criticalBashSystemConfigMarker = "/etc/"
	criticalBashSystemDataMarker   = "/var/lib/"
)

func obviousCriticalBash(command string) bool {
	if obviousMutatingSystemctl(command) {
		return true
	}
	for _, marker := range []string{
		"sudo ", "su ", criticalBashSystemConfigMarker, criticalBashSystemDataMarker,
		".env", "credentials", "secret", "private_key", "id_rsa",
		"curl ", "wget ", " nc ", "netcat ", "ssh ", "scp ", "rsync ", "--listen", " -l ",
		"pg_dump", "mysqldump", "terraform apply", "kubectl ",
	} {
		if strings.HasPrefix(command, marker) || strings.Contains(command, marker) {
			return true
		}
	}
	return false
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
	case "manage_worktree":
		// Integration is constrained to clean, committed children recorded in the
		// current parent's durable lineage. The worktree service preflights the
		// complete batch and applies it atomically, so this canonical operation is
		// safe to flow without a separate permission round trip.
		return PolicyDecisionAllow
	case "read", "search", "websearch", "webfetch", "agentic_search", "list", "skill_use", "manage_todos", "manage_theme":
		return PolicyDecisionAllow
	case "manage_sessions":
		return PolicyDecisionAllow
	case "session_deploy", "session_commit", "session_archive", "session_unarchive":
		return PolicyDecisionAsk
	case "plan_manage":
		if PlanManageLifecycleRequirement(toolArguments) != "" {
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
