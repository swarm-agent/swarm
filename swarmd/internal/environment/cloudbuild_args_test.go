package environment

import (
	"strings"
	"testing"
)

// Purpose: Cloud Build interprets dollars in step args as substitutions. Exercise
// the argument builder directly to prove literal-dollar escaping is lossless,
// including shell variables, command substitutions and already doubled dollars.
func TestCloudBuildStepArgsLiteralDollars(t *testing.T) {
	for _, prefix := range []string{"prefix/", "$NAME/${NAME}/$(id)/$$/"} {
		args := cloudBuildStepArgs("bucket", prefix, "sha")
		if len(args) != 2 || args[0] != "-c" {
			t.Fatalf("unexpected args: %q", args)
		}
		if strings.Contains(strings.ReplaceAll(args[1], "$$", ""), "$") {
			t.Fatal("unescaped dollar in step args")
		}
		decoded := strings.ReplaceAll(args[1], "$$", "$")
		if decoded != cloudBuildBootstrap("bucket", prefix, "sha") {
			t.Fatal("escaping changed shell script")
		}
	}
}
