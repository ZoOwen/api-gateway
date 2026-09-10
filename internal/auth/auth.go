package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/zoowen/gateway/internal/store"
)

type contextKey struct{}

var apiKeyContextKey = contextKey{}

// Lookuper is what auth needs from the key store — *store.Store and
// *store.Cache both satisfy it.
type Lookuper interface {
	Lookup(ctx context.Context, rawKey string) (*store.APIKey, error)
}

// Middleware reads "Authorization: Bearer gw_xxx", resolves it through
// lookup, and stores the *store.APIKey in the request context for
// downstream handlers (see FromContext). Every failure mode — no header,
// a malformed header, an unknown key, and an inactive key — returns the
// exact same 401 body, so a caller can't use the response to tell a dead
// key from one that never existed.
func Middleware(lookup Lookuper) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w)
				return
			}

			key, err := lookup.Lookup(r.Context(), rawKey)
			if err != nil {
				if errors.Is(err, store.ErrKeyNotFound) {
					writeUnauthorized(w)
					return
				}
				log.Printf("auth: lookup error: %v", err)
				writeInternalError(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithAPIKey(r.Context(), key)))
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(h, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}

func writeInternalError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal error"})
}

func WithAPIKey(ctx context.Context, key *store.APIKey) context.Context {
	return context.WithValue(ctx, apiKeyContextKey, key)
}

func FromContext(ctx context.Context) (*store.APIKey, bool) {
	key, ok := ctx.Value(apiKeyContextKey).(*store.APIKey)
	return key, ok
}
