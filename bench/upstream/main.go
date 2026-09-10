// Command upstream is a minimal dummy backend for benchmarking the
// gateway against — it always returns the same small static JSON body, so
// benchmark numbers measure the gateway's overhead, not a real backend's
// variance. Benchmarking against the internet (e.g. jsonplaceholder)
// mostly measures the internet.
package main

import (
	"log"
	"net/http"
	"os"
)

var body = []byte(`{"status":"ok","from":"dummy-upstream"}`)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	log.Printf("dummy upstream listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
