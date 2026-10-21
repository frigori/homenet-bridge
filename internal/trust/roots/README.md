# Embedded roots

Put your homenet's root CA here (public certificate only, `*.pem` or `*.crt`) before
building. It is embedded in the binary and used, together with the system roots, to verify
upstreams such as `https://jellyfin.lan.home.arpa`.

Everything in this folder except this README is git-ignored. Never put a private key here:
the bridge only needs the public certificate, and a key in this folder would end up inside
every binary you build.
