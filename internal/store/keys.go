package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrKeyNotFound is returned by Lookup both when no row matches and when
// the matching row is inactive. See Lookup for why those two cases are
// deliberately indistinguishable.
var ErrKeyNotFound = errors.New("store: api key not found")

type APIKey struct {
	ID          string
	Name        string
	KeyHash     string
	KeyPrefix   string
	Limit       int
	Window      time.Duration
	Burst       int
	Algo        string
	UpstreamURL string
	Active      bool
	CreatedAt   time.Time
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// hashKey hashes a raw API key with SHA-256, not a slow password hash like
// bcrypt. bcrypt's cost is deliberate friction against brute-forcing
// low-entropy human passwords. An API key here is never chosen by a
// person — it's 32 random bytes (256 bits) from GenerateKey — so there's
// no weak-password risk for a slow hash to defend against, and this hash
// runs on every single proxied request: bcrypt's ~100ms would make
// hashing the bottleneck of the whole gateway.
func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

const rowColumns = `id::text, name, key_hash, key_prefix, limit_count, window_ms, burst, algo, upstream_url, active, created_at`

func scanAPIKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	var windowMs int
	if err := row.Scan(&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Limit, &windowMs, &k.Burst, &k.Algo, &k.UpstreamURL, &k.Active, &k.CreatedAt); err != nil {
		return nil, err
	}
	k.Window = time.Duration(windowMs) * time.Millisecond
	return &k, nil
}

// Lookup finds an active API key by its raw (un-hashed) value. A row that
// exists but is inactive returns the exact same ErrKeyNotFound as a row
// that doesn't exist at all — callers (the auth middleware) must not be
// able to tell a revoked key from one that never existed.
func (s *Store) Lookup(ctx context.Context, rawKey string) (*APIKey, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+rowColumns+` FROM api_keys WHERE key_hash = $1 AND active`, hashKey(rawKey))
	key, err := scanAPIKey(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("store: lookup: %w", err)
	}
	return key, nil
}

type CreateParams struct {
	Name        string
	Limit       int
	Window      time.Duration
	Burst       int
	Algo        string // defaults to "token_bucket" if empty
	UpstreamURL string
}

// Create generates a new raw key, stores only its hash and prefix, and
// returns both the created row and the raw key. The raw key is never
// persisted — this is the only time it's available, so the caller must
// show it now or it's gone.
//
// Algo is validated here, not just left to the database's CHECK constraint
// (migrations/002_algo_check.sql): Registry actually dispatches on this
// value now (internal/limiter/registry.go), so a typo here silently kills
// the key at request time instead of failing loudly at creation time.
func (s *Store) Create(ctx context.Context, p CreateParams) (*APIKey, string, error) {
	algo := p.Algo
	if algo == "" {
		algo = "token_bucket"
	}
	if algo != "token_bucket" && algo != "sliding_window" {
		return nil, "", fmt.Errorf("store: invalid algo %q: must be one of: token_bucket, sliding_window", algo)
	}

	raw, err := GenerateKey()
	if err != nil {
		return nil, "", fmt.Errorf("store: generating key: %w", err)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (name, key_hash, key_prefix, limit_count, window_ms, burst, algo, upstream_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+rowColumns,
		p.Name, hashKey(raw), keyPrefix(raw), p.Limit, p.Window.Milliseconds(), p.Burst, algo, p.UpstreamURL,
	)
	key, err := scanAPIKey(row)
	if err != nil {
		return nil, "", fmt.Errorf("store: create: %w", err)
	}
	return key, raw, nil
}

func (s *Store) List(ctx context.Context) ([]*APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+rowColumns+` FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list: %w", err)
	}
	return keys, nil
}

// Revoke deactivates a key by id. It does not delete the row — Lookup
// already treats an inactive row as not-found, and keeping the row means
// the key's history (name, when it was created) survives revocation.
func (s *Store) Revoke(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET active = false WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrKeyNotFound
	}
	return nil
}
