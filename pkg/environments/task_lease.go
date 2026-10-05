package environments

// TaskLeaseBinding fences a borrowed receipt to one durable attachment revision
// and attempt. UserID is authenticated attribution, not a client-supplied claim.
type TaskLeaseBinding struct {
	ProjectID string `json:"project_id"`
	TaskID string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	AttachmentID string `json:"attachment_id"`
	AttachmentRevision int `json:"attachment_revision"`
	UserID string `json:"user_id"`
}
