package api

// RunPhase names canonical V3 run lifecycle phases emitted as durable session events.
type RunPhase string

const (
	RunPhaseProviderRequestStarted RunPhase = "provider_request_started"
	RunPhaseProviderFirstEvent     RunPhase = "provider_first_event"
	RunPhaseOutputStreaming        RunPhase = "output_streaming"
)
