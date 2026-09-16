// testbench-codex-broker owns one dedicated test login. It is not swarmd and
// cannot read the host daemon's store: init requires a new private state root.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/net/netutil"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/provider/codex"
	db "swarm/packages/swarmd/internal/store/pebble"
	bridge "swarm/packages/swarmd/internal/testbenchcodex"
)

const marker = "dedicated-testbench-codex-v1\n"
const account = "testbench-dedicated"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "testbench broker operation failed (no provider diagnostics emitted)")
		os.Exit(1)
	}
}
func run() error {
	syscall.Umask(0077)
	action := flag.String("action", "status", "init, login, status, serve")
	root := flag.String("state", "", "explicit dedicated state root")
	sockets := flag.String("sockets", "", "comma-separated lane socket paths (serve only)")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*root) || filepath.Clean(*root) != *root {
		return errors.New("explicit canonical root required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(*root))
	if err != nil || parent != filepath.Dir(*root) {
		return errors.New("unsafe state parent")
	}
	for path := parent; ; path = filepath.Dir(path) {
		i, e := os.Stat(path)
		if e != nil || i.Mode().Perm()&0022 != 0 {
			return errors.New("writable state ancestor")
		}
		if path == filepath.Dir(path) {
			break
		}
	}
	if *action == "init" {
		if err := os.Mkdir(*root, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(*root, "purpose"), []byte(marker), 0600)
	}
	resolved, err := filepath.EvalSymlinks(*root)
	if err != nil || resolved != *root {
		return errors.New("unsafe root")
	}
	info, err := os.Lstat(*root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return errors.New("private owned root required")
	}
	markerInfo, err := os.Lstat(filepath.Join(*root, "purpose"))
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm() != 0600 {
		return errors.New("unsafe purpose marker")
	}
	purpose, err := os.ReadFile(filepath.Join(*root, "purpose"))
	if err != nil || string(purpose) != marker {
		return errors.New("dedicated initialization required")
	}
	// Shared process lock prevents login or a second broker rotating the same
	// dedicated token while serve owns it. Pebble adds its independent DB lock.
	lock, err := os.OpenFile(filepath.Join(*root, "owner.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	database := filepath.Join(*root, "credentials")
	if i, e := os.Lstat(database); e == nil && (!i.IsDir() || i.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe database")
	}
	store, err := db.Open(database)
	if err != nil {
		return err
	}
	defer store.Close()
	auth := db.NewAuthStore(store)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx = identity.ContextWithPrincipal(ctx, identity.Principal{Type: identity.PrincipalTypeUser, UserID: account, AccountScopeID: account})
	switch *action {
	case "status":
		_, ok, err := auth.GetCodexAuthRecordForAccount(account)
		if err != nil {
			return err
		}
		fmt.Printf("dedicated_login_configured=%t\n", ok)
		return nil
	case "login":
		loginCtx, stop := context.WithTimeout(ctx, 9*time.Minute)
		defer stop()
		authorization, err := codex.RequestDeviceAuthorization(loginCtx)
		if err != nil {
			return err
		}
		fmt.Printf("Complete the dedicated testbench login at %s using code %s. Do not paste tokens.\n", authorization.VerificationURL, authorization.UserCode)
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					fmt.Println("Waiting for dedicated testbench login approval...")
				}
			}
		}()
		tokens, err := codex.CompleteDeviceAuthorization(loginCtx, authorization)
		if err != nil {
			return err
		}
		_, err = auth.SetCodexOAuthForAccount(account, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt, "")
		if err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(*root, "request-in-flight")); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("Dedicated testbench login saved; credentials were not exported.")
		return nil
	case "serve":
		paths := strings.Split(*sockets, ",")
		if len(paths) < 1 || len(paths) > 8 {
			return errors.New("one to eight lane sockets required")
		}
		client := codex.NewClient(auth)
		broker := bridge.NewBroker(func(c context.Context, r codex.Request, e func(codex.StreamEvent)) (codex.Response, error) {
			c = identity.ContextWithPrincipal(c, identity.Principal{Type: identity.PrincipalTypeUser, UserID: account, AccountScopeID: account})
			return guardedRequest(*root, func() (codex.Response, error) { return client.CreateResponseStreaming(c, r, e) })
		}, func(context.Context) bool {
			if _, err := os.Lstat(filepath.Join(*root, "request-in-flight")); !os.IsNotExist(err) {
				return false
			}
			r, ok, e := auth.GetCodexAuthRecordForAccount(account)
			return e == nil && ok && r.AccessToken != "" && r.RefreshToken != ""
		})
		var servers []*http.Server
		defer func() {
			for _, s := range servers {
				s.Close()
			}
		}()
		seen := map[string]bool{}
		for _, path := range paths {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[path] {
				return errors.New("invalid lane socket")
			}
			seen[path] = true
			parent, e := filepath.EvalSymlinks(filepath.Dir(path))
			if e != nil || parent != filepath.Dir(path) {
				return errors.New("unsafe socket parent")
			}
			i, e := os.Stat(parent)
			if e != nil || i.Mode().Perm() != 0700 || int(i.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
				return errors.New("unsafe socket directory")
			}
			if _, e := os.Lstat(path); !os.IsNotExist(e) {
				return errors.New("socket already exists; no replacement")
			}
			listener, e := net.Listen("unix", path)
			if e != nil {
				return e
			}
			// Ancestor directories are private on the host; readonly per-lane bind
			// exposes only this socket to that isolated guest, never the auth root.
			if e := os.Chmod(path, 0666); e != nil {
				listener.Close()
				return e
			}
			s := &http.Server{Handler: broker.Handler(path), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
			servers = append(servers, s)
			go func() {
				if e := s.Serve(netutil.LimitListener(listener, 16)); e != nil && e != http.ErrServerClosed {
					cancel()
				}
			}()
		}
		fmt.Println("Dedicated Codex broker ready; one serialized refresh authority.")
		<-ctx.Done()
		return nil
	default:
		return errors.New("unknown action")
	}
}
