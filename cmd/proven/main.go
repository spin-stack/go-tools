// Command proven asks GitHub what a repository's CI already proved about a commit, and writes the
// answer where the workflow reads it ($GITHUB_OUTPUT, or stdout outside one):
//
//	go tool proven tree      proven=true|false: whether the gate passed on the commit's tree
//	go tool proven base      what a run on main is of: proven=true, or base=<commit> for the diff
//	go tool proven release   exits 1 unless the gate passed on the tree and the push lanes on the commit
//	go tool proven times     times=<lines>: each test's seconds in the last gate that passed
//
// The repository is -repo or $GITHUB_REPOSITORY, the commit -sha or $GITHUB_SHA or HEAD, the token
// $GH_TOKEN or $GITHUB_TOKEN, the API $GITHUB_API_URL or github.com's. The gate is -workflow,
// ci.yml by default, and answers for a tree by its runs on a schedule or a dispatch (-events).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spin-stack/go-tools/internal/proven"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil { // mutate-exempt: main's way out; run is what the tests drive
		fmt.Fprintf(os.Stderr, "proven: %v\n", err)
		os.Exit(1) // mutate-exempt: a refusal's exit code, which a workflow reads as the step failing
	}
}

const usage = `usage: proven tree|base|release|times [flags]

  tree     proven=true|false, whether the gate passed on the commit's tree
  base     what a run on main is of: proven=true, or base=<commit> for mutate's diff
  release  refuse (exit 1) unless the gate passed on the tree and -also's lanes on the commit
  times    times=<lines>, each test's seconds from the last gate that passed

Each takes -repo, -sha, -workflow, -events; -h says the rest.
`

// completed and succeeded are the runs asked for: every one that finished, and those that passed.
var (
	completed = url.Values{"status": {"completed"}}
	succeeded = url.Values{"status": {"success"}}
)

// errRefused is a release the gates do not allow: said already, line by line.
var errRefused = errors.New("refused")

func run(ctx context.Context, argv []string) error {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help" {
		fmt.Print(usage)
		if len(argv) == 0 {
			return errors.New("a command is needed")
		}
		return nil
	}
	fs := flag.NewFlagSet("proven "+argv[0], flag.ContinueOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name")
	sha := fs.String("sha", os.Getenv("GITHUB_SHA"), "the commit; HEAD when neither is given")
	workflow := fs.String("workflow", "ci.yml", "the gate's workflow file")
	events := fs.String("events", "schedule,workflow_dispatch", "the events whose runs answer for a tree, comma-separated")
	limit := fs.Int("limit", 40, "how many of the newest runs are asked")
	ref := fs.String("ref", os.Getenv("GITHUB_REF"), "base: the run's ref; a run not on -main is of its branch against main")
	mainBranch := fs.String("main", "main", "base: the branch whose history is the base's")
	depth := fs.Int("depth", 200, "base: how far back in main's first-parent history a passed run is looked for") // mutate-exempt: a default, as deep as a day on main goes
	also := fs.String("also", "kvm.yml", "release: the workflow that runs on every push to main, asked of the commit itself; empty is none")
	jobs := fs.String("jobs", "e2e-verify", "times: the jobs whose logs hold the tests' times, by the start of their name")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("-repo or GITHUB_REPOSITORY names the repository")
	}
	if *sha == "" {
		out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("-sha or GITHUB_SHA names the commit; HEAD: %w", err)
		}
		*sha = strings.TrimSpace(string(out))
	}
	gh := proven.GitHub{API: "https://api.github.com", Repo: *repo, Token: os.Getenv("GH_TOKEN")}
	if api := os.Getenv("GITHUB_API_URL"); api != "" {
		gh.API = api
	}
	if gh.Token == "" {
		gh.Token = os.Getenv("GITHUB_TOKEN")
	}
	trees := proven.TreesOf(".", gh)
	ev := strings.Split(*events, ",")
	switch argv[0] {
	case "tree":
		return tree(ctx, gh, trees, *sha, *workflow, ev, *limit)
	case "base":
		return base(ctx, gh, trees, *sha, *ref, *mainBranch, *workflow, ev, *limit, *depth)
	case "release":
		return release(ctx, gh, trees, *sha, *workflow, ev, *limit, *also)
	case "times":
		return times(ctx, gh, *workflow, *jobs, *limit)
	}
	return fmt.Errorf("no command %q; one of tree, base, release, times", argv[0])
}

// tree answers whether the gate passed on the commit's tree, for a workflow that then runs only
// the lanes that run did not.
func tree(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int) error {
	t, err := trees(ctx, sha)
	if err != nil {
		return err
	}
	runs, err := gh.Runs(ctx, workflow, completed, limit)
	if err != nil {
		return err
	}
	run, found := proven.Answer(ctx, runs, sha, t, events, trees)
	switch {
	case found && run.Passed():
		summary(fmt.Sprintf("%s passed on %s's tree: %s: what that run ran is not run again", workflow, sha, run.URL))
		return output("proven", "true")
	case found:
		fmt.Printf("%s did not pass on %s's tree (%s): %s: every lane\n", workflow, sha, run.Conclusion, run.URL)
	default:
		fmt.Printf("%s never finished on %s's tree: every lane\n", workflow, sha)
	}
	return output("proven", "false")
}

// base is what a run on main is of: nothing, when a run that passed has main's tree, else the
// diff from the newest of main's commits one did - what mutate is asked to break - or from the
// commit before when none of the last depth did. A run on another branch is of it against main.
func base(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, ref, mainBranch, workflow string, events []string, limit, depth int) error {
	if ref != "refs/heads/"+mainBranch {
		fmt.Printf("a dispatch on %s: every lane, mutate against %s\n", ref, mainBranch)
		return output("base", "origin/"+mainBranch)
	}
	runs, err := gh.Runs(ctx, workflow, succeeded, limit)
	if err != nil {
		return err
	}
	var passed []proven.Run
	for _, r := range runs {
		for _, e := range events {
			if r.Event == e {
				passed = append(passed, r)
				break
			}
		}
	}
	history, err := proven.FirstParent(ctx, ".", sha, depth)
	if err != nil {
		return err
	}
	c, run, found := proven.Base(ctx, history, passed, trees)
	switch {
	case found && c.SHA == sha:
		summary(fmt.Sprintf("%s passed on %s's tree: %s", workflow, mainBranch, run.URL))
		return output("proven", "true")
	case found:
		fmt.Printf("%s moved since %s (%s); every lane, mutate against it\n", mainBranch, c.SHA, run.URL)
		return output("base", c.SHA)
	case len(history) > 1:
		fmt.Printf("none of %s's last %d commits passed in the last %d runs: the last commit is what is new\n", mainBranch, len(history), limit)
		return output("base", history[1].SHA)
	}
	return fmt.Errorf("%s has no commit before %s to be the base", mainBranch, sha)
}

// release refuses a commit the gates did not pass on, each refusal said for the workflow's log.
func release(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int, also string) error {
	t, err := trees(ctx, sha)
	if err != nil {
		return err
	}
	runs, err := gh.Runs(ctx, workflow, completed, limit)
	if err != nil {
		return err
	}
	answer, found := proven.Answer(ctx, runs, sha, t, events, trees)
	var alsoRuns []proven.Run
	if also != "" {
		all, err := gh.Runs(ctx, also, url.Values{"head_sha": {sha}}, limit)
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Event == "push" || r.Event == "workflow_dispatch" {
				alsoRuns = append(alsoRuns, r)
			}
		}
	}
	v := proven.Judge(sha, workflow, answer, found, also, alsoRuns)
	for _, line := range v.Lines {
		fmt.Println(line)
	}
	if !v.OK {
		return errRefused
	}
	return nil
}

// times is each test's time from the newest of the last limit runs that passed whose jobs' logs
// have any: a run proven by another ran no lane, and its logs name no test.
func times(ctx context.Context, gh proven.GitHub, workflow, jobs string, limit int) error {
	runs, err := gh.Runs(ctx, workflow, succeeded, limit)
	if err != nil {
		return err
	}
	for _, r := range runs {
		all, err := gh.Jobs(ctx, r.ID)
		if err != nil {
			return err
		}
		var lines []string
		for _, j := range all {
			if !strings.HasPrefix(j.Name, jobs) {
				continue
			}
			log, err := gh.Log(ctx, j.ID)
			if err != nil {
				return err
			}
			lines = append(lines, proven.Times(log)...)
		}
		if len(lines) > 0 {
			fmt.Printf("run %d: %d tests' times\n", r.ID, len(lines))
			return output("times", strings.Join(lines, "\n"))
		}
	}
	fmt.Println("run none: 0 tests' times")
	return output("times", "")
}

// output hands the workflow one name=value through $GITHUB_OUTPUT - a heredoc when the value has
// lines - or prints it when there is none.
func output(name, value string) error {
	text := name + "=" + value + "\n"
	if strings.Contains(value, "\n") {
		text = name + "<<EOF\n" + value + "\nEOF\n"
	}
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		fmt.Print(text)
		return nil
	}
	return appendTo(path, text)
}

// summary says line, and writes it to the run's summary when there is one.
func summary(line string) {
	fmt.Println(line)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		_ = appendTo(path, line+"\n") // the summary is a courtesy; the answer is the output
	}
}

func appendTo(path, text string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // the workflow's own file, by its name
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
