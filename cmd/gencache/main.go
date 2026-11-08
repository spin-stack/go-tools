// Command gencache is go generate for the packages it is given, with each `go tool mockgen`
// directive answered from a cache when nothing its mock depends on has changed since it ran.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spin-stack/go-tools/internal/gencache"
)

func main() { os.Exit(run()) }

func run() int {
	tags := flag.String("tags", "", "build tags, as go generate takes them")
	dir := flag.String("cache", "", "the cache's directory (default: the user's cache directory, go-tools/gencache)")
	// Half the machine: each mockgen loads its packages with go list and the type checker, which
	// are parallel already.
	jobs := flag.Int("jobs", max(1, runtime.GOMAXPROCS(0)/2), "packages generated side by side; 1 is go generate's order")
	flag.Parse()

	if *dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "gencache: %v: name a directory with -cache\n", err)
			return 2
		}
		*dir = filepath.Join(base, "go-tools", "gencache")
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gencache: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := gencache.Run(ctx, gencache.Options{
		Dir: wd, Tags: *tags, Patterns: flag.Args(), Cache: *dir, Jobs: *jobs, Out: os.Stdout,
	})
	fmt.Println(summary(res))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gencache: %v\n", err)
		return 1
	}
	return 0
}

func summary(res gencache.Result) string {
	s := fmt.Sprintf("gencache: %d directives: %d from the cache", res.Directives, res.FromCache)
	if res.Rewritten > 0 {
		s += fmt.Sprintf(" (%d rewritten)", res.Rewritten)
	}
	s += fmt.Sprintf(", %d run", res.Ran)
	notKeyed := 0
	reasons := make([]string, 0, len(res.NotKeyed))
	for reason, n := range res.NotKeyed {
		notKeyed += n
		reasons = append(reasons, fmt.Sprintf("%d %s", n, reason))
	}
	if notKeyed > 0 {
		sort.Strings(reasons)
		s += fmt.Sprintf(", %d run every time (%s)", notKeyed, strings.Join(reasons, "; "))
	}
	return s
}
