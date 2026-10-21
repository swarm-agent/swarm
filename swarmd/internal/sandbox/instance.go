package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

const labelInstance = "swarm.instance"

func instanceIdentity(state string) string {
	if state == "" {
		return ""
	}
	absolute, err := filepath.Abs(state)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	sum := sha256.Sum256([]byte(absolute))
	return hex.EncodeToString(sum[:])
}

type instanceInspection struct {
	Config struct{ Labels map[string]string }
	Mounts []struct{ Source, Destination string }
}

// Legacy containers have no identity label. The daemon-owned resolv.conf bind
// is positive ownership evidence; project roots alone are not (they can overlap).
func ownsInstance(state string, item instanceInspection) bool {
	identity := instanceIdentity(state)
	if identity == "" {
		return false
	}
	if label := item.Config.Labels[labelInstance]; label != "" {
		return label == identity
	}
	for _, mount := range item.Mounts {
		if mount.Destination == "/etc/resolv.conf" && instanceIdentity(filepath.Dir(mount.Source)) == identity && filepath.Base(mount.Source) == "resolv.conf" {
			return true
		}
	}
	return false
}
