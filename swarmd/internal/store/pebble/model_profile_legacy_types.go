package pebblestore

const (
	ModelProfileModeSingle = "single"
	ModelProfileModeSplit  = "split"
)

// ModelProfileSelection is retained only for the untouched session and swarm
// snapshot contracts. It is not part of canonical flat favorite persistence.
type ModelProfileSelection struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}
