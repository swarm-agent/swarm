package uisettings

// CompactAgentSettings and AgentSettings are temporary compile-only shapes for
// API-local projection fixtures that still consume UISettings.Agents. They are
// absent from the UI service/store wire and persistence schema.
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
