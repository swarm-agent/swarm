package permission

// ExplainDesignSourceRead evaluates source sharing separately from ordinary
// reads. Default read allowance is not sharing consent; explicit account rules
// and the account's bypass setting are authority. Denies and explicit consent
// restrictions remain effective in bypass (including account-sensitive sources).
// Callers
// must first validate the principal, run and rooted source scope.
func (s *Service) ExplainDesignSourceRead(account, arguments string) (PolicyExplain, error) {
	state, err := s.CurrentPermissionStateForAccount(account)
	if err != nil {
		return PolicyExplain{}, err
	}
	explain := ExplainPolicy("auto", "read", arguments, state.Policy)
	if explain.Decision == PolicyDecisionDeny || (explain.Source == "rule" && explain.Decision == PolicyDecisionAsk) {
		return explain, nil
	}
	if state.BypassPermissions {
		explain.Decision = PolicyDecisionAllow
		explain.Source = "bypass_permissions"
		explain.Reason = "source sharing approval is bypassed"
		return explain, nil
	}
	if explain.Source == "default" {
		explain.Decision = PolicyDecisionAsk
		explain.Reason = "source sharing requires approval"
	}
	return explain, nil
}
