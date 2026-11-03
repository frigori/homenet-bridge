package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSession struct {
	mu       sync.Mutex
	until    time.Time
	devices  map[string]string
	extended time.Duration
	stopped  bool
}

func (f *fakeSession) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := Status{Service: "jellyfin", Address: "http://192.168.0.42:8096", Until: f.until}
	for ip, state := range f.devices {
		st.Devices = append(st.Devices, Device{IP: ip, Code: "1234", State: state})
	}
	return st
}

func (f *fakeSession) Decide(ip netip.Addr, approve bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[ip.String()]; !ok {
		return false
	}
	f.devices[ip.String()] = map[bool]string{true: "approved", false: "denied"}[approve]
	return true
}

func (f *fakeSession) Extend(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extended += d
	f.until = f.until.Add(d)
}

func (f *fakeSession) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

// socketPath is short: t.TempDir() can exceed the Unix socket path limit on macOS.
func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, SocketName)
}

type conn struct {
	t   *testing.T
	c   net.Conn
	sc  *bufio.Scanner
	enc *json.Encoder
}

func dial(t *testing.T, path string) *conn {
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &conn{t: t, c: c, sc: bufio.NewScanner(c), enc: json.NewEncoder(c)}
}

func (c *conn) next() Message {
	c.t.Helper()
	c.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !c.sc.Scan() {
		c.t.Fatalf("no message: %v", c.sc.Err())
	}
	var m Message
	if err := json.Unmarshal(c.sc.Bytes(), &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *conn) send(cmd Command) {
	c.t.Helper()
	if err := c.enc.Encode(cmd); err != nil {
		c.t.Fatal(err)
	}
}

func start(t *testing.T, sess Session) (*Server, string) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &Server{Session: sess}
	go srv.Serve(ctx, ln)
	return srv, path
}

func TestStatusAndCommands(t *testing.T) {
	until := time.Date(2026, 11, 3, 22, 0, 0, 0, time.UTC)
	sess := &fakeSession{until: until, devices: map[string]string{"192.168.0.23": "pending"}}
	srv, path := start(t, sess)

	c := dial(t, path)
	m := c.next()
	if m.Type != "status" || m.Status.Address != "http://192.168.0.42:8096" || len(m.Status.Devices) != 1 {
		t.Fatalf("first message = %+v", m)
	}

	c.send(Command{Cmd: "approve", IP: "192.168.0.23"})
	// The bridge reports decisions through the registry's changes, as main does.
	deadline := time.Now().Add(2 * time.Second)
	for sess.Status().Devices[0].State != "approved" {
		if time.Now().After(deadline) {
			t.Fatal("approve did not reach the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv.Changed()
	if m := c.next(); m.Status.Devices[0].State != "approved" {
		t.Fatalf("after approve: %+v", m.Status.Devices)
	}

	c.send(Command{Cmd: "extend", Minutes: 60})
	if m := c.next(); !m.Status.Until.Equal(until.Add(time.Hour)) {
		t.Fatalf("until after extend = %v", m.Status.Until)
	}

	for _, bad := range []Command{
		{Cmd: "approve", IP: "192.168.0.99"},
		{Cmd: "deny", IP: "not-an-ip"},
		{Cmd: "extend", Minutes: 0},
		{Cmd: "extend", Minutes: 24 * 60},
		{Cmd: "reboot"},
	} {
		c.send(bad)
		if m := c.next(); m.Type != "error" || m.Error == "" {
			t.Errorf("%+v: got %+v, want an error", bad, m)
		}
	}

	c.send(Command{Cmd: "stop"})
	deadline = time.Now().Add(2 * time.Second)
	for {
		sess.mu.Lock()
		stopped := sess.stopped
		sess.mu.Unlock()
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop did not reach the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEveryClientGetsChanges(t *testing.T) {
	sess := &fakeSession{devices: map[string]string{}}
	srv, path := start(t, sess)
	a, b := dial(t, path), dial(t, path)
	a.next()
	b.next()

	sess.mu.Lock()
	sess.devices["192.168.0.30"] = "pending"
	sess.mu.Unlock()
	srv.Changed()
	for _, c := range []*conn{a, b} {
		if m := c.next(); len(m.Status.Devices) != 1 {
			t.Errorf("devices = %+v", m.Status.Devices)
		}
	}
}

func TestListen(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %v, want 0600", perm)
	}

	// A live socket belongs to another bridge.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second Listen: %v, want already running", err)
	}

	// A stale one, left by a bridge that crashed, is replaced.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	ln2, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	ln2.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}
}

func TestDefaultPath(t *testing.T) {
	if got := DefaultPath("/home/me/.config/homenet-bridge"); got != "/home/me/.config/homenet-bridge/control.sock" {
		t.Errorf("DefaultPath = %q", got)
	}
	long := "/" + strings.Repeat("x", 120)
	if got := DefaultPath(long); filepath.Dir(got) != filepath.Clean(os.TempDir()) {
		t.Errorf("DefaultPath(long) = %q, want it in the temp dir", got)
	}
}
