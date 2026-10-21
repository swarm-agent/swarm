package agent

import (
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestMachineToolSchemasCompile(t *testing.T) {
	for _, definition := range pebblestore.MachineClientTools() {
		if _, err := CompileClientToolSchema(definition.InputSchema); err != nil {
			t.Fatalf("%s: %v", definition.Name, err)
		}
	}
	if _, err := ValidateClientToolArguments(pebblestore.MachineClientTools()[1].InputSchema, `{"command":"uname -a"}`); err != nil {
		t.Fatalf("machine_run arguments: %v", err)
	}
	if _, err := ValidateClientToolArguments(pebblestore.MachineClientTools()[1].InputSchema, `{"command":"x","shell":"zsh"}`); err == nil {
		t.Fatal("machine_run accepted an unknown argument")
	}
}
