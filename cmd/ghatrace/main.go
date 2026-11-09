// Command ghatrace shows where a GitHub Actions run's time went, with nothing hosted:
//
//	go tool ghatrace run                  the run as a Job Summary, trace.html and trace.json
//	go tool ghatrace span -name N -- cmd  run cmd and record it as a span of the step it runs in
//
// run is the last job of a workflow, or a step at the end of its last job. It reads its own run
// ($GITHUB_RUN_ID, or -run) from the API with $GH_TOKEN or $GITHUB_TOKEN, and the spans the run's
// jobs uploaded as artifacts named ghatrace-*. span records to $GHATRACE_SPANS, or
// $RUNNER_TEMP/ghatrace-spans.jsonl on a runner, and outside one only runs the command.
package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spin-stack/go-tools/internal/ghatrace"
	"github.com/spin-stack/go-tools/internal/proven"
)

func main() {
	err := run(context.Background(), os.Args[1:]) // mutate-exempt: main's way in; run is what the tests drive
	var exit exitError
	if errors.As(err, &exit) {
		os.Exit(int(exit))
	}
	if err != nil { // mutate-exempt: main's way out; run is what the tests drive
		fmt.Fprintf(os.Stderr, "ghatrace: %v\n", err)
		os.Exit(1) // mutate-exempt: a refusal's exit code
	}
}

// exitError is a command span ran that did not succeed: span exits as it did.
type exitError int

func (e exitError) Error() string { return "exit status " + strconv.Itoa(int(e)) }

const usage = `usage: ghatrace run|span [flags]

  run   read a workflow run and write where its time went: the Job Summary ($GITHUB_STEP_SUMMARY,
        or stdout), trace.html (open it in a browser) and trace.json (open it at ui.perfetto.dev)
        in -out
  span  -name NAME -- cmd args...: run cmd, and record it as a span of the step it runs in

-h after a command says the rest.
`

func run(ctx context.Context, argv []string) error {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help" {
		fmt.Print(usage)
		if len(argv) == 0 {
			return errors.New("a command is needed")
		}
		return nil
	}
	switch argv[0] {
	case "run":
		return report(ctx, argv[1:])
	case "span":
		return span(ctx, argv[1:])
	}
	return fmt.Errorf("no command %q; one of run, span", argv[0])
}

func report(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ghatrace run", flag.ContinueOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name")
	id := fs.String("run", os.Getenv("GITHUB_RUN_ID"), "the run: its id, or its URL")
	out := fs.String("out", "_output/ghatrace", "the directory trace.html and trace.json are written to")
	artifacts := fs.String("artifacts", "ghatrace-", "the start of the names of the run's artifacts that hold spans; empty reads none")
	local := fs.String("spans", "", "a directory whose *.jsonl files are spans too, read as well as the artifacts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runID, owner, err := runOf(*id, *repo)
	if err != nil {
		return err
	}
	gh := proven.GitHub{
		API:   cmp.Or(os.Getenv("GITHUB_API_URL"), "https://api.github.com"),
		Repo:  owner,
		Token: cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")),
	}
	jobs, err := gh.Jobs(ctx, runID)
	if err != nil {
		return err
	}
	spans, err := spansOf(ctx, gh, runID, *artifacts, *local)
	if err != nil {
		return err
	}
	r := ghatrace.Run{
		ID:       runID,
		URL:      fmt.Sprintf("%s/%s/actions/runs/%d", cmp.Or(os.Getenv("GITHUB_SERVER_URL"), "https://github.com"), owner, runID),
		Workflow: fmt.Sprintf("run %d", runID),
		Jobs:     jobs,
		Now:      time.Now(),
	}
	for _, j := range jobs {
		r.Workflow = cmp.Or(j.Workflow, r.Workflow)
	}
	t := ghatrace.Build(r, spans)
	if err := os.MkdirAll(*out, 0o755); err != nil { //nolint:gosec // the output is the run's to read
		return err
	}
	if err := write(filepath.Join(*out, "trace.html"), false, func(w io.Writer) error { return ghatrace.WriteHTML(w, t) }); err != nil {
		return err
	}
	if err := write(filepath.Join(*out, "trace.json"), false, func(w io.Writer) error { return ghatrace.WritePerfetto(w, t) }); err != nil {
		return err
	}
	files := "The waterfall is trace.html, and the detail trace.json for https://ui.perfetto.dev, in " + *out + "."
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		files = "The waterfall is trace.html, and the detail trace.json for [ui.perfetto.dev](https://ui.perfetto.dev), in this run's artifacts."
		return write(path, true, func(w io.Writer) error { return ghatrace.WriteSummary(w, t, files) })
	}
	return ghatrace.WriteSummary(os.Stdout, t, files)
}

// runOf is the run's id and repository from -run, which may be the run's URL.
func runOf(id, repo string) (int64, string, error) {
	if site, rest, ok := strings.Cut(id, "/actions/runs/"); ok {
		parts := strings.Split(site, "/")
		if len(parts) >= 2 {
			repo = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
		id, _, _ = strings.Cut(rest, "/")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("-run or GITHUB_RUN_ID names the run: %q: %w", id, err)
	}
	if repo == "" {
		return 0, "", errors.New("-repo or GITHUB_REPOSITORY names the repository")
	}
	return n, repo, nil
}

// spansOf is every span the run's jobs uploaded, and every span in the files under dir.
func spansOf(ctx context.Context, gh proven.GitHub, runID int64, prefix, dir string) ([]ghatrace.Span, error) {
	var spans []ghatrace.Span
	if prefix != "" {
		arts, err := gh.Artifacts(ctx, runID)
		if err != nil {
			return nil, err
		}
		for _, a := range arts {
			if a.Expired || !strings.HasPrefix(a.Name, prefix) {
				continue
			}
			archive, err := gh.Archive(ctx, a.URL)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.Name, err)
			}
			got, err := unzip(archive)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", a.Name, err)
			}
			spans = append(spans, got...)
		}
	}
	if dir == "" {
		return spans, nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".jsonl" {
			return err
		}
		f, err := os.Open(path) //nolint:gosec // a file under the directory -spans names
		if err != nil {
			return err
		}
		got, err := ghatrace.Read(f)
		_ = f.Close() // read-only
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		spans = append(spans, got...)
		return nil
	})
	return spans, err
}

// unzip is the spans in every file of an artifact's archive, or in the artifact itself when it was
// uploaded as one file, unarchived.
func unzip(archive []byte) ([]ghatrace.Span, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if errors.Is(err, zip.ErrFormat) {
		return ghatrace.Read(bytes.NewReader(archive))
	}
	if err != nil {
		return nil, err
	}
	var spans []ghatrace.Span
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		got, err := ghatrace.Read(rc)
		_ = rc.Close() // read-only
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		spans = append(spans, got...)
	}
	return spans, nil
}

// write writes a file afresh, or adds to it when it is the run's summary, which every step adds to.
func write(path string, add bool, f func(io.Writer) error) error {
	mode := os.O_TRUNC
	if add {
		mode = os.O_APPEND
	}
	file, err := os.OpenFile(path, mode|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // the run's own files
	if err != nil {
		return err
	}
	return errors.Join(f(file), file.Close())
}

func span(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ghatrace span", flag.ContinueOnError)
	name := fs.String("name", "", "what the span is called; the command when empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("span: a command is needed after --")
	}
	if *name == "" {
		*name = strings.Join(argv, " ")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the command the caller asked to run
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A cancelled run interrupts the step: the command gets the signal, and the span is written
	// once it has stopped, so a cancelled lane still shows where it was.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s) // the command may have exited already
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	s := ghatrace.Span{Name: *name, Start: start, End: time.Now(), Runner: os.Getenv("RUNNER_NAME"), Job: os.Getenv("GITHUB_JOB")}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		s.Exit = exit.ExitCode()
		if !exit.Exited() { // killed by a signal
			s.Exit = 128
		}
	case err != nil:
		return err
	}
	if path := spansFile(); path != "" {
		if werr := ghatrace.Append(path, s); werr != nil {
			fmt.Fprintf(os.Stderr, "ghatrace: span %q not recorded: %v\n", s.Name, werr)
		}
	}
	if s.Exit != 0 {
		return exitError(s.Exit)
	}
	return nil
}

// spansFile is where spans are recorded: $GHATRACE_SPANS, or a file of the runner's own on one,
// and nowhere off one, where span only runs the command.
func spansFile() string {
	if path := os.Getenv("GHATRACE_SPANS"); path != "" {
		return path
	}
	if tmp := os.Getenv("RUNNER_TEMP"); tmp != "" {
		return filepath.Join(tmp, "ghatrace-spans.jsonl")
	}
	return ""
}
