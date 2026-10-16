package startupconfig

// ResolvedLandingParallel returns the per-folder candidate limit. The stored
// zero value preserves provider-sensitive defaults rather than fixing them at
// configuration migration time. This is independent of box-wide admission.
func (cfg FileConfig) ResolvedLandingParallel() int {
	if cfg.LandingParallel > 0 {
		return cfg.LandingParallel
	}
	switch cfg.EnvironmentProvider {
	case "gcp-cloudbuild", "runners":
		return 8
	case "auto":
		if cfg.CloudBuildProject != "" && cfg.CloudBuildBucket != "" {
			return 8
		}
	}
	return 1
}
