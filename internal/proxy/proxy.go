package proxy

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func New(upstreamURL *url.URL) http.Handler {
	rp := httputil.NewSingleHostReverseProxy(upstreamURL)

	originalDirector := rp.Director
	rp.Director = func(r *http.Request) {
		originalDirector(r)

		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/proxy")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}

		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			r.Header.Set("X-Forwarded-For", host)
		} else {
			r.Header.Set("X-Forwarded-For", r.RemoteAddr)
		}

		r.Host = upstreamURL.Host
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "upstream unavailable"})
	}

	return rp
}
