package pebblestore

import (
	"strings"
	"testing"
)

func TestValidateV3SessionMutationInputRequiresOwnership(t *testing.T) {
	base := V3SessionMutationInput{
		SessionID:       "session-1",
		UserID:          "user-1",
		AccountScopeID:  "account-1",
		ClientRequestID: "request-1",
		IdempotencyKey:  "request-1",
		PayloadHash:     "hash-1",
		Kind:            V3SessionMutationAppendMessage,
	}

	for _, test := range []struct {
		name string
		edit func(*V3SessionMutationInput)
		want string
	}{
		{name: "missing user", edit: func(input *V3SessionMutationInput) { input.UserID = "" }, want: "user id is required"},
		{name: "missing account", edit: func(input *V3SessionMutationInput) { input.AccountScopeID = "" }, want: "account scope id is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if err := validateV3SessionMutationInput(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateV3SessionMutationInputRejectsEmbeddedOwnershipMismatch(t *testing.T) {
	base := V3SessionMutationInput{
		SessionID:       "session-1",
		UserID:          "user-1",
		AccountScopeID:  "account-1",
		ClientRequestID: "request-1",
		IdempotencyKey:  "request-1",
		PayloadHash:     "hash-1",
		Kind:            V3SessionMutationAppendMessage,
	}

	for _, test := range []struct {
		name string
		edit func(*V3SessionMutationInput)
		want string
	}{
		{name: "session user", edit: func(input *V3SessionMutationInput) {
			input.Session = &SessionSnapshot{ID: input.SessionID, UserID: "other-user", AccountScopeID: input.AccountScopeID}
		}, want: "session user id does not match mutation ownership"},
		{name: "message account", edit: func(input *V3SessionMutationInput) {
			input.Message = &MessageSnapshot{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: "other-account"}
		}, want: "message account scope id does not match mutation ownership"},
		{name: "lifecycle user", edit: func(input *V3SessionMutationInput) {
			input.Lifecycle = &SessionLifecycleSnapshot{SessionID: input.SessionID, UserID: "other-user", AccountScopeID: input.AccountScopeID}
		}, want: "lifecycle user id does not match mutation ownership"},
		{name: "run intent account", edit: func(input *V3SessionMutationInput) {
			input.RunIntent = &V3SessionRunIntent{SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: "other-account"}
		}, want: "run intent account scope id does not match mutation ownership"},
		{name: "plan save user", edit: func(input *V3SessionMutationInput) {
			input.Kind = V3SessionMutationSavePlan
			input.PlanSave = &V3PlanSaveMutation{Plan: SessionPlanSnapshot{ID: "plan-1", SessionID: input.SessionID, UserID: "other-user", AccountScopeID: input.AccountScopeID}}
		}, want: "plan save user id does not match mutation ownership"},
		{name: "plan save archived account", edit: func(input *V3SessionMutationInput) {
			input.Kind = V3SessionMutationSavePlan
			input.PlanSave = &V3PlanSaveMutation{Plan: SessionPlanSnapshot{ID: "plan-1", SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: input.AccountScopeID}, ArchivedRevision: &SessionPlanSnapshot{ID: "plan-1", SessionID: input.SessionID, UserID: input.UserID, AccountScopeID: "other-account"}}
		}, want: "plan save archived revision account scope id does not match mutation ownership"},
		{name: "plan user", edit: func(input *V3SessionMutationInput) {
			input.Kind = V3SessionMutationAcceptPlan
			input.PlanAcceptance = validOwnershipTestPlanAcceptance(input.SessionID, "other-user", input.AccountScopeID)
		}, want: "plan user id does not match mutation ownership"},
		{name: "accepted session account", edit: func(input *V3SessionMutationInput) {
			input.Kind = V3SessionMutationAcceptPlan
			input.PlanAcceptance = validOwnershipTestPlanAcceptance(input.SessionID, input.UserID, input.AccountScopeID)
			input.PlanAcceptance.Session.AccountScopeID = "other-account"
		}, want: "plan acceptance session account scope id does not match mutation ownership"},
		{name: "mode message user", edit: func(input *V3SessionMutationInput) {
			input.Kind = V3SessionMutationAcceptPlan
			input.PlanAcceptance = validOwnershipTestPlanAcceptance(input.SessionID, input.UserID, input.AccountScopeID)
			input.PlanAcceptance.ModeMessage = &MessageSnapshot{SessionID: input.SessionID, UserID: "other-user", AccountScopeID: input.AccountScopeID}
		}, want: "plan acceptance mode message user id does not match mutation ownership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if err := validateV3SessionMutationInput(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func validOwnershipTestPlanAcceptance(sessionID, userID, accountScopeID string) *V3PlanAcceptanceMutation {
	return &V3PlanAcceptanceMutation{
		Plan:             SessionPlanSnapshot{ID: "plan-1", SessionID: sessionID, UserID: userID, AccountScopeID: accountScopeID},
		Session:          SessionSnapshot{ID: sessionID, UserID: userID, AccountScopeID: accountScopeID},
		PlanEventPayload: []byte(`{}`),
		ModeEventPayload: []byte(`{}`),
	}
}
