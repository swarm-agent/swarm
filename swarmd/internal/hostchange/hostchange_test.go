package hostchange

// Purpose: the daemon's only way to change the machine is one line to the
// installer-placed helper socket; it never runs a program, sudo or a shell.
// These hermetic tests pin the wire contract against a fake socket server in
// the run's temporary directory: the exact request line (`OP PATH`, path
// may hold spaces, never a line break), one JSON reply line parsed into
// Result, ok:false carried as a failure (no Go error), a helper that is not
// running reported as ErrNotRunning, an unconfigured socket as ErrUnsupported,
// and the timeout enforced on a helper that never answers.

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHelper struct {
	socket string
	mu     sync.Mutex
	lines  []string
	reply  func(line string) string
}

func newFakeHelper(t *testing.T, reply func(line string) string) *fakeHelper {
	t.Helper()
	// Unix socket names are short-limited; keep the directory name small.
	dir, err := os.MkdirTemp("", "hc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeHelper{socket: filepath.Join(dir, "helper.sock"), reply: reply}
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				f.mu.Lock()
				f.lines = append(f.lines, strings.TrimSuffix(line, "\n"))
				f.mu.Unlock()
				if out := f.reply(line); out != "" {
					_, _ = conn.Write([]byte(out))
				}
			}()
		}
	}()
	return f
}

func (f *fakeHelper) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lines...)
}

func TestClientSpeaksOneLineProtocol(t *testing.T) {
	helper := newFakeHelper(t, func(line string) string {
		switch {
		case strings.HasPrefix(line, "mount-workspace "):
			return `{"ok":true,"op":"mount-workspace","path":"/home/person/My Code","owner":"person","restart_required":true}` + "\n"
		case line == "restart-daemon\n":
			return `{"ok":true,"op":"restart-daemon"}` + "\n"
		case strings.HasPrefix(line, "unmount-workspace "):
			return `{"ok":false,"error":"not mounted"}` + "\n"
		}
		return `{"ok":false,"error":"unknown op"}` + "\n"
	})
	c := NewClient(helper.socket)
	if !c.Supported() {
		t.Fatal("configured socket reported unsupported")
	}
	res, err := c.Run(context.Background(), OpMountWorkspace, "/home/person/My Code")
	if err != nil || !res.OK || !res.RestartRequired || res.Owner != "person" || res.Path != "/home/person/My Code" {
		t.Fatalf("mount: %+v %v", res, err)
	}
	if !strings.HasPrefix(res.Raw, `{"ok":true`) {
		t.Fatalf("raw reply not kept: %q", res.Raw)
	}
	res, err = c.Run(context.Background(), OpRestartDaemon, "")
	if err != nil || !res.OK {
		t.Fatalf("restart: %+v %v", res, err)
	}
	// ok:false is the helper's answer, not a transport failure.
	res, err = c.Run(context.Background(), OpUnmountWorkspace, "/home/person/other")
	if err != nil || res.OK || res.Error != "not mounted" {
		t.Fatalf("refusal: %+v %v", res, err)
	}
	want := []string{"mount-workspace /home/person/My Code", "restart-daemon", "unmount-workspace /home/person/other"}
	if got := helper.received(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("helper received %q, want %q", got, want)
	}
}

func TestClientRefusesBadRequestsWithoutConnecting(t *testing.T) {
	helper := newFakeHelper(t, func(string) string { return `{"ok":true}` + "\n" })
	c := NewClient(helper.socket)
	for _, bad := range [][2]string{
		{OpMountWorkspace, ""},
		{OpMountWorkspace, "relative/path"},
		{OpMountWorkspace, "/home/x\nrestart-daemon"},
		{OpRestartDaemon, "/home/x"},
		{"format-disk", "/"},
	} {
		if _, err := c.Run(context.Background(), bad[0], bad[1]); err == nil {
			t.Errorf("%q %q accepted", bad[0], bad[1])
		}
	}
	if got := helper.received(); len(got) != 0 {
		t.Fatalf("invalid requests reached the helper: %q", got)
	}
}

func TestClientReportsUnsupportedNotRunningAndTimeout(t *testing.T) {
	if _, err := NewClient("").Run(context.Background(), OpMountWorkspace, "/home/x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unconfigured: %v", err)
	}
	if NewClient("").Supported() {
		t.Fatal("empty socket reported supported")
	}
	dir, err := os.MkdirTemp("", "hc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if _, err := NewClient(filepath.Join(dir, "absent.sock")).Run(context.Background(), OpMountWorkspace, "/home/x"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("absent socket: %v", err)
	}

	silent := newFakeHelper(t, func(string) string { time.Sleep(2 * time.Second); return "" })
	c := NewClient(silent.socket)
	c.timeout = 200 * time.Millisecond
	start := time.Now()
	_, err = c.Run(context.Background(), OpMountWorkspace, "/home/x")
	if err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("silent helper: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}

	garbage := newFakeHelper(t, func(string) string { return "not json\n" })
	if _, err := NewClient(garbage.socket).Run(context.Background(), OpMountWorkspace, "/home/x"); err == nil || !strings.Contains(err.Error(), "not one JSON line") {
		t.Fatalf("garbage reply: %v", err)
	}
}

func TestValidateSocketPath(t *testing.T) {
	for _, ok := range []string{"", "/run/swarm-host-change.sock"} {
		if err := ValidateSocketPath(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"run/x.sock", "/run/../x.sock", "/run//x.sock"} {
		if err := ValidateSocketPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
