// Package pairing keeps the list of LAN devices allowed through the bridge.
//
// A device is identified by its IP address. The first time it shows up (HTTP request or
// discovery packet) it becomes pending and gets a short code; the person running the bridge
// approves or denies it. Nothing is persisted: approvals end with the sharing session.
package pairing

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/frigori/homenet-bridge/internal/netutil"
)

type State int

const (
	Pending State = iota
	Approved
	Denied
)

func (s State) String() string {
	return [...]string{"pending", "approved", "denied"}[s]
}

type Device struct {
	IP        netip.Addr
	Code      string
	State     State
	FirstSeen time.Time
	// Hint is what we know about the device, e.g. its User-Agent.
	Hint string

	queued bool
}

type Registry struct {
	mu       sync.Mutex
	devices  map[netip.Addr]*Device
	requests chan Device
	now      func() time.Time
}

func New() *Registry {
	return &Registry{
		devices:  map[netip.Addr]*Device{},
		requests: make(chan Device, 16),
		now:      time.Now,
	}
}

// Requests delivers each new pending device once, for the operator to decide on.
func (r *Registry) Requests() <-chan Device { return r.requests }

// Allow pre-approves ip.
func (r *Registry) Allow(ip netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(ip.Unmap(), "")
	d.State, d.queued = Approved, true
}

// Decide approves or denies ip. It returns false if ip never showed up.
func (r *Registry) Decide(ip netip.Addr, approve bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[ip.Unmap()]
	if !ok {
		return false
	}
	if approve {
		d.State = Approved
	} else {
		d.State = Denied
	}
	return true
}

// Check returns ip's state, registering it as pending the first time it is seen.
// Non-local addresses are always denied and never registered.
func (r *Registry) Check(ip netip.Addr, hint string) (State, string) {
	ip = ip.Unmap()
	if !netutil.IsLocal(ip) {
		return Denied, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.get(ip, hint)
	if d.Hint == "" {
		d.Hint = hint
	}
	if d.State == Pending && !d.queued {
		select {
		case r.requests <- *d:
			d.queued = true
		default:
			// Operator queue full: try again on the device's next request.
		}
	}
	return d.State, d.Code
}

// Devices returns a snapshot sorted by first contact.
func (r *Registry) Devices() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out
}

func (r *Registry) get(ip netip.Addr, hint string) *Device {
	d, ok := r.devices[ip]
	if !ok {
		d = &Device{IP: ip, Code: newCode(), State: Pending, FirstSeen: r.now(), Hint: hint}
		r.devices[ip] = d
	}
	return d
}

func newCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%04d", n.Int64())
}

// Middleware lets approved devices through to next and turns everybody else away.
// bridgeName is shown on the waiting page so the person at the TV knows where to look.
// Requests for which public returns true also reach next from pending devices: clients
// check a server's public info before listing it, and would drop the bridge before anyone
// had the chance to approve them. public may be nil.
func (r *Registry) Middleware(bridgeName string, public func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ap, err := netip.ParseAddrPort(req.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		state, code := r.Check(ap.Addr(), req.UserAgent())
		switch {
		case state == Approved, state == Pending && public != nil && public(req):
			next.ServeHTTP(w, req)
			return
		}
		switch state {
		case Pending:
			w.Header().Set("Retry-After", "5")
			http.Error(w, fmt.Sprintf(
				"homenet-bridge: this device is waiting for approval on %q.\nPairing code: %s\nApprove it there, then retry.",
				bridgeName, code), http.StatusServiceUnavailable)
		default:
			http.Error(w, "homenet-bridge: this device is not allowed", http.StatusForbidden)
		}
	})
}
