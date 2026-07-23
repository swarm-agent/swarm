package permission

import (
	"strings"
	"testing"
)

func TestPermissionPersistenceHelpersRedactCredentialsAndOmitOutputByDefault(t *testing.T) {
	arguments := permissionStoredArguments(`{"command":"curl https://example.invalid?token=supersecret","api_key":"sk-abcdefghijklmnop"}`)
	if strings.Contains(arguments, "supersecret") || strings.Contains(arguments, "sk-abcdefghijklmnop") {
		t.Fatalf("stored arguments contain credential material: %s", arguments)
	}
	if got := permissionStoredOutput("provider prompt and full tool output", "bash", false); got != "bash executed; detailed output omitted for privacy" {
		t.Fatalf("permissionStoredOutput() = %q", got)
	}
	if got := permissionStoredError("authorization: Bearer supersecret"); strings.Contains(got, "supersecret") {
		t.Fatalf("stored error contains credential material: %s", got)
	}
}
