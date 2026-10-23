// homenet-bridge shares a homenet service, reached through this machine's VPN (Twingate,
// Tailscale, ...), with devices on the local network that cannot run the VPN themselves.
package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/frigori/homenet-bridge/internal/awake"
	"github.com/frigori/homenet-bridge/internal/config"
	"github.com/frigori/homenet-bridge/internal/discovery"
	"github.com/frigori/homenet-bridge/internal/netutil"
	"github.com/frigori/homenet-bridge/internal/pairing"
	"github.com/frigori/homenet-bridge/internal/proxy"
	"github.com/frigori/homenet-bridge/internal/trust"
)

var version = "dev"

const usage = `homenet-bridge shares a homenet service with devices on this local network.

Usage:
  homenet-bridge share [flags] [service]   start a time-limited sharing session
  homenet-bridge check [flags] [service]   only check that the upstream is reachable
  homenet-bridge trust add <root-ca.crt>   trust your homenet's root CA (public certificate)
  homenet-bridge version

Run "homenet-bridge share -h" for the flags.
`

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "share":
		err = cmdShare(os.Args[2:], false)
	case "check":
		err = cmdShare(os.Args[2:], true)
	case "trust":
		err = cmdTrust(os.Args[2:])
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdShare(args []string, checkOnly bool) error {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config file (default "+config.DefaultPath()+")")
	upstream := fs.String("upstream", "", "upstream URL, instead of a service from the config")
	port := fs.Int("port", 8096, "local port, with --upstream")
	kind := fs.String("kind", config.KindJellyfin, `service kind, with --upstream ("jellyfin" or "")`)
	ttl := fs.Duration("for", 4*time.Hour, "how long to share before stopping by itself")
	bind := fs.String("bind", "", "LAN address to listen on (default: this machine's LAN address)")
	name := fs.String("name", "", "bridge name shown to devices (default: hostname)")
	noDiscovery := fs.Bool("no-discovery", false, "do not answer Jellyfin discovery on UDP 7359")
	verbose := fs.Bool("verbose", false, "log every request (device, method, path, status, time)")
	var allow, caFiles multiFlag
	fs.Var(&allow, "allow", "pre-approve a device IP (repeatable)")
	fs.Var(&caFiles, "ca-file", "extra PEM root to trust for the upstream (repeatable)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	svc, err := pickService(cfg, fs.Arg(0), *upstream, *port, *kind)
	if err != nil {
		return err
	}
	upURL, _ := svc.UpstreamURL()

	dirFiles, err := trust.DirFiles(config.Dir())
	if err != nil {
		return err
	}
	roots, added, err := trust.Pool(append(append(dirFiles, cfg.CAFiles...), caFiles...))
	if err != nil {
		return err
	}
	fmt.Printf("Trusting the system roots + %d extra root(s).\n", added)

	info, err := probe(svc, roots)
	if err != nil {
		hint := "Check that the VPN (Twingate, Tailscale) is connected."
		if added == 0 {
			hint += "\nIf this machine does not trust your homenet root CA, add it with \"homenet-bridge trust add <root-ca.crt>\"."
		}
		return fmt.Errorf("%s is not reachable from this machine: %w\n%s", upURL.Host, err, hint)
	}
	fmt.Printf("Upstream %s OK%s.\n", upURL.Host, info.describe())
	if checkOnly {
		return nil
	}
	if *ttl <= 0 {
		return errors.New("--for must be positive")
	}

	bridgeName := firstNonEmpty(*name, cfg.BridgeName, hostname())
	bindAddr, err := pickBind(*bind)
	if err != nil {
		return err
	}

	reg := pairing.New()
	for _, a := range allow {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return fmt.Errorf("--allow %q: %w", a, err)
		}
		reg.Allow(ip)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *ttl)
	defer cancel()

	logger := log.Default()
	jellyfin := svc.Kind == config.KindJellyfin
	var public func(*http.Request) bool
	if jellyfin {
		public = proxy.JellyfinPublic
	}
	upstreamProxy := proxy.New(upURL, roots, logger, proxy.Options{Jellyfin: jellyfin})
	srv := &http.Server{
		Handler:           accessLog(*verbose, logger, reg.Middleware(bridgeName, public, upstreamProxy)),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          logger,
	}
	ln, err := net.Listen("tcp", netip.AddrPortFrom(bindAddr, uint16(svc.Port)).String())
	if err != nil {
		return err
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http: %v", err)
			cancel()
		}
	}()

	if svc.Kind == config.KindJellyfin && !*noDiscovery {
		startDiscovery(ctx, reg, svc, bindAddr, info, bridgeName, logger)
	}
	if release, ok := awake.Hold("sharing " + svc.Name); ok {
		defer release()
	}

	fmt.Printf("\nSharing %s as http://%s:%d until %s (Ctrl-C to stop).\n",
		svc.Name, bindAddr, svc.Port, time.Now().Add(*ttl).Format("15:04"))
	fmt.Println("New devices must be approved here; their screen shows the same pairing code.")

	go console(ctx, reg)
	<-ctx.Done()

	fmt.Println("\nStopping: sharing session over.")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	return srv.Shutdown(shutCtx)
}

func cmdTrust(args []string) error {
	if len(args) != 2 || args[0] != "add" {
		return errors.New(`usage: homenet-bridge trust add <root-ca.crt>`)
	}
	dest, fps, err := trust.Install(args[1], config.Dir())
	if err != nil {
		return err
	}
	fmt.Printf("Trusted for homenet upstreams: %s\n", dest)
	for _, fp := range fps {
		fmt.Printf("SHA-256 %s\n", fp)
	}
	fmt.Println("Compare it with the fingerprint printed when the CA was created.")
	return nil
}

// accessLog logs each request once it is done, when on.
func accessLog(on bool, logger *log.Logger, next http.Handler) http.Handler {
	if !on {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Printf("%s %s %s -> %d (%s)", r.RemoteAddr, r.Method, r.URL.RequestURI(), rec.status,
			time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush and Hijack (streaming, websockets).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func pickService(cfg *config.Config, name, upstream string, port int, kind string) (*config.Service, error) {
	if upstream == "" {
		return cfg.Service(name)
	}
	svc := &config.Service{Name: firstNonEmpty(name, kind, "service"), Upstream: upstream, Port: port, Kind: kind}
	return svc, svc.Validate()
}

func pickBind(flagValue string) (netip.Addr, error) {
	if flagValue == "" {
		ip, err := netutil.DefaultLANAddr()
		if err != nil {
			return netip.Addr{}, err
		}
		if all, _ := netutil.LANAddrs(); len(all) > 1 {
			fmt.Printf("Several LAN addresses %v; using %s (choose with --bind).\n", all, ip)
		}
		return ip, nil
	}
	ip, err := netip.ParseAddr(flagValue)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("--bind: %w", err)
	}
	if !netutil.IsLocal(ip) {
		return netip.Addr{}, fmt.Errorf("--bind %s is not a LAN address; refusing to share the homenet beyond the local network", ip)
	}
	return ip, nil
}

func startDiscovery(ctx context.Context, reg *pairing.Registry, svc *config.Service, bindAddr netip.Addr,
	info upstreamInfo, bridgeName string, logger *log.Logger) {
	// Broadcasts only reach a socket bound to the wildcard address.
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", discovery.Port))
	if err != nil {
		fmt.Printf("Discovery off (%v): enter http://%s:%d on the device by hand.\n", err, bindAddr, svc.Port)
		return
	}
	r := &discovery.Responder{
		HTTPPort:  svc.Port,
		Advertise: bindAddr,
		ID:        info.ID,
		Name:      fmt.Sprintf("%s via %s", firstNonEmpty(info.Name, svc.Name), bridgeName),
		Accept: func(ip netip.Addr) bool {
			s, _ := reg.Check(ip, "Jellyfin discovery")
			return s != pairing.Denied
		},
		Logger: logger,
	}
	go func() {
		if err := r.Serve(ctx, conn); err != nil {
			logger.Printf("discovery: %v", err)
		}
	}()
	fmt.Printf("Answering Jellyfin discovery on UDP %d.\n", discovery.Port)
}

// console asks the operator about each new device on stdin.
func console(ctx context.Context, reg *pairing.Registry) {
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	for {
		var d pairing.Device
		select {
		case <-ctx.Done():
			return
		case d = <-reg.Requests():
		}
		hint := ""
		if d.Hint != "" {
			hint = " (" + d.Hint + ")"
		}
		if lines == nil {
			fmt.Printf("Device %s%s wants access, code %s: no terminal to approve it, restart with --allow %s.\n",
				d.IP, hint, d.Code, d.IP)
			continue
		}
		fmt.Printf("\nDevice %s%s wants access. Pairing code %s. Allow? [y/N] ", d.IP, hint, d.Code)
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				lines = nil
				fmt.Println("\nstdin closed; new devices can only be pre-approved with --allow.")
				continue
			}
			yes := strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
			reg.Decide(d.IP, yes)
			if yes {
				fmt.Printf("Allowed %s until the session ends.\n", d.IP)
			} else {
				fmt.Printf("Denied %s.\n", d.IP)
			}
		}
	}
}

type upstreamInfo struct {
	Name    string `json:"ServerName"`
	ID      string `json:"Id"`
	Version string `json:"Version"`
}

func (i upstreamInfo) describe() string {
	if i.Name == "" {
		return ""
	}
	return fmt.Sprintf(" (Jellyfin %q %s)", i.Name, i.Version)
}

// probe checks the upstream over the same TLS settings the proxy uses. For Jellyfin it also
// reads the server's public info, reused in discovery replies.
func probe(svc *config.Service, roots *x509.CertPool) (upstreamInfo, error) {
	u, _ := svc.UpstreamURL()
	tr := proxy.New(u, roots, log.Default(), proxy.Options{}).Transport
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	path := "/"
	if svc.Kind == config.KindJellyfin {
		path = "/System/Info/Public"
	}
	resp, err := client.Get(u.JoinPath(path).String())
	if err != nil {
		return upstreamInfo{}, err
	}
	defer resp.Body.Close()
	var info upstreamInfo
	if svc.Kind == config.KindJellyfin {
		if resp.StatusCode != http.StatusOK {
			return info, fmt.Errorf("%s answered %s", path, resp.Status)
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return info, fmt.Errorf("%s: not a Jellyfin server? %w", path, err)
		}
	}
	return info, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "homenet-bridge"
	}
	return strings.TrimSuffix(h, ".local")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
