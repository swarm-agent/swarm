package permission

import (
	"encoding/json"
	"strings"
)

// bashSimpleCommandWords returns the shell words of one simple command, or
// false when the raw command uses any shell syntax beyond plain words and
// balanced quotes: separators, pipes, redirection, substitution, expansion,
// grouping, escapes or line breaks. Built-in allows only apply to such commands.
func bashSimpleCommandWords(arguments string) ([]string, bool) {
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &payload); err != nil {
		return nil, false
	}
	command := strings.TrimSpace(payload.Command)
	if command == "" || strings.ContainsAny(command, ";&|<>`$(){}\\\n\r") {
		return nil, false
	}
	var words []string
	var current strings.Builder
	var quote rune
	inWord := false
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inWord {
		words = append(words, current.String())
	}
	return words, len(words) > 0
}

// bashClearlyReadOnlyCommand reports whether a simple command only observes
// state and is equivalent to an already default-allowed inspection tool
// (git_status/git_diff) or reads no file content (pwd, date). Commands that
// print file contents (cat/head/tail/wc) are deliberately excluded: unlike the
// read tool they follow symlinks outside the session's workspace scope.
func bashClearlyReadOnlyCommand(words []string) bool {
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "pwd":
		for _, word := range words[1:] {
			if word != "-P" && word != "-L" {
				return false
			}
		}
		return true
	case "date":
		return bashReadOnlyDateArgs(words[1:])
	case "git":
		return bashReadOnlyGitArgs(words[1:])
	default:
		return false
	}
}

func bashReadOnlyDateArgs(args []string) bool {
	for i := 0; i < len(args); i++ {
		word := args[i]
		switch {
		case strings.HasPrefix(word, "+"):
			// Output format.
		case word == "-u", word == "--utc", word == "--universal", word == "-R", word == "--rfc-email",
			strings.HasPrefix(word, "-I"), strings.HasPrefix(word, "--iso-8601"), strings.HasPrefix(word, "--rfc-3339="),
			strings.HasPrefix(word, "--date="):
		case word == "-d" || word == "--date":
			i++ // the date string to display
		default:
			// Rejects -s/--set and bare MMDDhhmm operands (both set the clock)
			// and -f/-r (read files).
			return false
		}
	}
	return true
}

func bashReadOnlyGitArgs(args []string) bool {
	// The subcommand must come first: git global options (-c, -C, --exec-path,
	// --git-dir, ...) can redirect Git or execute arbitrary programs.
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "status", "log", "diff", "show":
	default:
		return false
	}
	for _, word := range args[1:] {
		for _, rejected := range []string{"--output", "--ext-diff", "--textconv", "--no-index", "--open-files-in-pager", "-O", "--orderfile"} {
			if strings.HasPrefix(word, rejected) {
				return false
			}
		}
	}
	return true
}
