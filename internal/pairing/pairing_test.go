package pairing

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPairingFlow(t *testing.T) {
	r := New()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r.Hold = 0
	h := r.Middleware("laptop", nil, ok)

	do := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("192.168.0.23:5555")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("new device: status %d, want 503", rec.Code)
	}
	var req Device
	select {
	case req = <-r.Requests():
	default:
		t.Fatal("no pairing request queued")
	}
	if !strings.Contains(rec.Body.String(), req.Code) {
		t.Errorf("waiting page does not show code %s", req.Code)
	}

	do("192.168.0.23:5556")
	select {
	case <-r.Requests():
		t.Fatal("device queued twice")
	default:
	}

	r.Decide(req.IP, true)
	if rec := do("192.168.0.23:5557"); rec.Code != http.StatusTeapot {
		t.Fatalf("approved device: status %d", rec.Code)
	}

	do("192.168.0.99:1")
	r.Decide(netip.MustParseAddr("192.168.0.99"), false)
	if rec := do("192.168.0.99:2"); rec.Code != http.StatusForbidden {
		t.Fatalf("denied device: status %d", rec.Code)
	}
}

func TestVPNAndPublicAddressesAreNeverLocal(t *testing.T) {
	r := New()
	for _, ip := range []string{"100.96.1.2", "8.8.8.8"} {
		if s, _ := r.Check(netip.MustParseAddr(ip), ""); s != Denied {
			t.Errorf("%s: state %v, want denied", ip, s)
		}
	}
	if len(r.Devices()) != 0 {
		t.Error("non-local address was registered")
	}
}

func TestAllowAndMappedAddresses(t *testing.T) {
	r := New()
	r.Allow(netip.MustParseAddr("192.168.0.5"))
	if s, _ := r.Check(netip.MustParseAddr("::ffff:192.168.0.5"), ""); s != Approved {
		t.Fatalf("state %v, want approved", s)
	}
	select {
	case <-r.Requests():
		t.Fatal("pre-approved device should not ask")
	default:
	}
}

func TestPublicRequestsReachPendingButNotDeniedDevices(t *testing.T) {
	r := New()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	public := func(req *http.Request) bool { return req.URL.Path == "/public" }
	r.Hold = 0
	h := r.Middleware("laptop", public, ok)

	do := func(remote, path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do("192.168.0.23:1", "/public"); code != http.StatusTeapot {
		t.Fatalf("pending device, public path: %d, want it served", code)
	}
	select {
	case <-r.Requests():
	default:
		t.Fatal("a public request must still put the device up for approval")
	}
	if code := do("192.168.0.23:1", "/private"); code != http.StatusServiceUnavailable {
		t.Fatalf("pending device, private path: %d, want 503", code)
	}

	r.Decide(netip.MustParseAddr("192.168.0.23"), false)
	if code := do("192.168.0.23:1", "/public"); code != http.StatusForbidden {
		t.Fatalf("denied device, public path: %d, want 403", code)
	}
	if code := do("100.96.0.1:1", "/public"); code != http.StatusForbidden {
		t.Fatalf("VPN address, public path: %d, want 403", code)
	}
}

func TestPendingRequestWaitsForTheDecision(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	serve := func(r *Registry) <-chan int {
		done := make(chan int, 1)
		go func() {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "192.168.0.23:1"
			rec := httptest.NewRecorder()
			r.Middleware("laptop", nil, ok).ServeHTTP(rec, req)
			done <- rec.Code
		}()
		return done
	}

	for _, c := range []struct {
		name    string
		approve bool
		want    int
	}{
		{"approved while waiting", true, http.StatusTeapot},
		{"denied while waiting", false, http.StatusForbidden},
	} {
		r := New()
		r.Hold = 5 * time.Second
		done := serve(r)
		d := <-r.Requests() // the operator sees the prompt while the request waits
		select {
		case code := <-done:
			t.Fatalf("%s: answered %d before any decision", c.name, code)
		case <-time.After(50 * time.Millisecond):
		}
		r.Decide(d.IP, c.approve)
		select {
		case code := <-done:
			if code != c.want {
				t.Errorf("%s: %d, want %d", c.name, code, c.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: still waiting after the decision", c.name)
		}
	}

	r := New()
	r.Hold = 20 * time.Millisecond
	if code := <-serve(r); code != http.StatusServiceUnavailable {
		t.Errorf("no decision: %d, want 503 after Hold", code)
	}
}
