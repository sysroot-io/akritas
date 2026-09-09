package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
)

func main() {
	address := flag.String("address", "127.0.0.1:18081", "HTTP listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": "smoke-model", "object": "model"}},
		})
	})
	fmt.Printf("Smoke upstream: http://%s/v1\n", *address)
	if err := http.ListenAndServe(*address, mux); err != nil {
		panic(err)
	}
}
