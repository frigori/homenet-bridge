// Package netutil picks the address the bridge listens on and decides which peers count
// as "the local network".
package netutil

import (
	"errors"
	"net"
	"net/netip"
)

// IsLocal reports whether ip is on a LAN-style network: RFC 1918 / ULA, link-local or
// loopback. CGNAT (100.64.0.0/10), used by Twingate and Tailscale, is deliberately not
// local: the bridge must never re-share the homenet back onto the VPN.
func IsLocal(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}

// LANAddrs lists the private IPv4 addresses of the interfaces that are up.
func LANAddrs() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr()
			if ip.Is4() && ip.IsPrivate() {
				out = append(out, ip)
			}
		}
	}
	return out, nil
}

// DefaultLANAddr returns the private address of the default route if it has one,
// otherwise the first private address found.
func DefaultLANAddr() (netip.Addr, error) {
	addrs, err := LANAddrs()
	if err != nil {
		return netip.Addr{}, err
	}
	if len(addrs) == 0 {
		return netip.Addr{}, errors.New("no private IPv4 address on any interface (not on a LAN?)")
	}
	// No packet is sent: connecting a UDP socket only asks the kernel for a route.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if ap, err := netip.ParseAddrPort(c.LocalAddr().String()); err == nil {
			for _, a := range addrs {
				if a == ap.Addr() {
					return a, nil
				}
			}
		}
	}
	return addrs[0], nil
}

// LocalAddrFor returns the address this host uses to reach peer, i.e. the one to
// advertise to it.
func LocalAddrFor(peer netip.Addr) (netip.Addr, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(peer, 9)))
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap(), nil
}
