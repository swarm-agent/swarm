package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

func setV3PlanSaveInBatch(batch *pebble.Batch, sessionID string, save V3PlanSaveMutation) error {
	plan := save.Plan
	if strings.TrimSpace(plan.SessionID) != strings.TrimSpace(sessionID) {
		return errors.New("plan save session id does not match mutation session")
	}
	if strings.TrimSpace(plan.ID) == "" {
		return errors.New("plan save plan id is required")
	}
	if plan.Version <= 0 {
		plan.Version = 1
	}
	planPayload, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan %q/%q: %w", plan.SessionID, plan.ID, err)
	}
	if save.ArchivedRevision != nil {
		archived := *save.ArchivedRevision
		if archived.SessionID != plan.SessionID || archived.ID != plan.ID {
			return errors.New("archived plan revision does not match saved plan")
		}
		if archived.Version <= 0 {
			archived.Version = 1
		}
		payload, marshalErr := json.Marshal(archived)
		if marshalErr != nil {
			return fmt.Errorf("marshal plan revision %q/%q/%d: %w", archived.SessionID, archived.ID, archived.Version, marshalErr)
		}
		if err := batch.Set([]byte(KeySessionPlanRevision(archived.SessionID, archived.ID, archived.Version)), payload, nil); err != nil {
			return err
		}
	}
	if err := batch.Set([]byte(KeySessionPlanRevision(plan.SessionID, plan.ID, plan.Version)), planPayload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeySessionPlan(plan.SessionID, plan.ID)), planPayload, nil); err != nil {
		return err
	}
	if plan.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionPlanByAccount(plan.AccountScopeID, plan.SessionID, plan.ID)), []byte(plan.ID), nil); err != nil {
			return err
		}
	}
	if !save.Activate {
		return nil
	}
	active := SessionPlanActive{SessionID: plan.SessionID, PlanID: plan.ID, UserID: plan.UserID, AccountScopeID: plan.AccountScopeID, UpdatedAt: plan.UpdatedAt}
	activePayload, err := json.Marshal(active)
	if err != nil {
		return fmt.Errorf("marshal active plan %q: %w", plan.SessionID, err)
	}
	if err := batch.Set([]byte(KeySessionPlanActive(plan.SessionID)), activePayload, nil); err != nil {
		return err
	}
	if active.AccountScopeID != "" {
		if err := batch.Set([]byte(KeySessionPlanActiveByAccount(active.AccountScopeID, active.SessionID)), activePayload, nil); err != nil {
			return err
		}
	}
	return nil
}
