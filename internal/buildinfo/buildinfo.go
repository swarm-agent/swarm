package buildinfo

import "strings"

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = ""
)

func DisplayVersion() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		return "dev"
	}
	return version
}

func DisplayCommit() string {
	commit := strings.TrimSpace(Commit)
	if commit == "" {
		return "unknown"
	}
	return commit
}

func DisplayBuiltAt() string {
	return strings.TrimSpace(BuiltAt)
}
