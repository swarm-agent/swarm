package ui

import (
	"reflect"
	"testing"
)

func TestWrapNormalizesLoneCarriageReturns(t *testing.T) {
	got := Wrap("Enumerating objects: 5\rWriting objects: 100% (5/5)\rremote: done\n", 80)
	want := []string{
		"Enumerating objects: 5",
		"Writing objects: 100% (5/5)",
		"remote: done",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Wrap() = %#v, want %#v", got, want)
	}
}
