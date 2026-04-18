package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type AttachAuthRecord struct {
	Token     string `json:"token"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type ClientAuthStore struct {
	store *Store
}

func NewClientAuthStore(store *Store) *ClientAuthStore {
	return &ClientAuthStore{store: store}
}

func (s *ClientAuthStore) EnsureAttachToken() (AttachAuthRecord, error) {
	record, ok, err := s.GetAttachAuth()
	if err != nil {
		return AttachAuthRecord{}, err
	}
	if ok && record.Token != "" {
		return record, nil
	}
	return s.RotateAttachToken(0)
}

func (s *ClientAuthStore) RotateAttachToken(createdAt int64) (AttachAuthRecord, error) {
	now := time.Now().UnixMilli()
	if createdAt <= 0 {
		createdAt = now
	}
	token, err := generateToken(32)
	if err != nil {
		return AttachAuthRecord{}, err
	}
	record := AttachAuthRecord{
		Token:     token,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	if err := s.store.PutJSON(KeyAuthAttachDefault, record); err != nil {
		return AttachAuthRecord{}, err
	}
	return record, nil
}

func (s *ClientAuthStore) GetAttachAuth() (AttachAuthRecord, bool, error) {
	var record AttachAuthRecord
	ok, err := s.store.GetJSON(KeyAuthAttachDefault, &record)
	if err != nil {
		return AttachAuthRecord{}, false, err
	}
	if !ok {
		return AttachAuthRecord{}, false, nil
	}
	return record, true, nil
}

func generateToken(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("token size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
