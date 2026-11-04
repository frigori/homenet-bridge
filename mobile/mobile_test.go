package mobile

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frigori/homenet-bridge/internal/control"
)

type event struct {
	kind, a, b string
}

// listener records the calls the app would get.
type listener struct{ events chan event }

func (l *listener) OnStatus(status string)          { l.events <- event{"status", status, ""} }
func (l *listener) OnRequest(ip, code, hint string) { l.events <- event{"request", ip, code} }
func (l *listener) OnMessage(msg string)            { l.events <- event{"message", msg, ""} }
func (l *listener) OnStopped(err string)            { l.events <- event{"stopped", err, ""} }

// next waits for the next event of kind, skipping others.
func (l *listener) next(t *testing.T, kind string) event {
	t.Helper()
	timeout := time.After(6 * time.Second)
	for {
		select {
		case e := <-l.events:
			if e.kind == kind {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event", kind)
		}
	}
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// setup serves a fake Jellyfin over TLS whose certificate is saved as a CA file to trust.
func setup(t *testing.T) (cfg *Config, caFile string) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			io.WriteString(w, `{"ServerName":"home","Id":"abc","Version":"10.11.0"}`)
			return
		}
		io.WriteString(w, "hello")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	SetDataDir(filepath.Join(dir, "data"))
	caFile = filepath.Join(dir, "home-ca.crt")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = NewConfig()
	cfg.Upstream = srv.URL
	cfg.Port = freePort(t)
	cfg.Bind = "127.0.0.1"
	cfg.BridgeName = "phone"
	cfg.Discovery = false
	return cfg, caFile
}

func TestSession(t *testing.T) {
	cfg, caFile := setup(t)
	if _, err := Check(cfg); err == nil || !strings.Contains(err.Error(), "add your homenet root CA") {
		t.Fatalf("Check before TrustCA: %v", err)
	}
	fps, err := TrustCA(caFile)
	if err != nil || len(strings.Split(fps, ":")) != 32 {
		t.Fatalf("TrustCA = %q, %v", fps, err)
	}
	if n := TrustedCAs(); n < 1 {
		t.Errorf("TrustedCAs = %d", n)
	}
	if got, err := Check(cfg); err != nil || got != `Jellyfin "home" 10.11.0` {
		t.Fatalf("Check = %q, %v", got, err)
	}

	l := &listener{events: make(chan event, 32)}
	if err := Start(cfg, l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Stop)
	var st control.Status
	if err := json.Unmarshal([]byte(l.next(t, "status").a), &st); err != nil {
		t.Fatal(err)
	}
	if st.Service != "jellyfin" || st.Bridge != "phone" || !strings.HasPrefix(st.Address, "http://127.0.0.1:") {
		t.Fatalf("status = %+v", st)
	}
	if err := Start(cfg, l); err == nil {
		t.Error("second Start: no error")
	}

	// A new device waits for approval; the app allows it.
	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(st.Address + "/web/")
		if err != nil {
			got <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- string(b)
	}()
	req := l.next(t, "request")
	if req.a != "127.0.0.1" || len(req.b) != 4 {
		t.Errorf("request = %+v", req)
	}
	if err := Decide(req.a, true); err != nil {
		t.Fatal(err)
	}
	if body := <-got; body != "hello" {
		t.Errorf("approved device got %q", body)
	}
	if !strings.Contains(Status(), `"state":"approved"`) {
		t.Errorf("Status = %s", Status())
	}
	if err := Decide("192.168.0.99", true); err == nil {
		t.Error("Decide on an unknown device: no error")
	}

	if err := Extend(0); err == nil {
		t.Error("Extend(0): no error")
	}
	if err := Extend(60); err != nil {
		t.Fatal(err)
	}
	if m := l.next(t, "message"); !strings.HasPrefix(m.a, "Extended") {
		t.Errorf("message = %q", m.a)
	}

	Stop()
	if e := l.next(t, "stopped"); e.a != "" {
		t.Errorf("stopped with %q", e.a)
	}
	if Status() != "" {
		t.Error("Status after Stop is not empty")
	}
	if err := Extend(60); err == nil {
		t.Error("Extend after Stop: no error")
	}
}

func TestStartRefuses(t *testing.T) {
	cfg, caFile := setup(t)
	if _, err := TrustCA(caFile); err != nil {
		t.Fatal(err)
	}
	l := &listener{events: make(chan event, 8)}
	for name, change := range map[string]func(*Config){
		"VPN address": func(c *Config) { c.Bind = "100.64.0.1" },
		"bad address": func(c *Config) { c.Bind = "phone" },
		"no time":     func(c *Config) { c.Minutes = 0 },
		"bad port":    func(c *Config) { c.Port = 0 },
	} {
		c := *cfg
		change(&c)
		if err := Start(&c, l); err == nil {
			Stop()
			t.Errorf("%s: started", name)
		}
	}
}
