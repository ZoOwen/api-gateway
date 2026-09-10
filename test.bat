@echo off
rem Postgres runs on the host (local install, not Docker), so "localhost"
rem in .env's GATEWAY_TEST_DATABASE_URL means nothing from inside this
rem container — it resolves to the container itself. host.docker.internal
rem reaches the host instead; this override only applies to this
rem containerized run, .env keeps "localhost" for the normal (non-Docker)
rem `go test`.
docker run --rm -v "%cd%":/app -v gomodcache:/go/pkg/mod -v gobuildcache:/root/.cache/go-build -e GATEWAY_TEST_DATABASE_URL=postgres://postgres:12345678@host.docker.internal:5432/gateway_test?sslmode=disable -w /app golang:1.25 go test -race ./...