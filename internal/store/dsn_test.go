package store

import "testing"

func TestRequireDatabaseName_Match(t *testing.T) {
	err := RequireDatabaseName("postgres://user:pass@localhost:5432/gateway_test?sslmode=disable", "gateway")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRequireDatabaseName_Mismatch(t *testing.T) {
	err := RequireDatabaseName("postgres://user:pass@localhost:5432/loker_test_db?sslmode=disable", "gateway")
	if err == nil {
		t.Fatal("expected an error for a database name that doesn't contain \"gateway\"")
	}
}

func TestRequireDatabaseName_InvalidDSN(t *testing.T) {
	err := RequireDatabaseName("not a valid dsn", "gateway")
	if err == nil {
		t.Fatal("expected an error for an unparsable DSN")
	}
}
