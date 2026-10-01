package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

type AutomationV2GlobalWebhook struct {
	ID          string   `json:"id"`
	AccountID   string   `json:"account_id"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	WorkerID    string   `json:"worker_id,omitempty"`
	URL         string   `json:"url"`
	Secret      string   `json:"secret,omitempty"`
	Format      string   `json:"format,omitempty"` // "generic" | "slack" | "discord" | "telegram"
	Events      []string `json:"events,omitempty"` // ["*"] or ["started", "succeeded", "failed", "retry_exhausted"]
	Enabled     bool     `json:"enabled"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

func (w *AutomationV2GlobalWebhook) Validate() error {
	cleanURL := strings.TrimSpace(w.URL)
	if cleanURL == "" {
		return errors.New("webhook url is required")
	}
	if !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		return errors.New("webhook url must begin with http:// or https://")
	}
	w.URL = cleanURL
	if w.Format == "" {
		w.Format = "generic"
	}
	if w.Format != "generic" && w.Format != "slack" && w.Format != "discord" && w.Format != "telegram" {
		return errors.New("unsupported webhook format (allowed: generic, slack, discord, telegram)")
	}
	return nil
}

func (s *SessionStore) PutAutomationV2Webhook(accountScopeID string, whk *AutomationV2GlobalWebhook) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	if whk == nil {
		return errors.New("webhook definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	whk.AccountID = accountScopeID
	if err := whk.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if whk.ID == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		whk.ID = "whk_" + hex.EncodeToString(b)
	}
	if whk.CreatedAt <= 0 {
		whk.CreatedAt = now
	}
	whk.UpdatedAt = now

	key := KeyAutomationV2Webhook(accountScopeID, whk.ID)
	val, err := json.Marshal(whk)
	if err != nil {
		return err
	}
	return s.store.db.Set([]byte(key), val, nil)
}

func (s *SessionStore) GetAutomationV2Webhook(accountScopeID, id string) (AutomationV2GlobalWebhook, bool, error) {
	var out AutomationV2GlobalWebhook
	if s == nil || s.store == nil || s.store.db == nil {
		return out, false, errors.New("database not available")
	}
	key := KeyAutomationV2Webhook(accountScopeID, id)
	val, closer, err := s.store.db.Get([]byte(key))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return out, false, nil
		}
		return out, false, err
	}
	defer closer.Close()
	if err := json.Unmarshal(val, &out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (s *SessionStore) ListAutomationV2Webhooks(accountScopeID string) ([]AutomationV2GlobalWebhook, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	prefix := AutomationV2WebhookPrefix(accountScopeID)
	var records []AutomationV2GlobalWebhook
	err := iteratePrefixFromReader(s.store.db, prefix, 0, func(key string, value []byte) error {
		var rec AutomationV2GlobalWebhook
		if err := json.Unmarshal(value, &rec); err != nil {
			return err
		}
		records = append(records, rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (s *SessionStore) DeleteAutomationV2Webhook(accountScopeID, id string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	key := KeyAutomationV2Webhook(accountScopeID, id)
	return s.store.db.Delete([]byte(key), nil)
}
