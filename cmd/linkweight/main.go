// Command linkweight says what each of a repository's binaries links and why: the bytes each
// module puts in it, whether the linker could drop the methods nothing calls and which function
// stopped it, which import a refactor would cut to drop the most, and what the binary can reach -
// exec, listen, dial, mount, setuid. With -allow it is a gate on the last two.
//
//	go tool linkweight [-roots ./cmd/...] [-skip ./cmd/x,...] [-tags t] [-top 15] [-json out.json] [-allow file]
//
// Each main package under -roots is built with -ldflags=-dumpdep, and the answer is read from the
// linker's own reachability graph and the binary's symbol table: what the binary has, after
// inlining, build tags and the linker's dead-code pass, not what a static call graph supposes.
// It holds for the GOOS/GOARCH, tags and CGO_ENABLED it runs with, which should be the release's.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spin-stack/go-tools/internal/allow"
	"github.com/spin-stack/go-tools/internal/linkweight"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) // mutate-exempt: the process's own arguments; run is what the test drives
}

// run answers the exit status: 0 reported (and with -allow, every finding explained), 1 a finding
// or a line unexplained, 2 no answer.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("linkweight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	roots := fs.String("roots", "./...", "the pattern every binary is under")
	skip := fs.String("skip", "", "main packages that are no binary of the report, comma-separated as ./cmd/x")
	tags := fs.String("tags", "", "build tags, as the release builds with them")
	top := fs.Int("top", 15, "the modules and cuts listed for each binary")
	jsonPath := fs.String("json", "", "write the whole report here, for comparing two revisions")
	allowPath := fs.String("allow", "", "gate: `<root> capability|reflect-method <name>  # why` a line; a finding no line explains fails, and a line no finding matches")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var skipped []string
	if *skip != "" {
		skipped = strings.Split(*skip, ",")
	}
	// The list first: a line with no reason is refused before minutes of linking.
	var allowed map[string]bool
	if *allowPath != "" {
		var err error
		if allowed, err = allow.Read(*allowPath); err != nil {
			_, _ = fmt.Fprintln(stderr, "linkweight:", err)
			return 2
		}
	}
	bins, err := measure(*roots, skipped, *tags, stderr)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "linkweight:", err)
		return 2
	}
	linkweight.Write(stdout, bins, *top)
	if *jsonPath != "" {
		data, err := json.MarshalIndent(bins, "", "  ")
		if err == nil {
			err = os.WriteFile(*jsonPath, append(data, '\n'), 0o644) //nolint:gosec // a report the caller reads, not a secret
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "linkweight:", err)
			return 2
		}
	}
	if allowed == nil {
		return 0
	}
	v := linkweight.Judge(bins, allowed)
	v.Write(stdout, *allowPath)
	if !v.OK() {
		return 1
	}
	return 0
}

// measure builds and reads every root, one at a time: a binary's linker graph is hundreds of
// megabytes of text and the next one waits for its memory.
func measure(rootsPattern string, skipped []string, tags string, progress io.Writer) ([]linkweight.Binary, error) {
	module, err := sh("go", "list", "-m")
	if err != nil {
		return nil, err
	}
	module = strings.TrimSpace(module)
	mains, err := sh("go", "list", "-tags", tags, "-f", `{{if eq .Name "main"}}{{.ImportPath}}{{end}}`, rootsPattern)
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, m := range strings.Fields(mains) {
		rel := strings.TrimPrefix(strings.TrimPrefix(m, module), "/")
		if !slices.Contains(skipped, "./"+rel) {
			roots = append(roots, m)
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no main package under %s", rootsPattern)
	}
	dir, err := os.MkdirTemp("", "linkweight")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	var bins []linkweight.Binary
	for _, root := range roots {
		_, _ = fmt.Fprintln(progress, "linkweight: linking", root)
		b, err := one(root, module, tags, dir)
		if err != nil {
			return nil, err
		}
		bins = append(bins, b)
	}
	return bins, nil
}

// one is a root's report: built with the linker's graph on, its packages listed, both read.
func one(root, module, tags, dir string) (linkweight.Binary, error) {
	bin := filepath.Join(dir, path.Base(root))
	depsPath := bin + ".deps"
	deps, err := os.Create(depsPath) //nolint:gosec // a file in our own temporary directory
	if err != nil {
		return linkweight.Binary{}, err
	}
	defer func() { _ = deps.Close() }()
	// -dumpdep writes the graph to standard error, which is where the build's own errors go too:
	// a failed build is told by its exit status, and its message is the lines that are no edge.
	cmd := exec.Command("go", "build", "-trimpath", "-tags", tags, "-ldflags=-dumpdep", "-o", bin, root) //nolint:gosec // go, over the caller's own module
	cmd.Stderr = deps
	if err := cmd.Run(); err != nil {
		return linkweight.Binary{}, fmt.Errorf("go build %s: %w: %s", root, err, notEdges(depsPath))
	}
	if _, err := deps.Seek(0, io.SeekStart); err != nil {
		return linkweight.Binary{}, err
	}
	g, err := linkweight.ParseDeps(deps)
	if err != nil {
		return linkweight.Binary{}, fmt.Errorf("%s: %w", root, err)
	}
	listed, err := sh("go", "list", "-tags", tags, "-deps", "-json=ImportPath,Imports,Standard,Module", root)
	if err != nil {
		return linkweight.Binary{}, err
	}
	pkgs, err := linkweight.ParsePackages(strings.NewReader(listed))
	if err != nil {
		return linkweight.Binary{}, err
	}
	layout, err := linkweight.ReadLayout(bin)
	if err != nil {
		return linkweight.Binary{}, err
	}
	return linkweight.Analyze(linkweight.Input{
		Root:     strings.TrimPrefix(strings.TrimPrefix(root, module), "/"),
		MainPath: root,
		Module:   module,
		Packages: pkgs,
		Layout:   layout,
		Graph:    g,
	}), nil
}

// notEdges is what a failed build said, without the graph the linker may have started printing.
func notEdges(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // our own temporary file
	if err != nil {
		return err.Error()
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" && !strings.Contains(line, " -> ") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// sh is a command's standard output; what it said on standard error is in the error.
func sh(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output() //nolint:gosec // go, over the caller's own module
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}
