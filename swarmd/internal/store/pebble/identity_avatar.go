package pebblestore

// Avatar bytes are separate from user records so profile uploads cannot overwrite
// concurrent username changes or inflate identity/session DTOs.
func (s *IdentityStore) GetUserAvatar(accountID, userID string) ([]byte, bool, error) {
	if err := s.configured(); err != nil {
		return nil, false, err
	}
	return s.store.GetBytes(KeyIdentityUserAvatar(accountID, userID))
}

func (s *IdentityStore) PutUserAvatar(accountID, userID string, data []byte) error {
	if err := s.configured(); err != nil {
		return err
	}
	return s.store.PutBytes(KeyIdentityUserAvatar(accountID, userID), data)
}
