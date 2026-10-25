// Command taskgate fails on a workflow step that runs anything but one task, on a task that holds
// a program rather than a few commands, and on a script a task runs that is a program - so what
// CI does is written once, in the Taskfile, a developer runs the same thing, and what decides
// anything is Go with a test:
//
//	go tool taskgate [-allow file] [-max-lines n] [-max-script-lines n] [dir]
//
// -allow names what is allowed on purpose, one `<key>  # why` a line: a step as
// `<file>/<job>/<step>` (a composite action's without the job), a Taskfile's block as
// `<file>:<task>`, a script by its path. A line that matches nothing fails too.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/taskgate"
)

func main() {
	allowPath := flag.String("allow", "", "what is allowed on purpose, `<key>  # why` a line (none by default)")
	maxLines := flag.Int("max-lines", 12, "lines of shell a task's own cmd or sh may hold; 0 is any")
	maxScript := flag.Int("max-script-lines", 40, "lines of shell a script a task runs may hold; 0 is any")
	flag.Parse()
	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	allowed, err := allow.Read(*allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "taskgate: %v\n", err)
		os.Exit(2)
	}
	findings, err := taskgate.Check(root, taskgate.Limits{Block: *maxLines, Script: *maxScript})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	unexplained, stale := allow.Split(allowed, findings, taskgate.Finding.Key)
	for _, f := range unexplained {
		fmt.Println(f)
	}
	for _, k := range stale {
		fmt.Printf("%s is in %s and is no finding: remove the line\n", k, *allowPath)
	}
	if len(unexplained)+len(stale) > 0 {
		fmt.Printf("taskgate: %d finding(s), %d stale exemption(s)\n", len(unexplained), len(stale))
		os.Exit(1)
	}
	fmt.Printf("OK: every workflow step runs a task, and every task is commands (%d exempt).\n", len(allowed))
}
