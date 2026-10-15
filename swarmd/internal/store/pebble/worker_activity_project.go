package pebblestore

import "strings"

// Legacy runs predate ProjectID. Recover only from owned durable associations,
// never run input. Do not backfill on read or override a pinned admission result.
func (s *Store) workerActivityProject(run WorkerRunRecord) (string, error) {
	if run.ProjectID != "" {
		return run.ProjectID, nil
	}
	sessions := NewSessionStore(s)
	project := ""
	if run.SessionID != "" {
		session, found, err := sessions.GetSession(run.SessionID)
		if err != nil {
			return "", err
		}
		if found {
			if session.AccountScopeID != run.AccountScopeID || session.UserID != run.UserID {
				return "", nil
			}
			project, _ = session.Metadata["project_id"].(string)
		}
	}
	if strings.TrimSpace(project) == "" {
		worker, found, err := NewWorkerStore(s).GetWorker(run.AccountScopeID, run.WorkerID)
		if err != nil {
			return "", err
		}
		if !found || worker.AccountScopeID != run.AccountScopeID {
			return "", nil
		}
		project, _ = worker.Metadata["project_id"].(string)
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return "", nil
	}
	record, found, err := sessions.GetProject(run.AccountScopeID, project)
	if err != nil {
		return "", err
	}
	if !found || record.AccountID != run.AccountScopeID {
		return "", nil
	}
	return project, nil
}
