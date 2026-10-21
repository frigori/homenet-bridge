// Package config loads the list of homenet services a bridge is allowed to share.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// KindJellyfin enables Jellyfin client discovery (UDP 7359) for a service.
const KindJellyfin = "jellyfin"

// Service is one homenet service that can be shared on the local network.
type Service struct {
	Name string `json:"name"`
	// Upstream is the service's URL on the homenet, e.g. https://jellyfin.lan.home.arpa.
	// It is dialed by hostname, so the VPN client on this machine (Twingate, Tailscale)
	// routes it exactly as it would for a browser.
	Upstream string `json:"upstream"`
	// Port is the local port the bridge listens on.
	Port int `json:"port"`
	// Kind selects service-specific helpers ("jellyfin" or empty).
	Kind string `json:"kind,omitempty"`
}

type Config struct {
	// BridgeName is shown to devices (discovery, waiting page). Defaults to the hostname.
	BridgeName string `json:"bridge_name,omitempty"`
	// CAFiles are extra PEM roots trusted for upstreams, on top of the embedded ones.
	CAFiles  []string  `json:"ca_files,omitempty"`
	Services []Service `json:"services"`
}

// Dir is <user config dir>/homenet-bridge. Root CAs (*.pem, *.crt) placed here are trusted.
func Dir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "homenet-bridge")
}

// DefaultPath is <Dir>/config.json.
func DefaultPath() string {
	if Dir() == "" {
		return ""
	}
	return filepath.Join(Dir(), "config.json")
}

// Load reads path. With path empty it reads DefaultPath and treats a missing file as an
// empty config, so the CLI can still run with --upstream alone.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range cfg.Services {
		if err := cfg.Services[i].Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return cfg, nil
}

// Service returns the service called name. With name empty and a single service
// configured, that service is returned.
func (c *Config) Service(name string) (*Service, error) {
	if name == "" {
		if len(c.Services) == 1 {
			return &c.Services[0], nil
		}
		return nil, errors.New("no service given and the config does not have exactly one")
	}
	for i := range c.Services {
		if strings.EqualFold(c.Services[i].Name, name) {
			return &c.Services[i], nil
		}
	}
	return nil, fmt.Errorf("service %q is not in the config", name)
}

func (s *Service) Validate() error {
	if s.Name == "" {
		return errors.New("service without a name")
	}
	u, err := s.UpstreamURL()
	if err != nil {
		return fmt.Errorf("service %s: %w", s.Name, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("service %s: upstream must be http(s), got %q", s.Name, u.Scheme)
	}
	if s.Port <= 0 || s.Port > 65535 {
		return fmt.Errorf("service %s: invalid port %d", s.Name, s.Port)
	}
	if s.Kind != "" && s.Kind != KindJellyfin {
		return fmt.Errorf("service %s: unknown kind %q", s.Name, s.Kind)
	}
	return nil
}

func (s *Service) UpstreamURL() (*url.URL, error) {
	u, err := url.Parse(s.Upstream)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("upstream %q has no host", s.Upstream)
	}
	return u, nil
}
