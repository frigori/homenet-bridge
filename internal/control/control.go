// Package control serves a local socket through which a front end, such as the macOS menu
// bar app, follows a sharing session and acts on it: approve or deny devices, extend the
// session, stop it.
//
// The protocol is JSON, one object per line. On connecting, and after every change, the
// bridge sends {"type":"status","status":{...}}. The client sends commands:
//
//	{"cmd":"approve","ip":"192.168.0.23"}
//	{"cmd":"deny","ip":"192.168.0.23"}
//	{"cmd":"extend","minutes":60}
//	{"cmd":"stop"}
//
// A command that fails gets {"type":"error","error":"..."}. The socket is only accessible
// to the user running the bridge, who could approve devices in its terminal anyway.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SocketName is the socket's file name in the config directory.
const SocketName = "control.sock"

// DefaultPath is where the bridge listens and front ends connect: <dir>/control.sock, or
// the same name in the temporary directory if that path is too long for a Unix socket.
func DefaultPath(dir string) string {
	p := filepath.Join(dir, SocketName)
	// sun_path holds 104 bytes on macOS, 108 on Linux, including the terminating NUL.
	if dir == "" || len(p) > 103 {
		return filepath.Join(os.TempDir(), "homenet-bridge-"+SocketName)
	}
	return p
}

type Device struct {
	IP        string    `json:"ip"`
	Code      string    `json:"code"`
	State     string    `json:"state"`
	Hint      string    `json:"hint,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
}

type Status struct {
	Service  string `json:"service"`
	Upstream string `json:"upstream"`
	// Address is what devices open, e.g. http://192.168.0.42:8096.
	Address string    `json:"address"`
	Bridge  string    `json:"bridge"`
	Until   time.Time `json:"until"`
	Devices []Device  `json:"devices"`
}

type Message struct {
	Type   string  `json:"type"`
	Status *Status `json:"status,omitempty"`
	Error  string  `json:"error,omitempty"`
}

type Command struct {
	Cmd     string `json:"cmd"`
	IP      string `json:"ip,omitempty"`
	Minutes int    `json:"minutes,omitempty"`
}

// Session is the sharing session being controlled.
type Session interface {
	Status() Status
	// Decide approves or denies ip; false if ip never showed up.
	Decide(ip netip.Addr, approve bool) bool
	Extend(d time.Duration)
	Stop()
}

// MaxExtend caps a single extend command.
const MaxExtend = 12 * time.Hour

// Listen creates the socket at path, readable and writable only by this user. A socket
// left behind by a bridge that crashed is replaced; one that still answers is an error.
func Listen(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another homenet-bridge is already running (%s)", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

type Server struct {
	Session Session
	Logger  *log.Logger

	mu      sync.Mutex
	clients map[*client]struct{}
}

type client struct {
	conn net.Conn
	// notify asks the writer to send the current status; several changes collapse into one.
	notify chan struct{}
	errs   chan string
}

// Serve accepts front ends on ln until ctx ends, then closes ln (removing the socket).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.closeAll()
				return nil
			}
			return err
		}
		c := &client{conn: conn, notify: make(chan struct{}, 1), errs: make(chan string, 4)}
		s.mu.Lock()
		if s.clients == nil {
			s.clients = map[*client]struct{}{}
		}
		s.clients[c] = struct{}{}
		s.mu.Unlock()
		c.notify <- struct{}{}
		go s.write(c)
		go s.read(c)
	}
}

// Changed sends the current status to every front end.
func (s *Server) Changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.conn.Close()
	}
}

func (s *Server) drop(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		close(c.notify)
		c.conn.Close()
	}
}

func (s *Server) write(c *client) {
	enc := json.NewEncoder(c.conn)
	for {
		var msg Message
		select {
		case _, ok := <-c.notify:
			if !ok {
				return
			}
			st := s.Session.Status()
			msg = Message{Type: "status", Status: &st}
		case e := <-c.errs:
			msg = Message{Type: "error", Error: e}
		}
		c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := enc.Encode(msg); err != nil {
			s.drop(c)
			return
		}
	}
}

func (s *Server) read(c *client) {
	defer s.drop(c)
	dec := json.NewDecoder(c.conn)
	for {
		var cmd Command
		if err := dec.Decode(&cmd); err != nil {
			return
		}
		if err := s.run(cmd); err != nil {
			select {
			case c.errs <- err.Error():
			default:
			}
		}
	}
}

func (s *Server) run(cmd Command) error {
	switch cmd.Cmd {
	case "approve", "deny":
		ip, err := netip.ParseAddr(cmd.IP)
		if err != nil {
			return fmt.Errorf("%s: %w", cmd.Cmd, err)
		}
		if !s.Session.Decide(ip, cmd.Cmd == "approve") {
			return fmt.Errorf("%s: no device %s", cmd.Cmd, ip)
		}
	case "extend":
		d := time.Duration(cmd.Minutes) * time.Minute
		if d <= 0 || d > MaxExtend {
			return fmt.Errorf("extend: minutes must be between 1 and %d", int(MaxExtend.Minutes()))
		}
		s.Session.Extend(d)
		s.Changed()
	case "stop":
		s.Session.Stop()
	default:
		return fmt.Errorf("unknown command %q", cmd.Cmd)
	}
	return nil
}
