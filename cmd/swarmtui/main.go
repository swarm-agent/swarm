package main

import (
	"errors"
	"fmt"
	"os"

	"swarm-refactor/swarmtui/internal/app"
	"swarm-refactor/swarmtui/internal/updatehandoff"
)

func main() {
	a, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start swarmtui: %v\n", err)
		os.Exit(1)
	}
	defer a.Close()

	if err := a.Run(); err != nil {
		// os.Exit bypasses deferred calls, so restore the terminal before
		// handing control back to the launcher for update work.
		a.Close()
		if errors.Is(err, updatehandoff.ErrDevUpdateRequested) {
			os.Exit(updatehandoff.ExitCodeDevUpdateRequested)
		}
		fmt.Fprintf(os.Stderr, "swarmtui error: %v\n", err)
		os.Exit(1)
	}
}
