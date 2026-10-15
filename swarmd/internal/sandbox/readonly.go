package sandbox

import (
	"fmt"
	"strings"
	"unicode"
)

// ReadOnlyCommand deliberately accepts no shell language. A separate read-only
// container would not be the job sandbox, and changing shared mounts would race
// the Coder. Execute fixed system readers directly instead, never a shell or a
// workspace executable. Unknown flags fail closed (notably rg --pre).
func ReadOnlyCommand(command string) ([]string, error) {
	deny := func() ([]string, error) {
		return nil, fmt.Errorf("read-only bash refused command: only pwd, ls, cat, head, tail, wc, grep and rg with safe literal arguments are allowed; shell syntax, writes and git commit are prohibited")
	}
	if strings.IndexFunc(command, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" /._-:=,", r))
	}) >= 0 {
		return deny()
	}
	args := strings.Fields(command)
	if len(args) == 0 {
		return deny()
	}
	flags := map[string]map[string]bool{
		"pwd": {}, "ls": {"-l": true, "-a": true, "-la": true, "-al": true}, "cat": {"-n": true},
		"head": {"-n": true}, "tail": {"-n": true}, "wc": {"-l": true, "-c": true, "-w": true},
		"grep": {"-n": true, "-r": true, "-R": true, "-i": true, "-F": true, "-l": true},
		"rg":   {"-n": true, "-i": true, "-F": true, "-l": true, "--files": true},
	}
	allowed, ok := flags[args[0]]
	if !ok {
		return deny()
	}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-") && arg != "--" && !allowed[arg] {
			return deny()
		}
	}
	args[0] = "/usr/bin/" + args[0]
	return args, nil
}
