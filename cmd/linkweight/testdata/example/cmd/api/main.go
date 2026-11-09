// Command api serves the shop's front page.
package main

import (
	"net/http"

	"example.com/shop/internal/page"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _ = page.Front(w, "the shop") })
	_ = http.ListenAndServe("localhost:8080", nil)
}
