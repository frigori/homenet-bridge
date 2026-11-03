package main

import (
	"flag"
	"io"
	"net/url"
	"testing"
	"time"
)

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args    []string
		service string
		ttl     time.Duration
		wantErr bool
	}{
		{[]string{"jellyfin", "--for", "1h"}, "jellyfin", time.Hour, false},
		{[]string{"--for", "2h", "jellyfin"}, "jellyfin", 2 * time.Hour, false},
		{[]string{"--for", "30m"}, "", 30 * time.Minute, false},
		{[]string{"jellyfin"}, "jellyfin", 4 * time.Hour, false},
		{[]string{"jellyfin", "immich"}, "", 0, true},
	} {
		fs := flag.NewFlagSet("share", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		ttl := fs.Duration("for", 4*time.Hour, "")
		service, err := parseArgs(fs, c.args)
		if c.wantErr {
			if err == nil {
				t.Errorf("%v: no error", c.args)
			}
			continue
		}
		if err != nil || service != c.service || *ttl != c.ttl {
			t.Errorf("%v: service %q, for %v, err %v; want %q, %v", c.args, service, *ttl, err, c.service, c.ttl)
		}
	}
}

func TestLogURI(t *testing.T) {
	for in, want := range map[string]string{
		"/System/Info/Public":                           "/System/Info/Public",
		"/Videos/abc/stream?api_key=s3cret&Static=true": "/Videos/abc/stream?api_key=REDACTED&Static=true",
		"/socket?ApiKey=s3cret&deviceId=tv":             "/socket?ApiKey=REDACTED&deviceId=tv",
		"/x?X-Emby-Token=a&token=b&Token=c":             "/x?X-Emby-Token=REDACTED&token=REDACTED&Token=REDACTED",
		"/x?api%5Fkey=s3cret":                           "/x?api%5Fkey=REDACTED",
		"/Items?SearchTerm=token&Limit=5":               "/Items?SearchTerm=token&Limit=5",
	} {
		u, err := url.ParseRequestURI(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := logURI(u); got != want {
			t.Errorf("logURI(%q) = %q, want %q", in, got, want)
		}
	}
}
