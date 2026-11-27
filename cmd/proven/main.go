// Command proven asks GitHub what a repository's CI already proved about a commit, and writes the
// answer where the workflow reads it ($GITHUB_OUTPUT, or stdout outside one):
//
//	go tool proven tree      proven=true|false: whether the gate passed on the commit's tree
//	go tool proven base      what a run on main is of: proven=true, or base=<commit> for the diff
//	go tool proven release   exits 1 unless the gate passed on the tree and the push lanes on the commit
//	go tool proven times     times=<lines>: each test's seconds in the last gate that passed
//	go tool proven flakes    which failed tests are flaky (passed on the same tree) and which were red
//	go tool proven tests     every test the runs that answer for the commit logged, as JSON
//
// The repository is -repo or $GITHUB_REPOSITORY, the commit -sha or $GITHUB_SHA or HEAD, the token
// $GH_TOKEN or $GITHUB_TOKEN, the API $GITHUB_API_URL or github.com's. The gate is -workflow,
// ci.yml by default, and answers for a tree by its runs on a schedule or a dispatch (-events).
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/internal/interrupt"
	"github.com/spin-stack/go-tools/internal/proven"
)

func main() {
	ctx := interrupt.Context()
	err := run(ctx, os.Args[1:]) // mutate-exempt: main's way out; run is what the tests drive
	if err != nil {
		fmt.Fprintf(os.Stderr, "proven: %v\n", err)
		os.Exit(1) // mutate-exempt: a refusal's exit code, which a workflow reads as the step failing
	}
}

const usage = `usage: proven tree|base|release|times|flakes|tests [flags]

  tree     proven=true|false, whether the gate passed on the commit's tree
  base     what a run on main is of: proven=true, or base=<commit> for mutate's diff
  release  refuse (exit 1) unless the gate passed on the tree and -also's lanes on the commit
  times    times=<lines>, each test's seconds from the last gate that passed
  flakes   every test that failed in the last -limit runs of -workflow and -also: flaky when it
           did not fail on another run of the same tree, intermittent when on and off along a
           branch, red when it failed on runs in a row, once when nothing else tells
  tests    every test the runs that answer for the commit logged - the gate's by tree, -also's
           by sha - each with its answer, its time and the job that ran it, as JSON to -out

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
	mainBranch := fs.String("main", "main", "base: the branch whose history is the base's; release: the ref a missing answer is dispatched on")
	wait := fs.Duration("wait", 0, "release: dispatch each workflow with no answer on the commit and wait this long for the answers; 0 refuses at once")
	depth := fs.Int("depth", 200, "base: how far back in main's first-parent history a passed run is looked for") // mutate-exempt: a default, as deep as a day on main goes
	also := fs.String("also", "kvm.yml", "release: the workflow that runs on every push to main, asked of the commit itself; empty is none")
	jobs := fs.String("jobs", "e2e-verify", "times: the jobs whose logs hold the tests' times, by the start of their name")
	ran := fs.String("ran", "", "tree, release, tests, base: the jobs, by the start of their name and comma-separated, a run that passed must have run to success to answer for a tree; a gate that left one out proves nothing of it, and an older run on the tree is asked")
	out := fs.String("out", "", "tests: the file the JSON is written to; stdout when empty")
	if err := fs.Parse(argv[1:]); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("-repo or GITHUB_REPOSITORY names the repository")
	}
	if *sha == "" {
		*sha = "HEAD"
	}
	// The whole sha: the API's head_sha filter and a run's head match nothing shorter. A commit
	// the clone has not - a pull request's head - stays as given.
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", *sha+"^{commit}").Output(); err == nil { //nolint:gosec // a commit's name, which git refuses when it is not one
		*sha = strings.TrimSpace(string(out))
	} else if *sha == "HEAD" {
		return fmt.Errorf("-sha or GITHUB_SHA names the commit; HEAD: %w", err)
	}
	gh := proven.GitHub{
		API:   cmp.Or(os.Getenv("GITHUB_API_URL"), "https://api.github.com"),
		Repo:  *repo,
		Token: cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")),
	}
	trees := proven.TreesOf(".", gh)
	ev := strings.Split(*events, ",")
	switch argv[0] {
	case "tree":
		return tree(ctx, gh, trees, *sha, *workflow, ev, *limit, lanes(*ran))
	case "base":
		return base(ctx, gh, trees, *sha, *ref, *mainBranch, *workflow, ev, *limit, *depth, lanes(*ran))
	case "release":
		return release(ctx, gh, trees, *sha, *workflow, ev, *limit, *also, lanes(*ran), *mainBranch, *wait)
	case "times":
		return times(ctx, gh, *workflow, *jobs, *limit)
	case "flakes":
		workflows := []string{*workflow}
		if *also != "" {
			workflows = append(workflows, *also)
		}
		return flakes(ctx, gh, trees, workflows, *limit)
	case "tests":
		return tests(ctx, gh, trees, *sha, *workflow, ev, *limit, *also, *out, lanes(*ran))
	}
	return fmt.Errorf("no command %q; one of tree, base, release, times, flakes, tests", argv[0])
}

// evidence is what the runs that answer for a commit logged of their tests: the record a
// release publishes beside its files, so that a reader with no access to the repository can see
// which test ran on which machine, when, and what it answered.
type evidence struct {
	Commit string        `json:"commit"`
	Runs   []evidenceRun `json:"runs"`
	Tests  []evidenceOf  `json:"tests"`
	// Measures is every number the jobs' tests logged for the record (proven.Measures): what
	// a lane timed, on the machine the job names.
	Measures []evidenceMeasure `json:"measures"`
}

// evidenceRun is one run that answers for the commit: the gate's, found by the commit's tree,
// or the push workflow's, found by the commit itself.
type evidenceRun struct {
	Workflow   string `json:"workflow"`
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Conclusion string `json:"conclusion"`
	// Head is the commit the run was of: the gate's may be another commit with the same tree.
	Head string `json:"head"`
}

// evidenceOf is one test's answer in one job.
type evidenceOf struct {
	proven.Test
	evidenceJob
}

// evidenceMeasure is one number a job's test logged, with the job that ran it.
type evidenceMeasure struct {
	proven.Measure
	evidenceJob
}

// evidenceJob is where an answer or a measurement was logged.
type evidenceJob struct {
	Workflow string `json:"workflow"`
	Run      int64  `json:"run"`
	Job      string `json:"job"`
	JobURL   string `json:"jobUrl"`
	// Runner is the labels the job asked of its runner; a fleet's names the machine it ran on.
	Runner []string `json:"runner,omitempty"`
}

// tests is every test the answering runs logged, each with the job it ran in: the gate's run
// on the commit's tree, as tree finds it, and the newest run of -also on the commit, as release
// does. A job whose log the API no longer holds says nothing (proven.ErrGone); the runs are in
// the record whether they logged any test or not, so a reader sees what answered.
func tests(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int, also, out string, ran []string) error {
	t, err := trees(ctx, sha)
	if err != nil {
		return err
	}
	runs, err := gh.Runs(ctx, workflow, completed, limit)
	if err != nil {
		return err
	}
	answer, found, _, err := proof(ctx, gh, runs, sha, t, events, trees, ran)
	if err != nil {
		return err
	}
	var answering []evidenceRun
	if found {
		answering = append(answering, evidenceRun{Workflow: workflow, ID: answer.ID, URL: answer.URL, Conclusion: answer.Conclusion, Head: answer.HeadSHA})
	}
	if also != "" {
		alsoRuns, err := gh.Runs(ctx, also, onCommit(sha), limit)
		if err != nil {
			return err
		}
		for _, r := range alsoRuns {
			if r.Status == "completed" && r.Conclusion != "cancelled" {
				answering = append(answering, evidenceRun{Workflow: also, ID: r.ID, URL: r.URL, Conclusion: r.Conclusion, Head: r.HeadSHA})
				break
			}
		}
	}
	e := evidence{Commit: sha, Runs: answering, Tests: []evidenceOf{}, Measures: []evidenceMeasure{}}
	for _, r := range answering {
		jobs, err := gh.Jobs(ctx, r.ID)
		if err != nil {
			return fmt.Errorf("run %d: %w", r.ID, err)
		}
		for _, j := range jobs {
			log, err := gh.Log(ctx, j.ID)
			if errors.Is(err, proven.ErrGone) {
				continue
			}
			if err != nil {
				return fmt.Errorf("job %d of run %d: %w", j.ID, r.ID, err)
			}
			where := evidenceJob{Workflow: r.Workflow, Run: r.ID, Job: j.Name, JobURL: j.URL, Runner: j.Labels}
			for _, test := range proven.Tests(log) {
				e.Tests = append(e.Tests, evidenceOf{Test: test, evidenceJob: where})
			}
			for _, m := range proven.Measures(log) {
				e.Measures = append(e.Measures, evidenceMeasure{Measure: m, evidenceJob: where})
			}
		}
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	fmt.Printf("%d runs answer for %s: %d tests logged\n", len(e.Runs), sha, len(e.Tests))
	if out == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(out, b, 0o600)
}

// tree answers whether the gate passed on the commit's tree, for a workflow that then runs only
// the lanes that run did not.
func tree(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int, ran []string) error {
	t, err := trees(ctx, sha)
	if err != nil {
		return err
	}
	runs, err := gh.Runs(ctx, workflow, completed, limit)
	if err != nil {
		return err
	}
	run, found, missing, err := proof(ctx, gh, runs, sha, t, events, trees, ran)
	if err != nil {
		return err
	}
	if missing != "" {
		fmt.Printf("%s passed on %s's tree without running %s (%s): every lane\n", workflow, sha, missing, run.URL)
		return output("proven", "false")
	}
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
func base(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, ref, mainBranch, workflow string, events []string, limit, depth int, ran []string) error {
	if ref != "refs/heads/"+mainBranch {
		fmt.Printf("a dispatch on %s: every lane, mutate against %s\n", ref, mainBranch)
		return output("base", "origin/"+mainBranch)
	}
	runs, err := gh.Runs(ctx, workflow, succeeded, limit)
	if err != nil {
		return err
	}
	// A pass that left a lane of ran out is no base: what it skipped was never run on that tree.
	var passed []proven.Run
	for _, r := range runs {
		if !slices.Contains(events, r.Event) {
			continue
		}
		ok, _, err := ranAll(ctx, gh, r, ran)
		if err != nil {
			return err
		}
		if ok {
			passed = append(passed, r)
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
// Given wait, it first makes the answers that are missing: each workflow with none on the commit
// is dispatched on mainBranch, once, and the verdict is asked again when that run and any run of
// also still going have finished, until wait is up. A run that failed is never dispatched again.
func release(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int, also string, ran []string, mainBranch string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	// Each workflow dispatched: the runs it had on the commit before, so its new one is told apart.
	before := map[string]map[int64]bool{}
	for {
		// Before the verdict: a run that finishes between the two is then in the verdict, rather
		// than done and still missing from it.
		done := map[string]bool{}
		for w, seen := range before {
			d, err := dispatchedDone(ctx, gh, w, sha, limit, seen)
			if err != nil {
				return err
			}
			done[w] = d
		}
		v, err := verdict(ctx, gh, trees, sha, workflow, events, limit, also, ran)
		if err != nil {
			return err
		}
		waiting := false
		if wait > 0 && !v.OK { // mutate-exempt: a wait of a nanosecond is no wait anyone asks for
			if waiting, err = dispatchMissing(ctx, gh, v, before, done, sha, mainBranch, limit, wait); err != nil {
				return err
			}
		}
		if !waiting || !time.Now().Before(deadline) {
			for _, line := range v.Lines {
				fmt.Println(line)
			}
			if !v.OK {
				return errRefused
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// dispatchMissing dispatches each workflow v is missing that was not dispatched yet, recording in
// before its runs on the commit until then, and says whether an answer is still coming: a run of
// also still going, a dispatch just made, or a dispatched run not finished.
func dispatchMissing(ctx context.Context, gh proven.GitHub, v proven.Verdict, before map[string]map[int64]bool, done map[string]bool, sha, mainBranch string, limit int, wait time.Duration) (bool, error) {
	waiting := v.Running
	for _, w := range v.Missing {
		if _, sent := before[w]; sent {
			// Its run finished and it is still missing: the run answered nothing (a gate on main
			// that skipped its lanes again), and waiting more changes nothing.
			waiting = waiting || !done[w]
			continue
		}
		seen, err := runsOn(ctx, gh, w, sha, limit)
		if err != nil {
			return false, err
		}
		if err := gh.Dispatch(ctx, w, mainBranch); err != nil {
			return false, err
		}
		before[w] = seen
		fmt.Printf("%s has no answer on %s: dispatched on %s, waiting for it up to %s\n", w, sha, mainBranch, wait)
		waiting = true
	}
	return waiting, nil
}

// poll is how often release asks again while it waits: a lane takes minutes, and every ask is a
// few API calls.
var poll = 30 * time.Second

// verdict is the release's verdict now: the gate's answer for the tree (proof), and also's runs on
// the commit from a push or a dispatch.
func verdict(ctx context.Context, gh proven.GitHub, trees proven.Trees, sha, workflow string, events []string, limit int, also string, ran []string) (proven.Verdict, error) {
	t, err := trees(ctx, sha)
	if err != nil {
		return proven.Verdict{}, err
	}
	runs, err := gh.Runs(ctx, workflow, completed, limit)
	if err != nil {
		return proven.Verdict{}, err
	}
	answer, found, left, err := proof(ctx, gh, runs, sha, t, events, trees, ran)
	if err != nil {
		return proven.Verdict{}, err
	}
	var alsoRuns []proven.Run
	if also != "" {
		all, err := gh.Runs(ctx, also, onCommit(sha), limit)
		if err != nil {
			return proven.Verdict{}, err
		}
		for _, r := range all {
			if r.Event == "push" || r.Event == "workflow_dispatch" {
				alsoRuns = append(alsoRuns, r)
			}
		}
	}
	return proven.Judge(sha, workflow, answer, found, left, also, alsoRuns), nil
}

// onCommit is the runs query for the commit's own runs.
func onCommit(sha string) url.Values { return url.Values{"head_sha": {sha}} }

// runsOn is the ids of workflow's runs on the commit, whatever their state.
func runsOn(ctx context.Context, gh proven.GitHub, workflow, sha string, limit int) (map[int64]bool, error) {
	runs, err := gh.Runs(ctx, workflow, onCommit(sha), limit)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, r := range runs {
		ids[r.ID] = true
	}
	return ids, nil
}

// dispatchedDone is whether the run a dispatch started on the commit - one not among seen - has
// finished. None yet is not done: GitHub lists a dispatched run a few seconds after the dispatch.
func dispatchedDone(ctx context.Context, gh proven.GitHub, workflow, sha string, limit int, seen map[int64]bool) (bool, error) {
	runs, err := gh.Runs(ctx, workflow, onCommit(sha), limit)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if !seen[r.ID] {
			return r.Status == "completed", nil
		}
	}
	return false, nil
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

// flakes is every test that failed in the last limit runs of each workflow, judged by its trees
// (proven.Flakes): the failed tests of a run are read from the artifacts it kept - a lane's logs,
// which the fleet keeps only when the lane failed - and, when it kept none, from its failed
// jobs' logs. The table goes to the run's summary and stdout; the tests flaky more than once or
// intermittent are named last, as the candidates for a quarantine.
func flakes(ctx context.Context, gh proven.GitHub, trees proven.Trees, workflows []string, limit int) error {
	var all []proven.Failure
	runsAsked := 0
	for _, w := range workflows {
		runs, err := gh.Runs(ctx, w, completed, limit)
		if err != nil {
			return err
		}
		runsAsked += len(runs)
		failed := map[int64][]string{}
		for _, r := range runs {
			if r.Conclusion != "failure" {
				continue
			}
			tests, err := failedTests(ctx, gh, r.ID)
			if err != nil {
				return fmt.Errorf("run %d: %w", r.ID, err)
			}
			failed[r.ID] = tests
		}
		found, err := proven.Flakes(ctx, runs, failed, trees)
		if err != nil {
			return err
		}
		for i := range found {
			found[i].Test = w + ": " + found[i].Test
		}
		all = append(all, found...)
	}
	// The workflows' failures as one list, most failures first, then by name.
	sort.Slice(all, func(i, j int) bool {
		if len(all[i].Runs) != len(all[j].Runs) {
			return len(all[i].Runs) > len(all[j].Runs)
		}
		return all[i].Test < all[j].Test
	})
	lines := proven.Report(all, runsAsked)
	for _, l := range lines {
		summary(l)
	}
	var candidates []string
	for _, f := range all {
		if (f.Kind == proven.Flaky && len(f.Runs) > 1) || f.Kind == proven.Intermittent {
			candidates = append(candidates, f.Test)
		}
	}
	if len(candidates) == 0 {
		fmt.Printf("%d failed tests in %d runs; none flaky more than once\n", len(all), runsAsked)
		return nil
	}
	fmt.Printf("flaky more than once, for hack/quarantine.txt or a fix: %s\n", strings.Join(candidates, ", "))
	return nil
}

// failedTests is every test a failed run's kept logs say failed: its artifacts, each a zip, else
// its failed jobs' logs. A log or an artifact the API no longer holds says nothing (proven.ErrGone).
func failedTests(ctx context.Context, gh proven.GitHub, run int64) ([]string, error) {
	artifacts, err := gh.Artifacts(ctx, run)
	if err != nil {
		return nil, err
	}
	var tests []string
	for _, a := range artifacts {
		if a.Expired {
			continue
		}
		archive, err := gh.Archive(ctx, a.URL)
		if errors.Is(err, proven.ErrGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found, err := proven.FailedIn(archive)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", a.Name, err)
		}
		tests = append(tests, found...)
	}
	if len(tests) > 0 {
		return unique(tests), nil
	}
	jobs, err := gh.Jobs(ctx, run)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Conclusion != "failure" {
			continue
		}
		log, err := gh.Log(ctx, j.ID)
		if errors.Is(err, proven.ErrGone) {
			continue
		}
		if err != nil {
			return nil, err
		}
		tests = append(tests, proven.Failed(log)...)
	}
	return unique(tests), nil
}

func unique(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
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

// proof is the run that answers for the commit's tree (proven.Answer) once asked which lanes it
// must have run: the newest that failed, or that passed and ran every one of ran. A pass that ran
// fewer is passed over for an older run on the tree, because a pass can point elsewhere: ci.yml on
// main skips every lane when another run already passed on its tree, and still concludes success.
// When no run on the tree ran them all, the answer is the newest pass with the lane it left out.
func proof(ctx context.Context, gh proven.GitHub, runs []proven.Run, sha, tree string, events []string, trees proven.Trees, ran []string) (proven.Run, bool, string, error) {
	var partial proven.Run
	var missing string
	for rest := runs; ; {
		r, found := proven.Answer(ctx, rest, sha, tree, events, trees)
		if !found {
			return partial, missing != "", missing, nil
		}
		if !r.Passed() {
			return r, true, "", nil
		}
		ok, m, err := ranAll(ctx, gh, r, ran)
		if err != nil {
			return proven.Run{}, false, "", err
		}
		if ok {
			return r, true, "", nil
		}
		if missing == "" {
			partial, missing = r, m
		}
		rest = rest[slices.IndexFunc(rest, func(x proven.Run) bool { return x.ID == r.ID })+1:]
	}
}

// ranAll is whether run ran each of ran's jobs to success (proven.Ran); asked nothing, it is, and
// nothing is fetched.
func ranAll(ctx context.Context, gh proven.GitHub, run proven.Run, ran []string) (bool, string, error) {
	if len(ran) == 0 {
		return true, "", nil
	}
	jobs, err := gh.Jobs(ctx, run.ID)
	if err != nil {
		return false, "", err
	}
	ok, missing := proven.Ran(jobs, ran)
	return ok, missing, nil
}

// lanes is -ran's list: none when it is empty.
func lanes(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
