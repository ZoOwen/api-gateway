package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func doAdminRequest(t *testing.T, token, authHeader string) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	var called bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/stats", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	AdminMiddleware(token)(next).ServeHTTP(rec, req)

	return rec, called
}

func TestAdminMiddleware_ValidToken(t *testing.T) {
	rec, called := doAdminRequest(t, "supersecret", "Bearer supersecret")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Fatal("expected next handler to be called")
	}
}

func TestAdminMiddleware_WrongToken(t *testing.T) {
	rec, called := doAdminRequest(t, "supersecret", "Bearer wrongtoken")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}

func TestAdminMiddleware_NoHeader(t *testing.T) {
	rec, called := doAdminRequest(t, "supersecret", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}

// An unset ADMIN_TOKEN must not mean "accept anything" — it must reject
// every request, including one with an empty bearer token that would
// otherwise "match" an empty configured token.
func TestAdminMiddleware_EmptyConfiguredTokenAlwaysRejects(t *testing.T) {
	rec, called := doAdminRequest(t, "", "Bearer ")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}

	rec, called = doAdminRequest(t, "", "Bearer anything")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}

// An API key is not an admin token, even though both are Bearer tokens
// checked in similar-looking middleware — AdminMiddleware must not accept
// something just because it looks like a plausible credential of the
// other kind.
func TestAdminMiddleware_APIKeyShapedTokenRejectedIfNotConfiguredToken(t *testing.T) {
	rec, called := doAdminRequest(t, "supersecret", "Bearer gw_someapikeyvalue")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("expected next handler not to be called")
	}
}
