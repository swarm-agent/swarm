package launcher

import (
	"reflect"
	"testing"
)

func TestWithCtlAddrLeavesSetupOnTheOwnerSocket(t *testing.T) {
	const url = "http://127.0.0.1:7781"
	for _, tc := range []struct {
		in, want []string
	}{
		{[]string{"setup", "identity", "--username", "owner"}, []string{"setup", "identity", "--username", "owner"}},
		{[]string{"setup", "status"}, []string{"setup", "status"}},
		{[]string{"sessions", "list"}, []string{"sessions", "list", "--addr", url}},
		{[]string{"auth", "status"}, []string{"auth", "status", "--addr", url}},
		{[]string{"sessions", "list", "--addr", "http://other"}, []string{"sessions", "list", "--addr", "http://other"}},
	} {
		if got := withCtlAddr(append([]string{}, tc.in...), url); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("withCtlAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
