package provideriface

import "context"

type Status struct {
	ID              string       `json:"id"`
	Ready           bool         `json:"ready"`
	Runnable        bool         `json:"runnable"`
	Reason          string       `json:"reason,omitempty"`
	RunReason       string       `json:"run_reason,omitempty"`
	DefaultModel    string       `json:"default_model,omitempty"`
	DefaultThinking string       `json:"default_thinking,omitempty"`
	AuthMethods     []AuthMethod `json:"auth_methods,omitempty"`
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
