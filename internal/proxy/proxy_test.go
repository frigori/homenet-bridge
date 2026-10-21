package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxyVerifiesAndForwards(t *testing.T) {
	var gotHost, gotXFF string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotXFF = r.Host, r.Header.Get("X-Forwarded-For")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://"+r.Host+"/web/index.html?x=1", http.StatusFound)
			return
		}
		io.WriteString(w, "hello "+r.URL.Path)
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	roots := up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	front := httptest.NewServer(New(upURL, roots, log.New(io.Discard, "", 0), Options{}))
	defer front.Close()

	resp, err := http.Get(front.URL + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello /System/Info/Public" {
		t.Fatalf("body = %q", body)
	}
	if gotHost != upURL.Host {
		t.Errorf("upstream saw Host %q, want %q", gotHost, upURL.Host)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For not set")
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = noFollow.Get(front.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/web/index.html?x=1" {
		t.Errorf("Location = %q, want relative", loc)
	}
}

func TestProxyRejectsUntrustedUpstream(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	front := httptest.NewServer(New(upURL, nil, log.New(io.Discard, "", 0), Options{}))
	defer front.Close()

	resp, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "cannot reach") {
		t.Fatalf("status %d body %q, want 502", resp.StatusCode, body)
	}
}
