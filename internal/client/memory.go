package client

import "context"

// MemoryEntry retains opaque metadata when editing one object's content.
type MemoryEntry map[string]any

type MemoryDocument struct {
	Revision int64         `json:"revision"`
	Entries  []MemoryEntry `json:"entries"`
}

func (c *API) GetMemory(ctx context.Context) (MemoryDocument, error) {
	var doc MemoryDocument
	err := c.getJSON(ctx, "/v1/memory", &doc, true)
	return doc, err
}

func (c *API) MutateMemory(ctx context.Context, revision int64, action, reason, id string, entry MemoryEntry) error {
	var result MemoryDocument
	return c.postJSON(ctx, "/v1/memory", map[string]any{
		"action": action, "expected_revision": revision, "reason": reason,
		"entry_id": id, "entry": entry,
	}, &result, true)
}
