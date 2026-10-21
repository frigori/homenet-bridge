package discovery

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestIsQuery(t *testing.T) {
	for _, s := range []string{"who is JellyfinServer?", "Who is JellyfinServer?\n"} {
		if !IsQuery([]byte(s)) {
			t.Errorf("%q not recognised", s)
		}
	}
	if IsQuery([]byte("M-SEARCH * HTTP/1.1")) {
		t.Error("SSDP recognised as Jellyfin")
	}
}

func TestResponder(t *testing.T) {
	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var asked []netip.Addr
	r := &Responder{
		HTTPPort: 8096,
		ID:       "abc",
		Name:     "Home via laptop",
		Accept:   func(ip netip.Addr) bool { asked = append(asked, ip); return true },
		Logger:   log.New(io.Discard, "", 0),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, srv) }()

	cli, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cli.WriteTo([]byte("ignored"), srv.LocalAddr())
	cli.WriteTo([]byte("who is JellyfinServer?"), srv.LocalAddr())

	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, _, err := cli.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	var got Reply
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatal(err)
	}
	if got.Address != "http://127.0.0.1:8096" || got.ID != "abc" || got.Name != "Home via laptop" {
		t.Errorf("reply = %+v", got)
	}
	if len(asked) != 1 || asked[0] != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("Accept called with %v", asked)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
