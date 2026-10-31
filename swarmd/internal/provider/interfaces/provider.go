package provideriface

import (
	"context"
	"time"
)

type Status struct {
	ID              string          `json:"id"`
	Ready           bool            `json:"ready"`
	Runnable        bool            `json:"runnable"`
	RunsAgents      bool            `json:"runs_agents"`
	Reason          string          `json:"reason,omitempty"`
	RunReason       string          `json:"run_reason,omitempty"`
	DefaultModel    string          `json:"default_model,omitempty"`
	DefaultThinking string          `json:"default_thinking,omitempty"`
	AuthMethods     []AuthMethod    `json:"auth_methods,omitempty"`
	Accounts        []AccountStatus `json:"accounts,omitempty"`
}

// AccountStatus exposes identity and observed routing state, never credentials.
type AccountStatus struct {
	ID               string     `json:"id"`
	Order            int        `json:"order"`
	Ready            bool       `json:"ready"`
	Email            string     `json:"account_email,omitempty"`
	Organization     string     `json:"organization_name,omitempty"`
	OrganizationUUID string     `json:"organization_uuid,omitempty"`
	Plan             string     `json:"plan,omitempty"`
	LimitState       string     `json:"limit_state"`
	LimitUntil       *time.Time `json:"limit_until,omitempty"`
	Reason           string     `json:"reason,omitempty"`
}

type AuthMethod struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	CredentialType string `json:"credential_type,omitempty"`
	Description    string `json:"description,omitempty"`
}

type AuthCredential struct {
	ID           string
	Provider     string
	Type         string
	Label        string
	Tags         []string
	APIKey       string
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    int64
	// GoogleProject and GoogleLocation name the Vertex AI project and location
	// of an adc credential (not secret).
	GoogleProject  string
	GoogleLocation string
}

type AuthVerification struct {
	Connected bool   `json:"connected"`
	Method    string `json:"method,omitempty"`
	Message   string `json:"message,omitempty"`
}

type Adapter interface {
	ID() string
	Status(context.Context) (Status, error)
}

type AuthVerifier interface {
	VerifyCredential(context.Context, AuthCredential) (AuthVerification, error)
}
