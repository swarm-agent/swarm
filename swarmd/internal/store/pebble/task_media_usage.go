package pebblestore

import (
	"encoding/json"
	"errors"
	"github.com/cockroachdb/pebble"
	"math"
	"strings"
	"time"
)

// putTaskMediaUsage records principal-owned media tasks that have no conversation.
// The receipt and account counters commit together under the account mutation lock.
func (s *SessionStore) putTaskMediaUsage(rec SessionMediaUsageRecord) error {
	if strings.TrimSpace(rec.AccountScopeID) == "" || rec.ProjectID == "" || rec.TaskID == "" || rec.UserID == "" {
		return errors.New("media usage requires a session or owned project task and principal")
	}
	if rec.CostUSD < 0 || math.IsNaN(rec.CostUSD) || math.IsInf(rec.CostUSD, 0) {
		return errors.New("invalid media cost")
	}
	unlock := s.store.sessionMutations.lockSessions("account:" + rec.AccountScopeID)
	committed := false
	defer func() {
		unlock()
		if committed {
			s.store.signalScheduling(SchedulingSignal{Kind: SchedulingSignalBudget, AccountScopeID: rec.AccountScopeID})
			s.publishWorkerBudgetHolds(rec.AccountScopeID)
		}
	}()
	task, found, err := s.GetProjectTask(rec.AccountScopeID, rec.ProjectID, rec.TaskID)
	if err != nil {
		return err
	}
	if !found || task.AccountID != rec.AccountScopeID {
		return errors.New("media usage task ownership mismatch")
	}
	key := KeySessionMediaUsage(rec.AccountScopeID, rec.ID)
	var existing SessionMediaUsageRecord
	if found, err := s.store.GetJSON(key, &existing); err != nil {
		return err
	} else if found {
		return nil
	}
	if rec.CreatedAt <= 0 {
		rec.CreatedAt = time.Now().UnixMilli()
	}
	date := time.UnixMilli(rec.CreatedAt).UTC().Format("2006-01-02")
	acc, _, err := s.GetDailyUsageAccumulator(rec.AccountScopeID, date)
	if err != nil {
		return err
	}
	acc.AccountScopeID = rec.AccountScopeID
	acc.Date = date
	acc.UpdatedAt = rec.CreatedAt
	acc.TotalCostUSD += rec.CostUSD
	acc.MediaCostUSD += rec.CostUSD
	acc.MediaCalls++
	acc.UnknownReceipts += unknownBudgetReceipt(rec.PriceStatus)
	image, video, audio := 0, 0, 0
	switch rec.Kind {
	case "image":
		image = 1
		acc.ImageCount++
		acc.ImageCostUSD += rec.CostUSD
	case "video":
		video = 1
		acc.VideoCount++
		acc.VideoCostUSD += rec.CostUSD
	case "audio":
		audio = 1
		acc.AudioCount++
		acc.AudioCostUSD += rec.CostUSD
	default:
		return errors.New("invalid media kind")
	}
	batch := s.store.NewBatch()
	defer batch.Close()
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err = batch.Set([]byte(key), payload, nil); err != nil {
		return err
	}
	payload, err = json.Marshal(acc)
	if err != nil {
		return err
	}
	local := *s
	local.accountLockInput = V3SessionMutationInput{accountLockHeld: true, AccountScopeID: rec.AccountScopeID}
	if err = local.setMediaUsageDays(batch, rec); err != nil {
		return err
	}
	if err = local.setAccountWorkerBudgetHolds(batch, acc); err != nil {
		return err
	}
	if err = local.setDailyUsageAccumulatorPayload(batch, acc, payload); err != nil {
		return err
	}
	if err = local.updateAccountUsageRollupInBatch(batch, rec.AccountScopeID, date, "", rec.Provider, rec.Model, 0, 0, rec.CostUSD, 0, 0, 0, 0, 0, 0, 1, image, video, audio, int(unknownBudgetReceipt(rec.PriceStatus)), rec.CreatedAt, rec.CreatedAt); err != nil {
		return err
	}
	if err = batch.Commit(pebble.Sync); err != nil {
		return err
	}
	committed = true
	return nil
}
