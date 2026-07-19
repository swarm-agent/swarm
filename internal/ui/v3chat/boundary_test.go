package v3chat

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSourceBoundaryUsesOnlyV3NativeChatAuthority(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"ChatPage",
		"chat_page",
		"chat_",
		"/ws",
		"/v1/",
		"/v2/",
		"StreamSessionV3",
		"SessionV3StreamFrame",
		"StreamEvents",
		"SessionRunStream",
		"time.NewTicker",
		"time.Tick(",
		"time.After(",
		"SetReadDeadline",
		"SetDeadline",
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, "_test.go") || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbiddenToken := range forbidden {
			if strings.Contains(string(raw), forbiddenToken) {
				t.Errorf("%s contains forbidden legacy/polling boundary %q", name, forbiddenToken)
			}
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s imports: %v", name, err)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("decode %s import %q: %v", name, imported.Path.Value, err)
			}
			if importPath == "swarm-refactor/swarmtui/internal/ui" {
				t.Errorf("%s imports the legacy UI package", name)
			}
		}
	}
}
