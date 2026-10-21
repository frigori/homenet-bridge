package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// maxInfoBody bounds the System/Info bodies read into memory; real ones are ~1 KB.
const maxInfoBody = 1 << 20

// isSystemInfo reports whether path (relative to the upstream's base path) is one of
// Jellyfin's server-info endpoints. Jellyfin routes are case-insensitive.
func isSystemInfo(path string) bool {
	path = strings.TrimSuffix(path, "/")
	return strings.EqualFold(path, "/System/Info") || strings.EqualFold(path, "/System/Info/Public")
}

// rewriteLocalAddress replaces LocalAddress in Jellyfin's server info with the bridge's own
// address. Clients use LocalAddress to reconnect; left alone it is the homenet name
// (https://jellyfin.lan.home.arpa), which a TV on someone else's network can neither
// resolve nor verify, so the client would drop the bridge as soon as it read it.
func rewriteLocalAddress(resp *http.Response, upstream *url.URL) error {
	out := resp.Request
	path := strings.TrimPrefix(out.URL.Path, strings.TrimSuffix(upstream.Path, "/"))
	if resp.StatusCode != http.StatusOK || !isSystemInfo(path) ||
		resp.Header.Get("Content-Encoding") != "" ||
		!strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil
	}
	// SetXForwarded copied the Host the device used, e.g. 192.168.1.71:8096.
	host := out.Header.Get("X-Forwarded-Host")
	if host == "" {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInfoBody+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	replaceBody(resp, body)
	if len(body) > maxInfoBody {
		return nil
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(body, &info); err != nil {
		return nil
	}
	if _, ok := info["LocalAddress"]; !ok {
		return nil
	}
	info["LocalAddress"], _ = json.Marshal((&url.URL{Scheme: "http", Host: host}).String())
	rewritten, err := json.Marshal(info)
	if err != nil {
		return nil
	}
	replaceBody(resp, rewritten)
	return nil
}

func replaceBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}
