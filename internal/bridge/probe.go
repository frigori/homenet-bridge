package bridge

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/frigori/homenet-bridge/internal/config"
	"github.com/frigori/homenet-bridge/internal/proxy"
)

// UpstreamInfo is a Jellyfin server's public info.
type UpstreamInfo struct {
	Name    string `json:"ServerName"`
	ID      string `json:"Id"`
	Version string `json:"Version"`
}

// Describe is e.g. ` (Jellyfin "home" 10.11.0)`, or "" for other services.
func (i UpstreamInfo) Describe() string {
	if i.Name == "" {
		return ""
	}
	return fmt.Sprintf(" (Jellyfin %q %s)", i.Name, i.Version)
}

// Probe checks the upstream over the same TLS settings the proxy uses. For Jellyfin it also
// reads the server's public info, reused in discovery replies.
func Probe(svc *config.Service, roots *x509.CertPool) (UpstreamInfo, error) {
	u, err := svc.UpstreamURL()
	if err != nil {
		return UpstreamInfo{}, err
	}
	tr := proxy.New(u, roots, log.New(io.Discard, "", 0), proxy.Options{}).Transport
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}

	path := "/"
	if svc.Kind == config.KindJellyfin {
		path = "/System/Info/Public"
	}
	resp, err := client.Get(u.JoinPath(path).String())
	if err != nil {
		return UpstreamInfo{}, err
	}
	defer resp.Body.Close()
	var info UpstreamInfo
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
