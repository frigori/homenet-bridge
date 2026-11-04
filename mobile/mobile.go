// Package mobile is the bridge for mobile apps, bound with gomobile:
//
//	gomobile bind -target=android -androidapi 24 ./mobile
//
// It runs one sharing session at a time and reports to a Listener the app implements.
// Status is JSON in the control socket's format (internal/control.Status), so the app reads
// the same fields as the macOS menu bar app. Types and signatures stay within what gomobile
// can bind: strings, ints, bools, errors, interfaces and pointers to structs.
package mobile

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/frigori/homenet-bridge/internal/bridge"
	"github.com/frigori/homenet-bridge/internal/config"
	"github.com/frigori/homenet-bridge/internal/control"
	"github.com/frigori/homenet-bridge/internal/trust"
)

// Listener is implemented by the app. Calls come from Go goroutines, not the main thread.
type Listener interface {
	// OnStatus gets the session's status as JSON after every change.
	OnStatus(status string)
	// OnRequest is called when a device starts waiting for approval: show its code and offer
	// Allow and Deny (e.g. in a notification).
	OnRequest(ip, code, hint string)
	// OnMessage gets messages for the person running the bridge.
	OnMessage(msg string)
	// OnStopped is called once the session is over; err is "" unless it failed.
	OnStopped(err string)
}

// Config describes a session. Create it with NewConfig, which fills in the defaults.
type Config struct {
	// Upstream is the service's homenet URL, e.g. https://jellyfin.lan.home.arpa.
	Upstream string
	// Kind is "jellyfin" or "" for other services.
	Kind string
	// Name is the service name shown in the app.
	Name string
	Port int
	// Bind is the phone's LAN address (Wi-Fi or hotspot), from the app: Android does not let
	// Go list the network interfaces.
	Bind string
	// BridgeName is what devices see, e.g. the phone's name.
	BridgeName string
	Minutes    int
	// Discovery answers Jellyfin discovery on UDP 7359. The app must hold a multicast lock.
	Discovery bool
	// Verbose logs every request to logcat.
	Verbose bool
}

func NewConfig() *Config {
	return &Config{Kind: config.KindJellyfin, Name: "jellyfin", Port: 8096, Minutes: 240, Discovery: true}
}

var (
	mu      sync.Mutex
	dataDir string
	current *bridge.Session
)

// SetDataDir sets where trusted roots are kept: the app's files directory.
func SetDataDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	dataDir = dir
}

func rootsDir() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if dataDir == "" {
		return "", errors.New("SetDataDir was not called")
	}
	return filepath.Join(dataDir, "roots"), nil
}

// TrustCA adds the root CA certificates in the PEM file at path (e.g. a copy of the file the
// user picked) and returns their SHA-256 fingerprints, one per line, to show for comparison.
func TrustCA(path string) (string, error) {
	dir, err := rootsDir()
	if err != nil {
		return "", err
	}
	_, fps, err := trust.Install(path, dir)
	if err != nil {
		return "", err
	}
	return strings.Join(fps, "\n"), nil
}

// TrustedCAs is the number of root certificates added with TrustCA.
func TrustedCAs() int {
	dir, err := rootsDir()
	if err != nil {
		return 0
	}
	files, _ := trust.DirFiles(dir)
	_, n, err := trust.Pool(files)
	if err != nil {
		return 0
	}
	return n
}

func service(c *Config) (*config.Service, error) {
	if c == nil {
		return nil, errors.New("no config")
	}
	svc := &config.Service{Name: c.Name, Upstream: c.Upstream, Port: c.Port, Kind: c.Kind}
	return svc, svc.Validate()
}

// roots is the system roots plus those added with TrustCA.
func roots() (*x509.CertPool, error) {
	dir, err := rootsDir()
	if err != nil {
		return nil, err
	}
	files, err := trust.DirFiles(dir)
	if err != nil {
		return nil, err
	}
	pool, _, err := trust.Pool(files)
	return pool, err
}

// Check reaches the upstream through the VPN and describes it, e.g. `Jellyfin "home" 10.11.0`.
func Check(c *Config) (string, error) {
	svc, err := service(c)
	if err != nil {
		return "", err
	}
	info, _, err := probe(svc)
	if err != nil {
		return "", err
	}
	if info.Name == "" {
		return "reachable", nil
	}
	return fmt.Sprintf("Jellyfin %q %s", info.Name, info.Version), nil
}

// probe checks the upstream with the trusted roots, explaining a failure for the app.
func probe(svc *config.Service) (bridge.UpstreamInfo, *x509.CertPool, error) {
	pool, err := roots()
	if err != nil {
		return bridge.UpstreamInfo{}, nil, err
	}
	info, err := bridge.Probe(svc, pool)
	if err != nil {
		return info, nil, unreachable(svc, err)
	}
	return info, pool, nil
}

func unreachable(svc *config.Service, err error) error {
	u, _ := svc.UpstreamURL()
	hint := "Check that Twingate is connected."
	if errors.As(err, new(x509.UnknownAuthorityError)) {
		hint = "The phone does not trust its certificate: add your homenet root CA first."
	}
	return fmt.Errorf("%s is not reachable from this phone: %w. %s", u.Host, err, hint)
}

// Start checks the upstream and starts sharing. It fails if a session is already running.
func Start(c *Config, l Listener) error {
	if l == nil {
		return errors.New("no listener")
	}
	svc, err := service(c)
	if err != nil {
		return err
	}
	bind, err := netip.ParseAddr(c.Bind)
	if err != nil {
		return fmt.Errorf("phone address %q: %w", c.Bind, err)
	}
	if c.Minutes <= 0 {
		return errors.New("the sharing time must be positive")
	}
	if _, err := session(); err == nil {
		return errors.New("already sharing")
	}
	info, pool, err := probe(svc)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()
	if current != nil {
		return errors.New("already sharing")
	}
	sess, err := bridge.Start(context.Background(), bridge.Options{
		Service:   svc,
		Roots:     pool,
		Info:      info,
		Bind:      bind,
		Name:      c.BridgeName,
		TTL:       time.Duration(c.Minutes) * time.Minute,
		Discovery: c.Discovery,
		Verbose:   c.Verbose,
		Logger:    log.New(os.Stderr, "homenet-bridge: ", log.Ltime),
		Notify:    l.OnMessage,
	})
	if err != nil {
		return err
	}
	current = sess
	go report(sess, l)
	return nil
}

// report forwards the session's changes and requests to l until it ends.
func report(sess *bridge.Session, l Listener) {
	l.OnStatus(statusJSON(sess.Status()))
	for {
		select {
		case <-sess.Context().Done():
			err := sess.Wait()
			mu.Lock()
			if current == sess {
				current = nil
			}
			mu.Unlock()
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			l.OnStopped(msg)
			return
		case <-sess.Changes():
			l.OnStatus(statusJSON(sess.Status()))
		case d := <-sess.Requests():
			l.OnRequest(d.IP.String(), d.Code, d.Hint)
		}
	}
}

func statusJSON(st control.Status) string {
	b, _ := json.Marshal(st)
	return string(b)
}

func session() (*bridge.Session, error) {
	mu.Lock()
	defer mu.Unlock()
	if current == nil {
		return nil, errors.New("not sharing")
	}
	return current, nil
}

// Status is the running session's status as JSON, or "" when not sharing.
func Status() string {
	sess, err := session()
	if err != nil {
		return ""
	}
	return statusJSON(sess.Status())
}

// Decide approves or denies a device.
func Decide(ip string, allow bool) error {
	sess, err := session()
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return err
	}
	if !sess.Decide(addr, allow) {
		return fmt.Errorf("no device %s", ip)
	}
	return nil
}

// Extend moves the end of the session by minutes, up to 12 hours at a time.
func Extend(minutes int) error {
	sess, err := session()
	if err != nil {
		return err
	}
	d := time.Duration(minutes) * time.Minute
	if d <= 0 || d > control.MaxExtend {
		return fmt.Errorf("minutes must be between 1 and %d", int(control.MaxExtend.Minutes()))
	}
	sess.Extend(d)
	return nil
}

// Stop ends the session; the listener's OnStopped follows.
func Stop() {
	if sess, err := session(); err == nil {
		sess.Stop()
	}
}
