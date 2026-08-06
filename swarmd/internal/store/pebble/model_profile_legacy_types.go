package pebblestore

// Removed profile-bundle mode constants remain only for legacy migration and
// consumers that have not yet moved to the session Action/optional Plan shape.
const (
	ModelProfileModeSingle = "single"
	ModelProfileModeSplit  = "split"
)

// ModelProfileSelection is a resolved immutable session selection. Canonical
// favorites remain flat ModelProfileRecord values; snapshots copy these fields.
type ModelProfileSelection struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}
