// homenet-bridge shares a homenet service, reached through this machine's VPN (Twingate,
// Tailscale, ...), with devices on the local network that cannot run the VPN themselves.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/frigori/homenet-bridge/internal/awake"
	"github.com/frigori/homenet-bridge/internal/bridge"
	"github.com/frigori/homenet-bridge/internal/config"
	"github.com/frigori/homenet-bridge/internal/control"
	"github.com/frigori/homenet-bridge/internal/netutil"
	"github.com/frigori/homenet-bridge/internal/pairing"
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
	controlPath := fs.String("control", control.DefaultPath(config.Dir()),
		`control socket for the menu bar app ("" for none)`)
	var allow, caFiles multiFlag
	fs.Var(&allow, "allow", "pre-approve a device IP (repeatable)")
	fs.Var(&caFiles, "ca-file", "extra PEM root to trust for the upstream (repeatable)")
	service, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	svc, err := pickService(cfg, service, *upstream, *port, *kind)
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

	info, err := bridge.Probe(svc, roots)
	if err != nil {
		hint := "Check that the VPN (Twingate, Tailscale) is connected."
		if added == 0 {
			hint += "\nIf this machine does not trust your homenet root CA, add it with \"homenet-bridge trust add <root-ca.crt>\"."
		}
		return fmt.Errorf("%s is not reachable from this machine: %w\n%s", upURL.Host, err, hint)
	}
	fmt.Printf("Upstream %s OK%s.\n", upURL.Host, info.Describe())
	if checkOnly {
		return nil
	}
	if *ttl <= 0 {
		return errors.New("--for must be positive")
	}

	bindAddr, err := pickBind(*bind)
	if err != nil {
		return err
	}
	var allowed []netip.Addr
	for _, a := range allow {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return fmt.Errorf("--allow %q: %w", a, err)
		}
		allowed = append(allowed, ip)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.Default()
	sess, err := bridge.Start(ctx, bridge.Options{
		Service:   svc,
		Roots:     roots,
		Info:      info,
		Bind:      bindAddr,
		Name:      firstNonEmpty(*name, cfg.BridgeName, hostname()),
		TTL:       *ttl,
		Allow:     allowed,
		Discovery: !*noDiscovery,
		Verbose:   *verbose,
		Logger:    logger,
		Notify:    func(msg string) { fmt.Println(msg) },
	})
	if err != nil {
		return err
	}

	var ctl *control.Server
	if *controlPath != "" {
		if cln, err := control.Listen(*controlPath); err != nil {
			fmt.Printf("Menu bar control off: %v.\n", err)
		} else {
			ctl = &control.Server{Session: sess, Logger: logger}
			go func() {
				if err := ctl.Serve(sess.Context(), cln); err != nil {
					logger.Printf("control: %v", err)
				}
			}()
			go func() {
				for {
					select {
					case <-sess.Context().Done():
						return
					case <-sess.Changes():
						ctl.Changed()
					}
				}
			}()
		}
	}
	if release, ok := awake.Hold("sharing " + svc.Name); ok {
		defer release()
	}

	fmt.Printf("\nSharing %s as %s until %s (Ctrl-C to stop).\n",
		svc.Name, sess.Status().Address, sess.Until().Format("15:04"))
	if ctl != nil {
		fmt.Println("New devices must be approved here or in the menu bar app; their screen shows the same pairing code.")
	} else {
		fmt.Println("New devices must be approved here; their screen shows the same pairing code.")
	}

	go console(sess, ctl != nil)
	<-sess.Done()
	fmt.Println("\nStopping: sharing session over.")
	return sess.Wait()
}

// parseArgs parses flags before and after the optional service name: the flag package
// alone stops at the first non-flag, silently ignoring "share jellyfin --for 1h".
func parseArgs(fs *flag.FlagSet, args []string) (service string, err error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() == 0 {
		return "", nil
	}
	service = fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected arguments after %q: %s", service, strings.Join(fs.Args(), " "))
	}
	return service, nil
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
	return ip, nil
}

// console asks the operator about each new device on stdin. With a front end attached
// (menu bar app) a device may be decided there instead, which ends the question here.
func console(sess *bridge.Session, frontEnd bool) {
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
		case <-sess.Context().Done():
			return
		case d = <-sess.Requests():
		}
		hint := ""
		if d.Hint != "" {
			hint = " (" + d.Hint + ")"
		}
		if lines == nil {
			if frontEnd {
				fmt.Printf("Device %s%s wants access, code %s: approve it in the menu bar app.\n", d.IP, hint, d.Code)
			} else {
				fmt.Printf("Device %s%s wants access, code %s: no terminal to approve it, restart with --allow %s.\n",
					d.IP, hint, d.Code, d.IP)
			}
			continue
		}
		fmt.Printf("\nDevice %s%s wants access. Pairing code %s. Allow? [y/N] ", d.IP, hint, d.Code)
		select {
		case <-sess.Context().Done():
			return
		case <-sess.Decided(d.IP):
			fmt.Printf("%s from the menu bar app.\n", map[pairing.State]string{
				pairing.Approved: "Allowed", pairing.Denied: "Denied"}[sess.State(d.IP)])
		case line, ok := <-lines:
			if !ok {
				lines = nil
				if frontEnd {
					fmt.Println("\nNo terminal input: approve devices in the menu bar app.")
				} else {
					fmt.Println("\nstdin closed; new devices can only be pre-approved with --allow.")
				}
				continue
			}
			yes := strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
			sess.Decide(d.IP, yes)
			if yes {
				fmt.Printf("Allowed %s until the session ends.\n", d.IP)
			} else {
				fmt.Printf("Denied %s.\n", d.IP)
			}
		}
	}
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
