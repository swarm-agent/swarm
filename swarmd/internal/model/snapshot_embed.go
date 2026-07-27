package model

import _ "embed"

// pinnedSwarmSnapshotJSON is generated from the Swarm model snapshot that ships
// with this PR so catalog resolution works before any network refresh succeeds.
//
//go:embed snapshotdata/snapshot.json
var pinnedSwarmSnapshotJSON []byte

//go:embed snapshotdata/snapshot-version.json
var pinnedSwarmSnapshotVersionJSON []byte
