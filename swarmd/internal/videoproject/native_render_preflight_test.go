package videoproject

import (
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type preflightV2Authority struct {
	ref pebblestore.ArtifactV2VideoReference
}

func (a preflightV2Authority) ValidateVideoReference(account, user string, ref pebblestore.ArtifactV2VideoReference) error {
	if account != "account" || user != "user" || ref != a.ref {
		return fmt.Errorf("invalid V2 exact authority")
	}
	return nil
}
