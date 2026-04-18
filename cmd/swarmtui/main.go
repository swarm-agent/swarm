package main

import (
	"fmt"
	"os"

	"swarm-refactor/swarmtui/internal/app"
)

func main() {
	a, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start swarmtui: %v\n", err)
		os.Exit(1)
	}
	defer a.Close()

	if err := a.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "swarmtui error: %v\n", err)
		os.Exit(1)
	}
}
