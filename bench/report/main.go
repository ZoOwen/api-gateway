// Command report turns one or more k6 --summary-export JSON files into a
// markdown table for bench/RESULTS.md, so the numbers in that file come
// straight from k6's own output instead of being retyped by hand.
//
//	go run ./bench/report "label=path/to/summary.json" ...
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type metric struct {
	Count  float64 `json:"count"`
	Rate   float64 `json:"rate"`
	Med    float64 `json:"med"`
	P95    float64 `json:"p(95)"`
	P99    float64 `json:"p(99)"`
	Value  float64 `json:"value"`
	Passes float64 `json:"passes"`
	Fails  float64 `json:"fails"`
}

type summary struct {
	Metrics map[string]metric `json:"metrics"`
}

func loadSummary(path string) (*summary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s summary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &s, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: report "label=path/to/summary.json" ...`)
		os.Exit(1)
	}

	fmt.Println("| Scenario | req/s | p50 (ms) | p95 (ms) | p99 (ms) | allowed | denied | error rate |")
	fmt.Println("|---|---|---|---|---|---|---|---|")

	for _, arg := range os.Args[1:] {
		label, path, ok := strings.Cut(arg, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "skipping malformed argument %q (want label=path)\n", arg)
			continue
		}

		s, err := loadSummary(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", label, err)
			continue
		}

		reqs := s.Metrics["http_reqs"]
		dur := s.Metrics["http_req_duration"]
		checks := s.Metrics["checks"]
		allowed := s.Metrics["scenario_allowed"]
		denied := s.Metrics["scenario_denied"]
		unexpected := s.Metrics["scenario_unexpected"]

		total := checks.Passes + checks.Fails
		errorRate := 0.0
		if total > 0 {
			errorRate = unexpected.Count / total * 100
		}

		fmt.Printf("| %s | %.0f | %.2f | %.2f | %.2f | %.0f | %.0f | %.2f%% |\n",
			label, reqs.Rate, dur.Med, dur.P95, dur.P99, allowed.Count, denied.Count, errorRate)
	}
}
