package sandbox

import "fmt"

// LeaseStatus describes the command backend, independently of machine mode.
func (m *Manager) LeaseStatus(root string) map[string]any {
	if m == nil {
		return map[string]any{"available": false}
	}
	uid, gid := m.sandboxUser(root)
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime == "" {
		runtime = "engine-default"
	}
	return map[string]any{
		"available": m.Status().Active,
		"engine":    m.cfg.Engine, "runtime": runtime,
		"owner_user": fmt.Sprintf("%d:%d", uid, gid),
		"lease_user": fmt.Sprintf("%d:%d", m.cfg.UID, m.cfg.GID),
	}
}
