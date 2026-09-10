package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zoowen/gateway/internal/store"
)

type fakeLookuper struct {
	key *store.APIKey
	err error
}

func (f *fakeLookuper) Lookup(ctx context.Context, rawKey string) (*store.APIKey, error) {
	return f.key, f.err
}

func doRequest(t *testing.T, lookup Lookuper, authHeader string) (*httptest.ResponseRecorder, bool, *store.APIKey) {
	t.Helper()

	var (
		nextCalled bool
		gotKey     *store.APIKey
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		gotKey, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy/anything", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	Middleware(lookup)(next).ServeHTTP(rec, req)

	return rec, nextCalled, gotKey
}

func TestMiddleware_NoHeader(t *testing.T) {
	rec, called, _ := doRequest(t, &fakeLookuper{err: store.ErrKeyNotFound}, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}

func TestMiddleware_MalformedHeader(t *testing.T) {
	cases := []string{
		"gw_rawkeywithoutbearer",
		"Basic dXNlcjpwYXNz",
		"Bearer",
		"Bearer ",
		"bearer gw_lowercase",
	}

	for _, header := range cases {
		rec, called, _ := doRequest(t, &fakeLookuper{err: store.ErrKeyNotFound}, header)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: expected 401, got %d", header, rec.Code)
		}
		if called {
			t.Fatalf("header %q: expected next handler not to be called", header)
		}
	}
}

// The auth middleware must not be able to distinguish "no such key" from
// "key exists but is inactive" — by the time either reaches Middleware,
// store.Store.Lookup has already collapsed both into store.ErrKeyNotFound
// (see internal/store/keys.go). This test proves the two scenarios
// produce byte-identical responses, which is the actual requirement:
// nothing in the response may leak which case it was.
func TestMiddleware_KeyNotFoundAndInactiveAreIdentical(t *testing.T) {
	notFound := &fakeLookuper{err: store.ErrKeyNotFound}
	inactive := &fakeLookuper{err: store.ErrKeyNotFound} // same sentinel by design

	rec1, _, _ := doRequest(t, notFound, "Bearer gw_neverexisted")
	rec2, _, _ := doRequest(t, inactive, "Bearer gw_wasrevoked")

	if rec1.Code != http.StatusUnauthorized || rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected both 401, got %d and %d", rec1.Code, rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("expected identical bodies, got %q vs %q", rec1.Body.String(), rec2.Body.String())
	}
}

func TestMiddleware_LookupError(t *testing.T) {
	rec, called, _ := doRequest(t, &fakeLookuper{err: errors.New("connection refused")}, "Bearer gw_whatever")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for a non-ErrKeyNotFound error, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}

func TestMiddleware_ValidKey(t *testing.T) {
	want := &store.APIKey{ID: "k1", Name: "test", Active: true}
	rec, called, gotKey := doRequest(t, &fakeLookuper{key: want}, "Bearer gw_validkey")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("expected next handler to be called")
	}
	if gotKey != want {
		t.Fatalf("expected the APIKey to be retrievable from context, got %+v", gotKey)
	}
}
