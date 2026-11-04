package bridge

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// accessLog logs each request once it is done, when on.
func accessLog(on bool, logger *log.Logger, next http.Handler) http.Handler {
	if !on {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		var result string
		switch {
		case rec.status != 0:
			result = strconv.Itoa(rec.status)
		case r.Context().Err() != nil:
			// E.g. a pending device that stopped waiting for approval.
			result = "client gone"
		case r.Header.Get("Upgrade") != "":
			// The proxy writes 101 on the hijacked connection, past the recorder.
			result = "101 upgraded"
		default:
			result = "200"
		}
		logger.Printf("%s %s %s -> %s (%s)", r.RemoteAddr, r.Method, logURI(r.URL), result,
			time.Since(start).Round(time.Millisecond))
	})
}

// secretParams are query parameters that carry a Jellyfin access token, lowercased.
var secretParams = map[string]bool{
	"api_key": true, "apikey": true, "api-key": true, "token": true,
	"x-emby-token": true, "x-mediabrowser-token": true,
}

// logURI is u's path and query with access tokens replaced by REDACTED: Jellyfin clients
// put them in stream and websocket URLs, and the log ends up pasted into chats.
func logURI(u *url.URL) string {
	if u.RawQuery == "" {
		return u.RequestURI()
	}
	params := strings.Split(u.RawQuery, "&")
	for i, p := range params {
		raw, _, ok := strings.Cut(p, "=")
		key, err := url.QueryUnescape(raw)
		if err != nil {
			key = raw
		}
		if ok && secretParams[strings.ToLower(key)] {
			params[i] = raw + "=REDACTED"
		}
	}
	v := *u
	v.RawQuery = strings.Join(params, "&")
	return v.RequestURI()
}

// statusRecorder remembers the status written; 0 means nothing was written through it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and Hijack (streaming, websockets).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
