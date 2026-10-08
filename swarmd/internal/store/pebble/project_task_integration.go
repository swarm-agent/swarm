package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// ProjectTaskIntegrationStore keeps both UI and tool promotions on the existing
// atomic task/realtime-outbox mutation boundary.
type ProjectTaskIntegrationStore interface {
	UpdateProjectTask(string, string, string, func(*ProjectTaskRecord) error) (*ProjectTaskRecord, error)
}

func BeginProjectTaskIntegration(db ProjectTaskIntegrationStore, account string, task *ProjectTaskRecord, receipt *ProjectTaskIntegration) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	receipt.OperationID = hex.EncodeToString(id[:])
	receipt.AttemptID = task.ActiveAttemptID
	receipt.State = "in_progress"
	receipt.Error = ""
	receipt.ResultingTargetHead = ""
	_, err := db.UpdateProjectTask(account, task.ProjectID, task.ID, func(t *ProjectTaskRecord) error {
		if t.SessionID != receipt.SessionID || t.ActiveAttemptID != receipt.AttemptID || t.Revision != task.Revision || t.Archived {
			return errors.New("task attempt changed before integration")
		}
		if t.Integration != nil && t.Integration.State == "in_progress" {
			return errors.New("task integration is already in progress; inspect its operation before retrying")
		}
		copy := *receipt
		t.Integration = &copy
		t.Revision++
		return nil
	})
	return err
}

// CheckProjectTaskIntegration prevents a late operation from overwriting a
// reopened/repaired attempt, including same-session attempts.
func CheckProjectTaskIntegration(t *ProjectTaskRecord, receipt *ProjectTaskIntegration) error {
	if t.SessionID != receipt.SessionID || t.ActiveAttemptID != receipt.AttemptID || t.Integration == nil || t.Integration.OperationID != receipt.OperationID || t.Integration.State != "in_progress" || t.Archived {
		return errors.New("task integration operation or attempt changed")
	}
	return nil
}

func FinishProjectTaskIntegration(db ProjectTaskIntegrationStore, account string, task *ProjectTaskRecord, receipt *ProjectTaskIntegration) (*ProjectTaskRecord, error) {
	return FinishProjectTaskIntegrationGuarded(db, account, task, receipt, 0, nil)
}

// The revision check and external promotion execute under the same task mutation
// guard, so a concurrent reopen/update cannot pass between them. Git's own
// expected-head lock remains the target authority.
func FinishProjectTaskIntegrationGuarded(db ProjectTaskIntegrationStore, account string, task *ProjectTaskRecord, receipt *ProjectTaskIntegration, revision int, apply func() error) (*ProjectTaskRecord, error) {
	return db.UpdateProjectTask(account, task.ProjectID, task.ID, func(t *ProjectTaskRecord) error {
		if revision > 0 && t.Revision != revision {
			return errors.New("task revision changed during recovery; refresh and retry")
		}
		if err := CheckProjectTaskIntegration(t, receipt); err != nil {
			return err
		}
		if apply != nil {
			if err := apply(); err != nil {
				return err
			}
		}
		copy := *receipt
		t.Integration = &copy
		t.Revision++
		switch receipt.State {
		case "integrated", "already_integrated":
			t.IsIntegrated = true
			t.Status = "completed"
			t.UnintegratedCommits = 0
			t.GitStatus = "clean"
			t.ActionNeeded = ""
			t.LastError = ""
			t.WhatDidDo = append(t.WhatDidDo, "Integrated commits into "+receipt.TargetBranch+" (HEAD: "+receipt.ResultingTargetHead+")")
		case "recovered", "equivalent":
			// Delivery of a net delta is not original-source integration.
			t.IsIntegrated = false
			t.Status = "completed"
			t.UnintegratedCommits = 0
			t.GitStatus = "clean"
			t.ActionNeeded, t.LastError = "", ""
			t.WhatDidDo = append(t.WhatDidDo, "Verified task delta delivery ("+receipt.State+") to "+receipt.TargetBranch+" at "+receipt.ResultingTargetHead)
		case "failed", "conflict":
			t.IsIntegrated = false
			t.ActionNeeded = receipt.Error
		default:
			return errors.New("integration requires a terminal receipt")
		}
		return nil
	})
}
