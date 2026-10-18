// Command swarm-headless is the standalone backend composition. It shares
// canonical services with swarmd but does not link its Desktop startup path.
package main

import (
	"flag"
	"fmt"
	"os"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/runtime"
)

func main() {
	// Preserve configuration introspection without opening stores or a listener.
	if len(os.Args) == 3 && os.Args[1] == "config" && os.Args[2] == "removed-keys" {
		for _, key := range startupconfig.RemovedKeys() {
			fmt.Println(key.Name)
		}
		return
	}
	cfg, err := config.ParseHeadless(os.Args[1:])
	if err == flag.ErrHelp {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarm-headless config error: %v\n", err)
		os.Exit(2)
	}
	d, err := runtime.NewHeadless(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarm-headless startup error: %v\n", err)
		os.Exit(1)
	}
	err = d.Run()
	closeErr := d.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarm-headless runtime error: %v\n", err)
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "swarm-headless shutdown error: %v\n", closeErr)
	}
	if err != nil || closeErr != nil {
		os.Exit(1)
	}
}
