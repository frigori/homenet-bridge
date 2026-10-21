package pairing

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPairingFlow(t *testing.T) {
	r := New()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := r.Middleware("laptop", ok)

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
