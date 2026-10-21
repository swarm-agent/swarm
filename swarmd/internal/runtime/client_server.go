package runtime

import (
	"errors"
	"log"
	"net"
	"net/http"
)

// clientHTTPServer is supplied by an optional client composition. Headless
// supplies none. The shared lifecycle owns shutdown without knowing UI routes.
type clientHTTPServer struct {
	name     string
	server   *http.Server
	listener net.Listener
	done     chan struct{}
}

func (d *Daemon) serveClient(client *clientHTTPServer) error {
	ln, err := net.Listen("tcp", client.server.Addr)
	if err != nil {
		return err
	}
	client.listener = ln
	client.server.Addr = ln.Addr().String()
	log.Printf("swarmd client listener name=%q listen=%q", client.name, client.server.Addr)
	client.done = make(chan struct{})
	go func() {
		defer close(client.done)
		if err := client.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("swarmd %s serve error: %v", client.name, err)
			d.requestStop(client.name + "-serve-error")
		}
	}()
	return nil
}
