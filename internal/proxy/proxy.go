// Package proxy forwards plain-HTTP requests from the local network to an HTTPS homenet
// upstream. TLS (and its private CA) ends on this machine, so LAN devices that cannot
// install a CA, such as TVs, never see it.
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// New returns a reverse proxy to upstream that verifies it against roots.
func New(upstream *url.URL, roots *x509.CertPool, logger *log.Logger) *httputil.ReverseProxy {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	// Media streams are long; Twingate and Tailscale already keep the tunnel alive.
	tr.ResponseHeaderTimeout = 0

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// SetURL also sets Host to the upstream's, which is what Traefik routes on.
			pr.SetURL(upstream)
			pr.SetXForwarded()
		},
		Transport: tr,
		// Flush immediately: video segments and websocket-like long polls.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			rewriteLocation(resp, upstream)
			return nil
		},
		ErrorLog: logger,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Printf("upstream %s: %v", upstream.Host, err)
			http.Error(w, fmt.Sprintf("homenet-bridge: cannot reach %s (is the VPN connected?)", upstream.Host),
				http.StatusBadGateway)
		},
	}
}

// rewriteLocation turns absolute redirects to the upstream into relative ones, otherwise
// the device would follow them to a name it cannot resolve or verify.
func rewriteLocation(resp *http.Response, upstream *url.URL) {
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil || !u.IsAbs() || u.Host != upstream.Host {
		return
	}
	rel := u.EscapedPath()
	if rel == "" {
		rel = "/"
	}
	if u.RawQuery != "" {
		rel += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		rel += "#" + u.EscapedFragment()
	}
	resp.Header.Set("Location", rel)
}
