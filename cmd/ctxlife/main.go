// Command ctxlife fails on a goroutine that uses its starter's context and outlives the call.
// It reads the directories it is given, the module's root by default: spin names cmd and
// internal, where what it ships is.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/spin-stack/go-tools/internal/ctxlife"
)

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	findings, err := ctxlife.Check(roots...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxlife: %v\n", err)
		os.Exit(2)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Printf("ctxlife: %d goroutine(s) outlive the context they were given\n", len(findings))
		os.Exit(1)
	}
	fmt.Println("OK: every goroutine that outlives its call runs on a context of its own.")
}
