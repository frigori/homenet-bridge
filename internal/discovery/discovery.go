// Package discovery answers Jellyfin client discovery on UDP 7359, so TV and phone apps on
// the LAN list the bridge as a server without anyone typing an address.
//
// Clients broadcast "who is JellyfinServer?" and expect a JSON reply carrying the server's
// address, id and name, the same reply a Jellyfin server sends.
package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"

	"github.com/frigori/homenet-bridge/internal/netutil"
)

const (
	Port  = 7359
	query = "who is jellyfinserver?"
)

type Reply struct {
	Address         string  `json:"Address"`
	ID              string  `json:"Id"`
	Name            string  `json:"Name"`
	EndpointAddress *string `json:"EndpointAddress"`
}

// IsQuery reports whether a datagram is a Jellyfin discovery request.
func IsQuery(b []byte) bool {
	return bytes.Contains(bytes.ToLower(b), []byte(query))
}

type Responder struct {
	// HTTPPort is the bridge's port, advertised in the reply.
	HTTPPort int
	// Advertise, if valid, is the address to advertise. Otherwise the address used to reach
	// each client is computed per client.
	Advertise netip.Addr
	ID, Name  string
	// Accept filters who gets an answer, and is where pairing learns about new devices.
	Accept func(netip.Addr) bool
	Logger *log.Logger
}

// Serve answers queries on conn until ctx is done.
func (r *Responder) Serve(ctx context.Context, conn net.PacketConn) error {
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 1024)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if !IsQuery(buf[:n]) {
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		peer := ua.AddrPort().Addr().Unmap()
		if r.Accept != nil && !r.Accept(peer) {
			continue
		}
		reply, err := r.reply(peer)
		if err != nil {
			r.Logger.Printf("discovery: %s: %v", peer, err)
			continue
		}
		if _, err := conn.WriteTo(reply, from); err != nil {
			r.Logger.Printf("discovery: reply to %s: %v", peer, err)
		}
	}
}

func (r *Responder) reply(peer netip.Addr) ([]byte, error) {
	addr := r.Advertise
	if !addr.IsValid() || addr.IsUnspecified() {
		var err error
		if addr, err = localAddrFor(peer); err != nil {
			return nil, err
		}
	}
	return json.Marshal(Reply{
		Address: fmt.Sprintf("http://%s", netip.AddrPortFrom(addr, uint16(r.HTTPPort))),
		ID:      r.ID,
		Name:    r.Name,
	})
}

// localAddrFor is a variable so tests do not depend on the host's routes.
var localAddrFor = netutil.LocalAddrFor
