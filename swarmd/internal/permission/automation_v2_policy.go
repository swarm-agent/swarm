package permission

// ExplainAutomationV2Proposal checks authoring restrictions without conflating a
// pending draft with one-shot plan acceptance. Explicit denies retain the full
// argument context and remain effective even when ordinary permissions bypass.
func (s *Service) ExplainAutomationV2Proposal(account, mode, name, arguments string, overlay *Policy) (PolicyExplain, error) {
	state, err := s.CurrentPermissionStateForAccount(account)
	if err != nil {
		return PolicyExplain{}, err
	}
	policy := state.Policy
	if overlay != nil {
		policy.Rules = append(append([]PolicyRule(nil), overlay.Rules...), policy.Rules...)
	}
	policy = NormalizePolicy(policy)
	ctx := buildPolicyEvalContext(name, arguments)
	ctx.ToolName = normalizePolicyToolName(name)
	if x, ok := explainPhraseDeny(ctx, policy); ok {
		return x, nil
	}
	if x, ok := explainExplicitDeny(ctx, policy); ok {
		return x, nil
	}
	if x, ok := explainBuiltinDeny(mode, ctx); ok {
		return x, nil
	}
	return PolicyExplain{Decision: PolicyDecisionAllow, Source: "automation_v2_proposal", Reason: "authoring stores only a pending review; acceptance remains user-only", ToolName: ctx.ToolName}, nil
}
