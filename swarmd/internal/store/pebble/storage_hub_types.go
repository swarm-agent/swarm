package pebblestore

type StorageBucketRecord struct {
	ID              string `json:"id"`
	AccountScopeID  string `json:"account_scope_id"`
	Name            string `json:"name"`
	Provider        string `json:"provider"` // "s3", "gcs", "mock", "local"
	BucketName      string `json:"bucket_name"`
	Endpoint        string `json:"endpoint,omitempty"`
	Region          string `json:"region,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type StorageDiscoveredWorkerRecord struct {
	WorkerID       string         `json:"worker_id"`
	BucketID       string         `json:"bucket_id"`
	AccountScopeID string         `json:"account_scope_id"`
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Version        string         `json:"version,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	HasBaseContext bool           `json:"has_base_context"`
	BaseContext    map[string]any `json:"base_context,omitempty"`
	SessionsCount  int            `json:"sessions_count"`
	LastSessionID  string         `json:"last_session_id,omitempty"`
	LastStatus     string         `json:"last_status,omitempty"`
	LastStep       string         `json:"last_step,omitempty"`
	LastProgress   int            `json:"last_progress,omitempty"`
	LastActivityAt int64          `json:"last_activity_at"`
	Imported       bool           `json:"imported"`
	ImportedAt     int64          `json:"imported_at,omitempty"`
	DiscoveredAt   int64          `json:"discovered_at"`
	UpdatedAt      int64          `json:"updated_at"`
}

type StorageDeliverableFileRef struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

type StorageDiscoveredDeliverableRecord struct {
	DeliverableID  string                      `json:"deliverable_id"`
	WorkerID       string                      `json:"worker_id"`
	SessionID      string                      `json:"session_id"`
	BucketID       string                      `json:"bucket_id"`
	AccountScopeID string                      `json:"account_scope_id"`
	Title          string                      `json:"title"`
	Summary        string                      `json:"summary,omitempty"`
	Kind           string                      `json:"kind,omitempty"`
	Status         string                      `json:"status"` // "pending_review", "accepted", "rejected"
	SHA256         string                      `json:"sha256,omitempty"`
	Files          []StorageDeliverableFileRef `json:"files,omitempty"`
	Actions        []NotificationAction        `json:"actions,omitempty"`
	Payload        map[string]any              `json:"payload,omitempty"`
	CreatedAt      int64                       `json:"created_at"`
	UpdatedAt      int64                       `json:"updated_at"`
	ImportedAt     int64                       `json:"imported_at,omitempty"`
}
