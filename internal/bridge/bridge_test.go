package bridge

import (
	"context"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/frigori/homenet-bridge/internal/config"
)

// upstream is a fake Jellyfin over TLS, and roots that trust it.
func upstream(t *testing.T) (*config.Service, *x509.CertPool) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			io.WriteString(w, `{"ServerName":"home","Id":"abc","Version":"10.11.0"}`)
			return
		}
		io.WriteString(w, "hello from "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	return &config.Service{Name: "jellyfin", Upstream: srv.URL, Port: 0, Kind: config.KindJellyfin}, roots
}

var loopback = netip.MustParseAddr("127.0.0.1")

func start(t *testing.T, opts Options) *Session {
	t.Helper()
	sess, err := Start(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sess.Stop()
		sess.Wait()
	})
	return sess
}

func TestProbe(t *testing.T) {
	svc, roots := upstream(t)
	info, err := Probe(svc, roots)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "home" || info.ID != "abc" || info.Describe() != ` (Jellyfin "home" 10.11.0)` {
		t.Errorf("info = %+v", info)
	}
	if _, err := Probe(svc, x509.NewCertPool()); err == nil {
		t.Error("Probe without the upstream's root: no error")
	}
}

func TestSessionProxiesAndEnds(t *testing.T) {
	svc, roots := upstream(t)
	var notes []string
	sess := start(t, Options{
		Service: svc, Roots: roots, Bind: loopback, Name: "laptop", TTL: time.Hour,
		Allow:  []netip.Addr{loopback},
		Notify: func(m string) { notes = append(notes, m) },
	})

	st := sess.Status()
	if st.Service != "jellyfin" || st.Bridge != "laptop" || !strings.HasPrefix(st.Address, "http://127.0.0.1:") {
		t.Fatalf("status = %+v", st)
	}
	resp, err := http.Get(st.Address + "/web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from /web/index.html" {
		t.Errorf("body = %q", body)
	}

	before := sess.Until()
	sess.Extend(30 * time.Minute)
	if got := sess.Until().Sub(before); got != 30*time.Minute {
		t.Errorf("extended by %v", got)
	}
	select {
	case <-sess.Changes():
	case <-time.After(time.Second):
		t.Error("no change signalled after Extend")
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[len(notes)-1], "Extended: sharing until ") {
		t.Errorf("notes = %q", notes)
	}

	sess.Stop()
	select {
	case <-sess.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("session still running after Stop")
	}
	if _, err := http.Get(st.Address); err == nil {
		t.Error("bridge still answering after Stop")
	}
	sess.Extend(time.Hour) // a no-op once over
}

func TestSessionPairing(t *testing.T) {
	svc, roots := upstream(t)
	sess := start(t, Options{Service: svc, Roots: roots, Bind: loopback, Name: "laptop", TTL: time.Hour})

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get(sess.Status().Address + "/web/index.html")
		if err != nil {
			got <- err.Error()
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- string(body)
	}()
	var ip netip.Addr
	select {
	case d := <-sess.Requests():
		ip = d.IP
	case <-time.After(2 * time.Second):
		t.Fatal("no pairing request")
	}
	if !sess.Decide(ip, true) {
		t.Fatal("Decide: unknown device")
	}
	select {
	case <-sess.Decided(ip):
	case <-time.After(time.Second):
		t.Fatal("Decided not closed")
	}
	if s := sess.State(ip).String(); s != "approved" {
		t.Errorf("state = %s", s)
	}
	if body := <-got; body != "hello from /web/index.html" {
		t.Errorf("held request got %q", body)
	}
	if devs := sess.Status().Devices; len(devs) != 1 || devs[0].State != "approved" {
		t.Errorf("devices = %+v", devs)
	}
}

func TestSessionDeadline(t *testing.T) {
	svc, roots := upstream(t)
	sess := start(t, Options{Service: svc, Roots: roots, Bind: loopback, TTL: 50 * time.Millisecond})
	select {
	case <-sess.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("session outlived its deadline")
	}
}

func TestStartRefuses(t *testing.T) {
	svc, roots := upstream(t)
	for name, opts := range map[string]Options{
		"VPN address": {Service: svc, Roots: roots, Bind: netip.MustParseAddr("100.64.0.1"), TTL: time.Hour},
		"public":      {Service: svc, Roots: roots, Bind: netip.MustParseAddr("8.8.8.8"), TTL: time.Hour},
		"no time":     {Service: svc, Roots: roots, Bind: loopback},
	} {
		if sess, err := Start(context.Background(), opts); err == nil {
			sess.Stop()
			t.Errorf("%s: started", name)
		}
	}
}
