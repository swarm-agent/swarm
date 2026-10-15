package publish

import (
	"encoding/json"
)

type queueMemory map[string][]byte

func (m queueMemory) GetJSON(k string, v any) (bool, error) {
	b, ok := m[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}
func (m queueMemory) PutJSON(k string, v any) error { b, e := json.Marshal(v); m[k] = b; return e }
