package api

import (
	"encoding/json"
)

type landMemoryStore struct{ data []byte }

func (s *landMemoryStore) GetJSON(_ string, v any) (bool, error) {
	if len(s.data) == 0 {
		return false, nil
	}
	return true, json.Unmarshal(s.data, v)
}
func (s *landMemoryStore) PutJSON(_ string, v any) error {
	var err error
	s.data, err = json.Marshal(v)
	return err
}
