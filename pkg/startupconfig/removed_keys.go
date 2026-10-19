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
	keys := []RemovedKey{{Name: "sandbox_cpu_slots", RemovedIn: "a3f19d808"}}
	// Landing and its runner and Cloud Build environments.
	for _, name := range []string{
		"environment_provider", "environment_runner_wait_seconds", "environment_runner_wake_command",
		"landing_parallel", "landing_max_concurrent",
		"cloudbuild_project", "cloudbuild_region", "cloudbuild_bucket", "cloudbuild_machine_type",
		"cloudbuild_timeout_minutes", "cloudbuild_service_account", "cloudbuild_cache",
	} {
		keys = append(keys, RemovedKey{Name: name, RemovedIn: "the landing removal"})
	}
	return keys
}

func removedKeyRevision(key string) string {
	for _, removed := range RemovedKeys() {
		if removed.Name == key {
			return removed.RemovedIn
		}
	}
	return ""
}
