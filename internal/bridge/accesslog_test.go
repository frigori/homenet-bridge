package bridge

import (
	"net/url"
	"testing"
)

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
