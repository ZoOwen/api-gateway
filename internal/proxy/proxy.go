package proxy

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// UpstreamFunc resolves the upstream target for a request. It lets the
// proxy route to a different backend per request (e.g. per API key,
// looked up from context by the caller) instead of being locked to one
// fixed target picked at construction time.
type UpstreamFunc func(r *http.Request) (*url.URL, error)

// Static returns an UpstreamFunc that always resolves to the same target —
// the single-tenant behavior, used when there's no per-request upstream to
// look up (e.g. no database configured).
func Static(target *url.URL) UpstreamFunc {
	return func(r *http.Request) (*url.URL, error) {
		return target, nil
	}
}

// newTransport clones http.DefaultTransport rather than building one from
// scratch, so dial timeouts, TLS handshake timeout, HTTP/2, proxy-from-env,
// etc. all stay whatever the standard library considers reasonable
// defaults — only the idle-connection-pool sizing changes. The default
// transport's MaxIdleConnsPerHost is 2 (http.DefaultMaxIdleConnsPerHost);
// left unset, that's 2 idle connections to the upstream shared across
// however many concurrent requests the gateway is handling, so under real
// concurrency almost every request opens a fresh connection instead of
// reusing one. Confirmed via CPU profile (net.(*netFD).connect showing up
// disproportionately) before changing this — see bench/RESULTS.md.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 200
	t.MaxIdleConnsPerHost = 200
	t.IdleConnTimeout = 90 * time.Second
	return t
}

func New(upstream UpstreamFunc) http.Handler {
	rp := &httputil.ReverseProxy{
		Transport: newTransport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			target, err := upstream(pr.In)
			if err != nil {
				// Leave pr.Out.URL as the zero-ish clone of the inbound
				// URL (no scheme/host): the RoundTrip that follows fails
				// immediately and lands in ErrorHandler below, which
				// already answers with the standard "upstream unavailable"
				// 502. This path only exists as a defensive fallback —
				// with auth running before this in the chain, upstream()
				// should never actually fail.
				log.Printf("proxy: resolving upstream: %v", err)
				return
			}

			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, "/proxy")
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}

			// SetURL rewrites scheme/host and joins the (now-stripped)
			// path onto target's, and sets Out.Host = "" so the outbound
			// Host header follows Out.URL.Host — i.e. the upstream's host,
			// not the gateway's.
			pr.SetURL(target)

			if host, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
				pr.Out.Header.Set("X-Forwarded-For", host)
			} else {
				pr.Out.Header.Set("X-Forwarded-For", pr.In.RemoteAddr)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %v", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream unavailable"})
		},
	}

	return rp
}
