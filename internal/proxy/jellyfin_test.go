package proxy

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const publicInfo = `{"LocalAddress":"https://jellyfin.lan.home.arpa","ServerName":"jellyfin","Version":"10.11.6","Id":"808a5d56"}`

// fakeJellyfin serves publicInfo on the info endpoints, gzipped when the client accepts it,
// as Jellyfin's response compression does.
func fakeJellyfin(t *testing.T) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSystemInfo(r.URL.Path) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<a href="https://jellyfin.lan.home.arpa">LocalAddress</a>`)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			io.WriteString(gz, publicInfo)
			gz.Close()
			return
		}
		io.WriteString(w, publicInfo)
	}))
}

func get(t *testing.T, rawURL string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", rawURL, nil)
	// Set by hand, as browsers do: Go's client then leaves the body as it arrives.
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestLocalAddressPointsBackToTheBridge(t *testing.T) {
	up := fakeJellyfin(t)
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	roots := up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	front := httptest.NewServer(New(upURL, roots, log.New(io.Discard, "", 0), Options{Jellyfin: true}))
	defer front.Close()
	frontHost := strings.TrimPrefix(front.URL, "http://")

	for _, path := range []string{"/System/Info/Public", "/system/info/public", "/System/Info"} {
		resp, body := get(t, front.URL+path)
		if enc := resp.Header.Get("Content-Encoding"); enc != "" {
			t.Fatalf("%s: Content-Encoding %q, want plain JSON", path, enc)
		}
		var info map[string]string
		if err := json.Unmarshal(body, &info); err != nil {
			t.Fatalf("%s: %v (body %q)", path, err, body)
		}
		if info["LocalAddress"] != "http://"+frontHost {
			t.Errorf("%s: LocalAddress = %q, want http://%s", path, info["LocalAddress"], frontHost)
		}
		if info["ServerName"] != "jellyfin" || info["Id"] != "808a5d56" || info["Version"] != "10.11.6" {
			t.Errorf("%s: other fields changed: %v", path, info)
		}
		if resp.ContentLength != int64(len(body)) {
			t.Errorf("%s: Content-Length %d, body %d bytes", path, resp.ContentLength, len(body))
		}
	}

	// Other responses pass through untouched, compression included.
	resp, _ := get(t, front.URL+"/web/index.html")
	if resp.Header.Get("Content-Type") != "text/html" {
		t.Errorf("non-info response altered: %v", resp.Header)
	}
}

func TestLocalAddressUntouchedWithoutJellyfinOption(t *testing.T) {
	up := fakeJellyfin(t)
	defer up.Close()
	upURL, _ := url.Parse(up.URL)
	roots := up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	front := httptest.NewServer(New(upURL, roots, log.New(io.Discard, "", 0), Options{}))
	defer front.Close()

	resp, body := get(t, front.URL+"/System/Info/Public")
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(gz)
	}
	if string(body) != publicInfo {
		t.Errorf("body = %q, want it unchanged", body)
	}
}
