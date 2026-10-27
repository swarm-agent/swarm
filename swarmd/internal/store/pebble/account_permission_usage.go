package pebblestore

import "errors"

// RecordPermissionUse serializes usage with rule edits; usage advances revision
// so an older PATCH cannot overwrite a newer last_used timestamp.
func (s *Store) RecordPermissionUse(account, kind, target string, at int64) error {
	unlock := s.sessionMutations.lockSessions("account_permissions:" + account)
	defer unlock()
	p, err := s.GetAccountPermissions(account)
	if err != nil {
		return err
	}
	switch kind {
	case "files", "browser":
		r := p.Device(target)
		if at > r.LastUsed {
			r.LastUsed = at
		}
		p.Devices[target] = r
	case "linked":
		r := p.Link(target)
		if at > r.LastUsed {
			r.LastUsed = at
		}
		p.Linked[target] = r
	case "tasks":
		if at > p.TasksLastUsed {
			p.TasksLastUsed = at
		}
	default:
		return errors.New("unknown permission usage rule")
	}
	if p.Revision == ^uint64(0) {
		return ErrAccountPermissionsConflict
	}
	p.Revision++
	return s.PutJSON("account_permissions/"+keyPart(account), p)
}
func (s *PermissionStore) RecordPermissionUse(account, kind, target string, at int64) error {
	return s.store.RecordPermissionUse(account, kind, target, at)
}
func (s *SessionStore) RecordPermissionUse(account, kind, target string, at int64) error {
	return s.store.RecordPermissionUse(account, kind, target, at)
}
