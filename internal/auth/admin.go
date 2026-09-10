package auth

import (
	"crypto/subtle"
	"net/http"
)

// AdminMiddleware protects operational endpoints (/admin/*, /debug/pprof/*)
// with a single static bearer token from ADMIN_TOKEN. This is deliberately
// separate from Middleware/Lookuper: it doesn't touch the store, doesn't
// get cached, doesn't carry a rate limit — it's an operator credential,
// not an API key. An empty configured token always rejects, so a missing
// ADMIN_TOKEN doesn't silently leave these endpoints open.
func AdminMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			given, ok := bearerToken(r)
			if !ok || token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				writeUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
