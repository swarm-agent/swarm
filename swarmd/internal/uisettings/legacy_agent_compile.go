package uisettings

// CompactAgentSettings and AgentSettings are temporary compile-only shapes for
// legacy runtime consumers that are moved to agentmodelsettings in checkpoint 2.
// They are deliberately absent from the UI service/store wire and persistence
// schema; UISettings.Agents is ignored by JSON and SetForAccount.
type CompactAgentSettings struct {
	Provider    string
	Model       string
	Thinking    string
	ServiceTier string
}

type AgentSettings struct {
	Compact  CompactAgentSettings
	Finder   CompactAgentSettings
	Coder    CompactAgentSettings
	Designer CompactAgentSettings
	Router   CompactAgentSettings
}
