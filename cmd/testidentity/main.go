// Command testidentity fails on a commit of the branch - every one in BASE..HEAD - whose author
// or committer is one of the identities a test uses: an address at a domain reserved for testing,
// one with no dot, or one the tree's tests and fixtures spell. A test that wrote git's author into
// a checkout's config makes every commit from it, every worktree's, that test's; this is what
// stops those commits before they are pushed.
//
//	go tool testidentity [-base origin/main] [-allow file] [dir]
//
// -allow names an address that reads as a test's and is somebody's real author, one
// `<address>  # why` a line; a line whose address is not a test identity any more fails too.
// GitHub's own addresses - its Apps' and users' noreply, and noreply@github.com - need no line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/spin-stack/go-tools/internal/testidentity"
)

func main() {
	base := flag.String("base", "origin/main", "the commit the branch's own commits start after")
	allowPath := flag.String("allow", "", "addresses that are somebody's and read as a test's, `<address>  # why` a line (none by default)")
	flag.Parse()
	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	err := testidentity.Run(context.Background(), root, *base, *allowPath, os.Stdout)
	switch {
	case errors.Is(err, testidentity.ErrFound):
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "testidentity: %v\n", err) //nolint:errcheck // the exit status is the answer
		os.Exit(2)
	}
}
