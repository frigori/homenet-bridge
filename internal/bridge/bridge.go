// Package bridge runs a sharing session: the proxy in front of the upstream, device pairing,
// Jellyfin discovery and the deadline. Front ends (the command line, the control socket
// for the menu bar app, a mobile app) start one with Start and drive it through Session.
package bridge

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/frigori/homenet-bridge/internal/config"
	"github.com/frigori/homenet-bridge/internal/control"
	"github.com/frigori/homenet-bridge/internal/discovery"
	"github.com/frigori/homenet-bridge/internal/netutil"
	"github.com/frigori/homenet-bridge/internal/pairing"
	"github.com/frigori/homenet-bridge/internal/proxy"
)

type Options struct {
	Service *config.Service
	// Roots verify the upstream's certificate.
	Roots *x509.CertPool
	// Info is what Probe returned; discovery replies reuse it.
	Info UpstreamInfo
	// Bind is the LAN address to listen on.
	Bind netip.Addr
	// Name is the bridge name shown to devices.
	Name string
	TTL  time.Duration
	// Allow lists devices approved from the start.
	Allow []netip.Addr
	// Discovery answers Jellyfin discovery on UDP 7359, for Jellyfin services.
	Discovery bool
	// Verbose logs every request.
	Verbose bool
	Logger  *log.Logger
	// Notify gets messages for the person running the bridge, e.g. "Extended: sharing until
	// 22:00.". Nil discards them.
	Notify func(string)
}

// Session is a running share. It implements control.Session.
type Session struct {
	reg    *pairing.Registry
	srv    *http.Server
	ctx    context.Context
	cancel context.CancelFunc
	timer  *time.Timer
	notify func(string)
	// status is fixed at Start; Until and Devices are filled in on read.
	status  control.Status
	changes chan struct{}
	done    chan struct{}
	err     error

	mu       sync.Mutex
	deadline time.Time
}

// Start listens on opts.Bind and shares the service until ctx ends, the deadline passes or
// Stop is called.
func Start(ctx context.Context, opts Options) (*Session, error) {
	if opts.TTL <= 0 {
		return nil, errors.New("the sharing time must be positive")
	}
	if !netutil.IsLocal(opts.Bind) {
		return nil, fmt.Errorf("%s is not a LAN address; refusing to share the homenet beyond the local network", opts.Bind)
	}
	upURL, err := opts.Service.UpstreamURL()
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	notify := opts.Notify
	if notify == nil {
		notify = func(string) {}
	}

	reg := pairing.New()
	for _, ip := range opts.Allow {
		reg.Allow(ip)
	}
	jellyfin := opts.Service.Kind == config.KindJellyfin
	var public func(*http.Request) bool
	if jellyfin {
		public = proxy.JellyfinPublic
	}
	upstream := proxy.New(upURL, opts.Roots, logger, proxy.Options{Jellyfin: jellyfin})
	ln, err := net.Listen("tcp", netip.AddrPortFrom(opts.Bind, uint16(opts.Service.Port)).String())
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		reg:    reg,
		ctx:    ctx,
		cancel: cancel,
		notify: notify,
		srv: &http.Server{
			Handler:           accessLog(opts.Verbose, logger, reg.Middleware(opts.Name, public, upstream)),
			ReadHeaderTimeout: 10 * time.Second,
			ErrorLog:          logger,
		},
		status: control.Status{
			Service:  opts.Service.Name,
			Upstream: upURL.String(),
			Address:  fmt.Sprintf("http://%s", ln.Addr()),
			Bridge:   opts.Name,
		},
		changes:  make(chan struct{}, 1),
		done:     make(chan struct{}),
		deadline: time.Now().Add(opts.TTL),
	}
	s.timer = time.AfterFunc(opts.TTL, cancel)

	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http: %v", err)
			cancel()
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reg.Changes():
				s.changed()
			}
		}
	}()
	if jellyfin && opts.Discovery {
		s.startDiscovery(opts, logger)
	}
	go s.shutdown()
	return s, nil
}

func (s *Session) startDiscovery(opts Options, logger *log.Logger) {
	// Broadcasts only reach a socket bound to the wildcard address.
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", discovery.Port))
	if err != nil {
		s.notify(fmt.Sprintf("Discovery off (%v): enter %s on the device by hand.", err, s.status.Address))
		return
	}
	r := &discovery.Responder{
		HTTPPort:  opts.Service.Port,
		Advertise: opts.Bind,
		ID:        opts.Info.ID,
		Name:      fmt.Sprintf("%s via %s", firstNonEmpty(opts.Info.Name, opts.Service.Name), opts.Name),
		Accept: func(ip netip.Addr) bool {
			st, _ := s.reg.Check(ip, "Jellyfin discovery")
			return st != pairing.Denied
		},
		Logger: logger,
	}
	go func() {
		if err := r.Serve(s.ctx, conn); err != nil {
			logger.Printf("discovery: %v", err)
		}
	}()
	s.notify(fmt.Sprintf("Answering Jellyfin discovery on UDP %d.", discovery.Port))
}

// shutdown stops the server once the session ends, letting requests finish for 5 s.
func (s *Session) shutdown() {
	<-s.ctx.Done()
	s.timer.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.err = s.srv.Shutdown(ctx)
	close(s.done)
}

// Context ends when the session does, before the server finishes shutting down.
func (s *Session) Context() context.Context { return s.ctx }

// Done is closed once the session is over and the server has shut down.
func (s *Session) Done() <-chan struct{} { return s.done }

// Wait blocks until the session is over and returns the server's shutdown error.
func (s *Session) Wait() error {
	<-s.done
	return s.err
}

// Changes signals that Status changed: a device showed up or was decided, or the session
// was extended. Several changes may collapse into one signal.
func (s *Session) Changes() <-chan struct{} { return s.changes }

func (s *Session) changed() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

// Requests delivers each device that starts waiting for approval.
func (s *Session) Requests() <-chan pairing.Device { return s.reg.Requests() }

// Decided is closed once ip is approved or denied.
func (s *Session) Decided(ip netip.Addr) <-chan struct{} { return s.reg.Decided(ip) }

// State is ip's pairing state.
func (s *Session) State(ip netip.Addr) pairing.State {
	st, _ := s.reg.Check(ip, "")
	return st
}

// Until is when the session ends by itself.
func (s *Session) Until() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deadline
}

func (s *Session) Status() control.Status {
	st := s.status
	st.Until = s.Until()
	st.Devices = []control.Device{}
	for _, d := range s.reg.Devices() {
		st.Devices = append(st.Devices, control.Device{
			IP: d.IP.String(), Code: d.Code, State: d.State.String(), Hint: d.Hint, FirstSeen: d.FirstSeen,
		})
	}
	return st
}

func (s *Session) Decide(ip netip.Addr, approve bool) bool { return s.reg.Decide(ip, approve) }

func (s *Session) Extend(d time.Duration) {
	s.mu.Lock()
	if !s.timer.Stop() {
		s.mu.Unlock()
		return // already over
	}
	s.deadline = s.deadline.Add(d)
	s.timer.Reset(time.Until(s.deadline))
	until := s.deadline
	s.mu.Unlock()
	s.notify(fmt.Sprintf("Extended: sharing until %s.", until.Format("15:04")))
	s.changed()
}

func (s *Session) Stop() { s.cancel() }

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
