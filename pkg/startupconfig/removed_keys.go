package startupconfig

// RemovedKey records a retired file setting and the revision that removed it.
// Keep entries permanently so upgrades can read configurations from old installs.
type RemovedKey struct {
	Name      string
	RemovedIn string
}

// RemovedKeys is the shared authority for parsing and installer migration.
// Return a fresh slice so callers cannot change parser policy.
func RemovedKeys() []RemovedKey {
	return []RemovedKey{{Name: "sandbox_cpu_slots", RemovedIn: "a3f19d808"}}
}

func removedKeyRevision(key string) string {
	for _, removed := range RemovedKeys() {
		if removed.Name == key {
			return removed.RemovedIn
		}
	}
	return ""
}
