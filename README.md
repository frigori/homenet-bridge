# homenet-bridge

Let a device that can't run your VPN, like a TV in a holiday rental, use a service on your
home network, by borrowing the connection of a device that can: your laptop.

```
TV ──HTTP──▶ laptop:8096 ──HTTPS (your root CA)──▶ jellyfin.lan.home.arpa ──VPN──▶ home
```

You're on holiday, the guest house has a smart TV, and you want to watch Jellyfin. You don't
want to sign the TV into Twingate (or Tailscale), and the TV can't trust your homelab CA
anyway. Your laptop is already on the VPN, so homenet-bridge turns it into a small,
temporary, opt-in relay for **one service**, for **approved devices**, for **a limited time**.

## How it works

- **Application-level proxy.** TLS ends on the laptop, which checks the upstream against
  the system roots plus your homenet root CA (embedded at build time, or `--ca-file`). The
  upstream is dialed **by hostname**, so the laptop's VPN client routes it as it would for a
  browser, and your reverse proxy (Traefik, ...) sees the right Host and SNI.
- **Pairing.** A new device gets `503` and a 4-digit code. The laptop terminal shows the
  same code and asks you to approve it. Approvals last only for the session. Only LAN
  addresses (RFC 1918, link-local, loopback) can pair. CGNAT addresses (Twingate,
  Tailscale) never can, so the bridge can't reshare your homenet onto the VPN.
- **Time limit.** `--for 4h` (default). The bridge stops by itself and keeps the laptop
  awake until then (`caffeinate` on macOS, `systemd-inhibit` on Linux).
- **Jellyfin discovery.** Answers `who is JellyfinServer?` on UDP 7359, so the Jellyfin app
  on the TV lists "*Your server* via *laptop*" without anyone typing an IP. A device asking
  for discovery also starts pairing, so the approval prompt shows up early.

## Quick start

```sh
# 1. Embed your homenet root CA (public certificate only):
cp ~/.local/share/homelab-ca/homelab-root-ca.crt internal/trust/roots/
make build          # no local Go: see the comment at the top of the Makefile

# 2. Describe the services you're willing to share (or pass --config):
#    macOS: ~/Library/Application Support/homenet-bridge/config.json
#    Linux: ~/.config/homenet-bridge/config.json
mkdir -p ~/Library/Application\ Support/homenet-bridge
cp config.example.json ~/Library/Application\ Support/homenet-bridge/config.json

# 3. With the VPN connected:
bin/homenet-bridge check jellyfin
bin/homenet-bridge share jellyfin --for 3h
```

No config file? `bin/homenet-bridge share --upstream https://jellyfin.lan.home.arpa` works too.

On the TV, open the Jellyfin app: the server shows up by itself (or enter the address the
bridge prints, e.g. `http://192.168.0.42:8096`). Approve the device on the laptop, then
retry on the TV.

**Sign in with Quick Connect**, not your password: the TV ↔ laptop leg is plain HTTP on
someone else's Wi-Fi. Quick Connect shows a code on the TV that you approve from your phone,
so your password never crosses that network.

## Flags

| Flag | Default | |
|---|---|---|
| `--for` | `4h` | session length |
| `--bind` | the LAN address of the default route | address to listen on; must be a LAN address |
| `--allow IP` | | pre-approve a device (repeatable), e.g. when there is no terminal |
| `--ca-file` | | extra root CA (repeatable) |
| `--name` | hostname | name shown to devices |
| `--no-discovery` | | don't answer UDP 7359 |
| `--upstream`, `--port`, `--kind` | | share a service without a config file |

## Limits and caveats

- **Guest Wi-Fi with client isolation** blocks device-to-device traffic, and then nothing
  works. Use a cable, or a network you control.
- **Pairing is by IP.** On a hostile LAN, anyone who takes over an approved device's IP
  gets through. Keep sessions short, and rely on the service's own login (Jellyfin requires
  one).
- **HTTP only on the LAN leg.** See Quick Connect above.
- **Not every service works through a proxy.** Apps that build absolute URLs from their own
  hostname (Nextcloud, Immich, ...) need more work. Jellyfin is the tested target.
- **Bandwidth.** Every byte crosses the laptop twice. 1080p is fine; 4K remux depends on
  the Wi-Fi.
- You're lending your VPN access to a device. Only share what you'd share in person.

## Roadmap

- [ ] Tray app with an approve/deny notification
- [ ] Device identity by MAC on top of IP
- [ ] More service presets, with Host/URL rewriting
- [ ] Windows testing

## Development

```sh
make test vet
make dist     # darwin, linux and windows binaries in dist/
```

## License

MIT
