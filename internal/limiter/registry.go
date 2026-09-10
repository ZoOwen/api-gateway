package limiter

import (
	"context"
	"errors"
	"fmt"
)

// Registry dispatches Allow() to one of several underlying Limiters based
// on Rule.Algo, so different API keys can run different rate-limiting
// algorithms against the same running server at the same time — a request
// for a key with algo=token_bucket and a request for a key with
// algo=sliding_window can land back to back and each gets routed to the
// right implementation. Registry itself implements Limiter too, so it
// drops into the exact same middleware/wiring as any concrete limiter.
type Registry struct {
	limiters    map[string]Limiter
	defaultAlgo string
}

// NewRegistry builds a Registry over limiters (keyed by algo name, e.g.
// "token_bucket" / "sliding_window" — Registry doesn't care what the
// strings mean, only that Rule.Algo values match these keys).
// defaultAlgo is used whenever Rule.Algo is empty.
func NewRegistry(limiters map[string]Limiter, defaultAlgo string) *Registry {
	return &Registry{limiters: limiters, defaultAlgo: defaultAlgo}
}

func (r *Registry) Allow(ctx context.Context, key string, rule Rule) (Decision, error) {
	algo := rule.Algo
	if algo == "" {
		algo = r.defaultAlgo
	}

	l, ok := r.limiters[algo]
	if !ok {
		return Decision{}, fmt.Errorf("limiter: unknown algo %q", algo)
	}

	return l.Allow(ctx, key, rule)
}

// Close closes every limiter in the registry, joining any errors rather
// than stopping at the first one — one backend failing to close shouldn't
// leave the others leaked.
func (r *Registry) Close() error {
	var errs []error
	for _, l := range r.limiters {
		if err := l.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
