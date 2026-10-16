package pebblestore

import (
 "io"
 "os"
 "path/filepath"
)

// CloudBuildPayload uses the same bounded archive-v1 writer as runner delivery,
// without publishing a claimable runner landing. The caller owns cleanup.
func (s *Store) CloudBuildPayload(account, id, checkout, head string) (io.ReadCloser, string, func() error, error) {
 l := &RunnerLanding{ID: id, SourceClonePath: checkout, HeadCommit: head}
 cleanup := func() error { return os.RemoveAll(filepath.Dir(s.runnerPayloadPath(account, id))) }
 if err := s.buildRunnerPayload(account, l); err != nil { _ = cleanup(); return nil, "", nil, err }
 f, err := os.Open(s.runnerPayloadPath(account, id))
 if err != nil { _ = cleanup(); return nil, "", nil, err }
 return f, l.ArchiveSHA256, cleanup, nil
}
