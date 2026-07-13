package pebblestore

import "testing"

func TestNormalizeAgentProfileDefaultSessionMode(t *testing.T) {
	planProfile := NormalizeAgentProfile(AgentProfile{RuntimeMode: AgentRuntimeModePlanAuto})
	if planProfile.DefaultSessionMode != AgentDefaultSessionModePlan {
		t.Fatalf("plan default = %q", planProfile.DefaultSessionMode)
	}

	autoProfile := NormalizeAgentProfile(AgentProfile{RuntimeMode: AgentRuntimeModeReadWrite, DefaultSessionMode: AgentDefaultSessionModeAuto})
	if autoProfile.DefaultSessionMode != AgentDefaultSessionModeAuto {
		t.Fatalf("auto default = %q", autoProfile.DefaultSessionMode)
	}

	planCapableAutoProfile := NormalizeAgentProfile(AgentProfile{RuntimeMode: AgentRuntimeModePlanAuto, DefaultSessionMode: AgentDefaultSessionModeAuto})
	if planCapableAutoProfile.DefaultSessionMode != AgentDefaultSessionModeAuto {
		t.Fatalf("explicit plan-capable auto default = %q", planCapableAutoProfile.DefaultSessionMode)
	}
}
