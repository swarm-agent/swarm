package pebblestore

import (
 "strings"
 "github.com/cockroachdb/pebble"
)

// DesignWake is a coalesced hint only. Durable discovery repairs missed hints.
func (s *Store) DesignWake() <-chan struct{} {
 s.designWakeMu.Lock()
 defer s.designWakeMu.Unlock()
 if s.designWake == nil { s.designWake = make(chan struct{}, 1) }
 return s.designWake
}
func (s *Store) WakeDesign() {
 s.DesignWake()
 s.designWakeMu.Lock()
 defer s.designWakeMu.Unlock()
 select { case s.designWake <- struct{}{}: default: }
}

// ScanDesignPending is daemon-only discovery across owners. Cursor bounds work
// to 256 keys per page; historical content is never decoded during discovery.
func (s *Store) ScanDesignPending(after string) ([]DesignRequest, string, error) {
 const prefix = "design:v1/"
 if after != "" && !strings.HasPrefix(after, prefix) { return nil, "", ErrDesignInvalid }
 it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix+"\xff")})
 if err != nil { return nil, "", err }
 defer it.Close()
 rows := []DesignRequest{}
 cursor := ""
 start := prefix
 if after != "" { start = after+"\x00" }
 n := 0
 for ok := it.SeekGE([]byte(start)); ok; ok = it.Next() {
  if n == 256 { return rows, cursor, it.Error() }
  n++
  cursor = string(it.Key())
  parts := strings.Split(strings.TrimPrefix(cursor,prefix), "/")
  if len(parts) != 4 || parts[2] != "pending" { continue }
  r, err := s.GetDesignRequest(DesignPrincipal{AccountID:parts[0], PrincipalID:parts[1]}, parts[3])
  if err != nil { return nil, "", err }
  rows = append(rows,r)
 }
 return rows, "", it.Error()
}
