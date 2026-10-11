package permission

import (
	"encoding/json"
	"regexp"
	"strings"

	"swarm/packages/swarmd/internal/tool"
)

// bashSimpleCommand is one simple shell command split into words.
type bashSimpleCommand struct {
	Words []string
	// Expands reports an unquoted glob (*, ?, [) or tilde in any word: the
	// shell would replace it with paths the policy never saw.
	Expands bool
	// WorkspacePath is the bash tool's explicit workspace_path argument. The
	// command then runs in that repository instead of the session's cwd.
	WorkspacePath string
}

// parseBashSimpleCommand returns the shell words of one simple command, or
// false when the raw command uses any shell syntax beyond plain words and
// balanced quotes: separators, pipes, redirection, substitution, expansion,
// grouping, escapes or line breaks. Built-in allows only apply to such commands.
func parseBashSimpleCommand(arguments string) (bashSimpleCommand, bool) {
	var payload struct {
		Command       string `json:"command"`
		WorkspacePath string `json:"workspace_path"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(arguments)), &payload); err != nil {
		return bashSimpleCommand{}, false
	}
	command := strings.TrimSpace(payload.Command)
	if command == "" || strings.ContainsAny(command, ";&|<>`$(){}\\\n\r") {
		return bashSimpleCommand{}, false
	}
	out := bashSimpleCommand{WorkspacePath: strings.TrimSpace(payload.WorkspacePath)}
	var current strings.Builder
	var quote, previous rune
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
				out.Words = append(out.Words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			// Globs expand anywhere; a tilde only at the start of a word or
			// after = or : (HEAD~1 stays literal).
			if strings.ContainsRune("*?[", r) || (r == '~' && (current.Len() == 0 || previous == '=' || previous == ':')) {
				out.Expands = true
			}
			current.WriteRune(r)
			inWord = true
		}
		previous = r
	}
	if quote != 0 {
		return bashSimpleCommand{}, false
	}
	if inWord {
		out.Words = append(out.Words, current.String())
	}
	return out, len(out.Words) > 0
}

// bashClearlyReadOnlyCommand reports whether a simple command only observes
// state that the session may already observe without approval:
//   - pwd, date, echo and printf read no files;
//   - read-only git subcommands are equivalent to git_status/git_diff/list;
//   - ls, wc, cat, head, tail, stat, file, grep, rg, tree and du read files
//     only when scope (the trusted execution scope; nil when unknown) is known,
//     the command runs in its cwd, and the cwd and every path operand resolve,
//     symlinks followed, to an existing regular file or directory inside the
//     same read roots the read tool enforces. Options are allowlisted per
//     command: anything that follows symlinks, reads file names or patterns
//     from another file, runs a helper program, writes, or never ends is
//     not covered.
func bashClearlyReadOnlyCommand(cmd bashSimpleCommand, scope *tool.WorkspaceScope) bool {
	words := cmd.Words
	if len(words) == 0 || cmd.Expands {
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
	case "echo", "printf":
		// Shell builtins that print their arguments; the simple-command parse
		// already rejected redirection, substitution and expansion.
		return true
	case "git":
		return bashReadOnlyGitArgs(words[1:])
	}
	spec, ok := bashScopedReadSpecs[words[0]]
	if !ok || scope == nil || cmd.WorkspacePath != "" {
		return false
	}
	operands, ok := spec.operands(words[1:])
	if !ok {
		return false
	}
	// The implicit cwd is checked too: recursive commands without operands
	// read it, and relative operands are resolved from it.
	for _, path := range append([]string{"."}, operands...) {
		if _, err := tool.ResolveExistingReadPath(*scope, path); err != nil {
			return false
		}
	}
	return true
}

// BashCommandReadsWorkspacePaths reports whether bash arguments name a single
// simple path-reading command whose built-in allow needs the trusted execution
// scope, so callers resolve that scope only when it can matter.
func BashCommandReadsWorkspacePaths(arguments string) bool {
	cmd, ok := parseBashSimpleCommand(arguments)
	if !ok || cmd.Expands || cmd.WorkspacePath != "" {
		return false
	}
	_, scoped := bashScopedReadSpecs[cmd.Words[0]]
	return scoped
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
	for _, word := range args[1:] {
		for _, rejected := range []string{"--output", "--ext-diff", "--textconv", "--no-index", "--open-files-in-pager", "-O", "--orderfile"} {
			if strings.HasPrefix(word, rejected) {
				return false
			}
		}
	}
	rest := args[1:]
	switch args[0] {
	case "status", "log", "diff", "show", "show-ref", "describe":
		return true
	case "branch":
		// Listing only: any other argument creates, deletes, moves, copies or
		// configures a branch.
		return bashWordsWithin(rest, "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "-l", "--list", "--show-current")
	case "worktree":
		return len(rest) > 0 && rest[0] == "list" && bashWordsWithin(rest[1:], "--porcelain", "-v", "--verbose", "-z")
	case "remote":
		return bashWordsWithin(rest, "-v", "--verbose")
	case "rev-parse":
		for _, word := range rest {
			if strings.HasPrefix(word, "--resolve-git-dir") || strings.HasPrefix(word, "--parseopt") {
				return false
			}
		}
		return true
	case "ls-files":
		// -X/--exclude-from read patterns from a file anywhere on disk.
		for _, word := range rest {
			if strings.HasPrefix(word, "--exclude-from") || (bashShortOptionWord(word) && strings.Contains(word, "X")) {
				return false
			}
		}
		return true
	case "blame":
		// --contents, -S and --ignore-revs-file read another file.
		for _, word := range rest {
			if strings.HasPrefix(word, "--contents") || strings.HasPrefix(word, "--ignore-revs-file") || (bashShortOptionWord(word) && strings.Contains(word, "S")) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func bashWordsWithin(words []string, allowed ...string) bool {
	for _, word := range words {
		ok := false
		for _, candidate := range allowed {
			if word == candidate {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func bashShortOptionWord(word string) bool {
	return len(word) > 1 && word[0] == '-' && word[1] != '-'
}

var bashNumericOptionValue = regexp.MustCompile(`^[+-]?[0-9]+[a-zA-Z]{0,3}$`)

// bashReadSpec allowlists the options of one path-reading command. Every word
// that is not an allowed option (or its value) is a path operand.
type bashReadSpec struct {
	// flags are single-letter options without a value; they may be clustered.
	flags string
	// valued are single-letter options taking a value (attached or next word).
	valued string
	// numeric lists the valued/long options whose value must be a count.
	numeric map[string]bool
	// long are --options without a value; longValue take --option=value.
	long, longValue map[string]bool
	// numericShort admits -NUM (a line count) for head/tail.
	numericShort bool
	// pattern marks grep/rg: the first operand is the pattern unless one was
	// given with -e/--regexp.
	pattern bool
}

func bashSet(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func (spec bashReadSpec) operands(args []string) ([]string, bool) {
	var operands []string
	patternGiven := false
	for i := 0; i < len(args); i++ {
		word := args[i]
		switch {
		case word == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case word == "-":
			// stdin is not a path the policy can scope.
			return nil, false
		case strings.HasPrefix(word, "--"):
			name, value, attached := strings.Cut(word[2:], "=")
			switch {
			case attached && spec.longValue[name]:
				if spec.numeric[name] && !bashNumericOptionValue.MatchString(value) {
					return nil, false
				}
				if name == "regexp" {
					patternGiven = true
				}
			case !attached && spec.long[name]:
				if name == "files" {
					// rg --files lists paths and takes no pattern: every
					// operand is a path to scope.
					patternGiven = true
				}
			default:
				return nil, false
			}
		case strings.HasPrefix(word, "-"):
			if spec.numericShort && bashNumericOptionValue.MatchString(word) {
				continue
			}
			letters := word[1:]
			for j := 0; j < len(letters); j++ {
				letter := letters[j]
				if strings.IndexByte(spec.flags, letter) >= 0 {
					continue
				}
				if strings.IndexByte(spec.valued, letter) < 0 {
					return nil, false
				}
				value := letters[j+1:]
				if value == "" {
					if i+1 >= len(args) {
						return nil, false
					}
					i++
					value = args[i]
				}
				if spec.numeric[string(letter)] && !bashNumericOptionValue.MatchString(value) {
					return nil, false
				}
				if letter == 'e' && spec.pattern {
					patternGiven = true
				}
				break
			}
		default:
			operands = append(operands, word)
		}
	}
	if spec.pattern && !patternGiven {
		if len(operands) == 0 {
			return nil, false
		}
		operands = operands[1:]
	}
	return operands, true
}

var bashScopedReadSpecs = map[string]bashReadSpec{
	// No -L/-H (dereference), -D (dired) or options taking another file/pattern.
	"ls": {
		flags:     "aAlhRtrS1dFpisnogGcuUXvxmQbkN",
		long:      bashSet("all", "almost-all", "human-readable", "recursive", "reverse", "classify", "directory", "inode", "size", "numeric-uid-gid", "group-directories-first", "color", "full-time"),
		longValue: bashSet("color", "sort", "time-style"),
	},
	// No --files0-from: it reads the operand list from a file.
	"wc": {flags: "lwcmL", long: bashSet("lines", "words", "bytes", "chars", "max-line-length")},
	"cat": {
		flags: "nbAEeTtsuv",
		long:  bashSet("number", "number-nonblank", "show-all", "show-ends", "show-tabs", "squeeze-blank", "show-nonprinting"),
	},
	// No -f/-F/--follow/--retry/--pid/-s: following never ends.
	"head": {flags: "qvz", valued: "nc", numeric: bashSet("n", "c", "lines", "bytes"), numericShort: true, long: bashSet("quiet", "silent", "verbose", "zero-terminated"), longValue: bashSet("lines", "bytes")},
	"tail": {flags: "qvz", valued: "nc", numeric: bashSet("n", "c", "lines", "bytes"), numericShort: true, long: bashSet("quiet", "silent", "verbose", "zero-terminated"), longValue: bashSet("lines", "bytes")},
	"stat": {flags: "Lft", valued: "c", long: bashSet("dereference", "file-system", "terse"), longValue: bashSet("format", "printf")},
	// No -C (writes a compiled magic file), -m/-f (read other files), -z/-Z
	// (decompressors), -s (block devices) or -p (rewrites access times).
	"file": {flags: "bihLkNnr", long: bashSet("brief", "mime", "mime-type", "mime-encoding", "dereference", "no-dereference", "keep-going", "raw", "no-pad")},
	// -r follows only command-line symlinks (resolved above); no -R, -f/--file,
	// --exclude-from, -D/--devices or -d/--directories.
	"grep": {
		flags:     "iyvwxcLlnhHoqsbaIrUzZFEGPT",
		valued:    "eABCm",
		numeric:   bashSet("A", "B", "C", "m", "max-count", "context", "before-context", "after-context"),
		pattern:   true,
		long:      bashSet("ignore-case", "no-ignore-case", "invert-match", "word-regexp", "line-regexp", "count", "files-with-matches", "files-without-match", "line-number", "with-filename", "no-filename", "only-matching", "quiet", "silent", "no-messages", "byte-offset", "text", "recursive", "fixed-strings", "extended-regexp", "basic-regexp", "perl-regexp", "null", "null-data", "color", "colour"),
		longValue: bashSet("include", "exclude", "exclude-dir", "regexp", "max-count", "context", "before-context", "after-context", "color", "colour"),
	},
	// rg never follows symlinks without -L/--follow; no --pre/--pre-glob or
	// -z/--search-zip (run helper programs), --ignore-file or -f/--file.
	"rg": {
		flags:     "iSswxvnNlcFHoUa.u0p",
		valued:    "eABCmtTgM",
		numeric:   bashSet("A", "B", "C", "m", "M", "max-count", "context", "before-context", "after-context", "max-columns", "max-depth"),
		pattern:   true,
		long:      bashSet("ignore-case", "smart-case", "case-sensitive", "word-regexp", "line-regexp", "invert-match", "line-number", "no-line-number", "files-with-matches", "files-without-match", "count", "count-matches", "fixed-strings", "with-filename", "no-filename", "only-matching", "hidden", "no-ignore", "no-ignore-vcs", "files", "heading", "no-heading", "json", "vimgrep", "column", "trim", "unrestricted", "multiline", "stats"),
		longValue: bashSet("regexp", "glob", "iglob", "type", "type-not", "max-count", "context", "before-context", "after-context", "max-columns", "max-depth", "color", "sort", "sortr"),
	},
	// No -l (follow symlinks), -o (write output file), -R or --fromfile.
	"tree": {flags: "adfisDFCnpugQNvtcrxhJX", valued: "LPI", numeric: bashSet("L"), long: bashSet("noreport", "dirsfirst", "gitignore")},
	// No -L/-H/-D (dereference), --files0-from or -X/--exclude-from.
	"du": {
		flags:     "ahcksmbxSl0",
		valued:    "d",
		numeric:   bashSet("d", "max-depth"),
		long:      bashSet("all", "human-readable", "summarize", "total", "apparent-size", "bytes", "one-file-system", "si", "count-links", "null"),
		longValue: bashSet("max-depth"),
	},
}
