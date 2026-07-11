package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	providerdiagnostics "swarm/packages/swarmd/internal/provider/diagnostics"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

func (s *Service) contextWithProviderAPIDiagnosticRecorder(ctx context.Context, sessionID, runID string, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || applySessionMutation == nil || strings.TrimSpace(sessionID) == "" || !providerdiagnostics.Enabled() {
		return ctx
	}
	return providerdiagnostics.ContextWithRecorder(ctx, func(_ context.Context, event providerdiagnostics.Event) {
		s.recordProviderAPIDiagnostic(sessionID, runID, principal, applySessionMutation, event)
	})
}

func (s *Service) recordProviderAPIDiagnostic(sessionID, runID string, principal identity.Principal, applySessionMutation func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error), event providerdiagnostics.Event) {
	if s == nil || applySessionMutation == nil || !providerdiagnostics.Enabled() {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if (strings.TrimSpace(principal.UserID) == "" || strings.TrimSpace(principal.AccountScopeID) == "") && s.sessions != nil {
		if session, ok, err := s.sessions.GetSession(sessionID); err == nil && ok {
			if strings.TrimSpace(principal.UserID) == "" {
				principal.UserID = strings.TrimSpace(session.UserID)
			}
			if strings.TrimSpace(principal.AccountScopeID) == "" {
				principal.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
			}
		}
	}
	stage := "session.diagnostic.provider.api"
	if eventStage := strings.TrimSpace(event.Stage); eventStage != "" {
		stage += "." + strings.NewReplacer(" ", "_", "/", "_", ":", "_", "-", "_").Replace(eventStage)
	}
	now := event.RecordedAt
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	sequence := fmt.Sprintf("provider-api-%s-%s-%s-%d-%d", strings.TrimSpace(event.Provider), strings.TrimSpace(event.Operation), strings.TrimSpace(event.Stage), now, time.Now().UnixNano())
	envelope := map[string]any{
		"diagnostic":     true,
		"session_id":     sessionID,
		"run_id":         strings.TrimSpace(runID),
		"stage":          stage,
		"source":         "backend.provider.api",
		"recorded_at":    now,
		"sequence_label": sequence,
		"payload":        event,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		log.Printf("warning: failed to marshal provider api diagnostic session=%q run=%q provider=%q stage=%q: %v", sessionID, runID, event.Provider, event.Stage, err)
		return
	}
	sum := sha256.Sum256(raw)
	payloadHash := hex.EncodeToString(sum[:])
	clientRequestID := runProviderAPIDiagnosticClientRequestID(stage, runID, sequence, payloadHash)
	if _, err := applySessionMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordDiagnostic,
		EventType:       stage,
		EventPayload:    raw,
		NowUnixMs:       now,
	}); err != nil {
		log.Printf("warning: failed to record provider api diagnostic session=%q run=%q provider=%q stage=%q: %v", sessionID, runID, event.Provider, event.Stage, err)
	}
}

func runProviderAPIDiagnosticClientRequestID(stage, runID, sequenceLabel, payloadHash string) string {
	replacer := strings.NewReplacer(".", "_", "/", "_", " ", "_", ":", "_")
	label := replacer.Replace(strings.TrimSpace(stage))
	if label == "" {
		label = "session_diagnostic_provider_api"
	}
	runID = replacer.Replace(strings.TrimSpace(runID))
	sequenceLabel = replacer.Replace(strings.TrimSpace(sequenceLabel))
	parts := []string{"v3-diagnostic", label}
	if runID != "" {
		parts = append(parts, runID)
	}
	if sequenceLabel != "" {
		parts = append(parts, sequenceLabel)
	}
	if len(payloadHash) > 16 {
		payloadHash = payloadHash[:16]
	}
	if payloadHash != "" {
		parts = append(parts, payloadHash)
	}
	return strings.Join(parts, "-")
}
