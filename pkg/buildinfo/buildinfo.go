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

func IsDevVersion() bool {
	return IsDevVersionString(DisplayVersion())
}

func IsDevVersionString(value string) bool {
	version := strings.ToLower(strings.TrimSpace(value))
	if version == "" || version == "dev" {
		return true
	}
	return strings.Contains(version, "-dev") || strings.Contains(version, "+dev")
}
