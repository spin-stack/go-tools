// Command taskgate fails on a workflow step that runs anything but one task, so what CI does is
// written once, in the Taskfile, and a developer runs the same thing:
//
//	go tool taskgate [-allow file] [dir]
//
// -allow names the steps that are not a task on purpose, one `<file>/<job>/<step>  # why` a line
// (a composite action's without the job); a line that matches no step fails too.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/taskgate"
)

func main() {
	allowPath := flag.String("allow", "", "steps that are not a task on purpose, `<file>/<job>/<step>  # why` a line (none by default)")
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
	findings, err := taskgate.Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	unexplained, stale := allow.Split(allowed, findings, taskgate.Finding.Key)
	for _, f := range unexplained {
		fmt.Println(f)
	}
	for _, k := range stale {
		fmt.Printf("%s is in %s and is no step that is not a task: remove the line\n", k, *allowPath)
	}
	if len(unexplained)+len(stale) > 0 {
		fmt.Printf("taskgate: %d step(s) to make a task, %d stale exemption(s)\n", len(unexplained), len(stale))
		os.Exit(1)
	}
	fmt.Printf("OK: every workflow step runs a task (%d exempt).\n", len(allowed))
}
