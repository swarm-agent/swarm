package runtime

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/config"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tailscale"
)

// New preserves the existing daemon's Desktop composition. The standalone
// Headless command calls NewHeadless and never references this constructor.
func New(cfg config.Config) (*Daemon, error) {
	var desktopAddr string
	var detector *tailscale.Detector
	if cfg.DesktopPort > 0 {
		host, _, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			return nil, fmt.Errorf("resolve desktop listen host: %w", err)
		}
		desktopAddr = net.JoinHostPort(host, strconv.Itoa(cfg.DesktopPort))
		detector, err = tailscale.NewDetector(tailscale.Config{
			Listener: tailscale.Listener{Host: host, Port: cfg.DesktopPort},
		})
		if err != nil {
			return nil, fmt.Errorf("configure tailscale verifier: %w", err)
		}
	}
	d, err := newDaemon(cfg, func(server *api.Server, store *pebblestore.Store) {
		api.ConfigureDesktop(server)
		if detector != nil {
			server.SetTailscaleServePolicy(pebblestore.NewTailscaleServeAllowlistStore(store), detector)
		}
	})
	if err != nil {
		return nil, err
	}
	if desktopAddr != "" {
		d.clientServers = append(d.clientServers, &clientHTTPServer{
			name: "desktop",
			server: &http.Server{
				Addr:              desktopAddr,
				Handler:           d.apiServer.DesktopHandler(),
				ReadTimeout:       10 * time.Second,
				ReadHeaderTimeout: 5 * time.Second,
				WriteTimeout:      0,
				IdleTimeout:       60 * time.Second,
			},
		})
	}
	return d, nil
}
