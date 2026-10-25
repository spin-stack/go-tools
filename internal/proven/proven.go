// Package proven is what a repository's CI already proved about a commit, asked of GitHub's runs
// rather than remembered: which run answers for a commit's tree, what a run on main is of, whether
// a release's commit passed, and how long the last gate that passed took over each test. Each
// answer is a function over the runs and the commits, with a test; the API is read by one small
// client, and what a workflow reads next is written by the command.
//
// A run's answer is a tree's. A pull request's head, rebased onto a main that did not move,
// merges as a commit with the same tree, and the lanes that ran on those bytes do not run again
// for an hour to say the same thing. So a run on another commit with this one's tree answers for
// it, and a run on main's own commit is the ordinary case of that.
package proven

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// Run is one run of a workflow, as GitHub lists them, the newest first.
type Run struct {
	ID         int64  `json:"id"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Status     string `json:"status"`     // completed, in_progress, queued, ...
	Conclusion string `json:"conclusion"` // success, failure, cancelled, ...; empty until completed
	URL        string `json:"html_url"`
}

// completed is the status of a run that is over, whatever it concluded.
const completed = "completed"

// Passed is a run that finished and succeeded.
func (r Run) Passed() bool { return r.Status == completed && r.Conclusion == "success" }

// Finished is a run that ran to an answer, success or failure: a cancelled run answered nothing.
func (r Run) Finished() bool {
	return r.Status == completed && (r.Conclusion == "success" || r.Conclusion == "failure")
}

// Commit is one commit with its tree.
type Commit struct {
	SHA  string
	Tree string
}

// Trees is how a commit's tree is found by its sha: git when the commit is in the clone, the API
// when it is not - a pull request's head is nowhere in a checkout of main.
type Trees func(ctx context.Context, sha string) (string, error)

// Answer is the newest of runs (newest first) that finished on tree - on sha itself, or on another
// commit whose tree it is - and whether there is one. A run cancelled finished nothing and answers
// for nothing; a run still going is not an answer yet; a run of an event not in events is not
// asked. A head whose tree cannot be found - its branch force-pushed and the commit gone - is
// skipped, not fatal: it is one run of forty.
func Answer(ctx context.Context, runs []Run, sha, tree string, events []string, trees Trees) (Run, bool) {
	for _, r := range runs {
		if r.Status != "completed" || r.Conclusion == "cancelled" || !slices.Contains(events, r.Event) {
			continue
		}
		if r.HeadSHA == sha {
			return r, true
		}
		if t, err := trees(ctx, r.HeadSHA); err == nil && t == tree {
			return r, true
		}
	}
	return Run{}, false
}

// Base is the newest of main (its first-parent commits, newest first) that one of passed (the
// runs that succeeded, newest first) ran on - by its sha, or by its tree - with that run, and
// whether there is one. The first of main is the commit asked about: when it is the base, it is
// proven and nothing new is to be run.
//
// By tree and not by main's own runs: asked only of runs on main, the base was the last of them
// to pass, and every one that failed since kept it there. Two days and 332 commits behind, the
// diff mutate was asked to cover did not finish in the lane's hour, and no run on main could pass
// again - while every one of those commits had passed, on its pull request's branch, as the same
// tree.
func Base(ctx context.Context, main []Commit, passed []Run, trees Trees) (Commit, Run, bool) {
	bySHA := map[string]Run{}
	for _, r := range passed {
		if _, seen := bySHA[r.HeadSHA]; !seen {
			bySHA[r.HeadSHA] = r
		}
	}
	var byTree map[string]Run
	for _, c := range main {
		if r, ok := bySHA[c.SHA]; ok {
			return c, r, true
		}
		if byTree == nil {
			byTree = map[string]Run{}
			for _, r := range passed {
				t, err := trees(ctx, r.HeadSHA)
				if err != nil {
					continue
				}
				if _, seen := byTree[t]; !seen {
					byTree[t] = r
				}
			}
		}
		if r, ok := byTree[c.Tree]; ok {
			return c, r, true
		}
	}
	return Commit{}, Run{}, false
}

// Verdict is whether a commit may be released, and why in either case: each line is one
// workflow's answer, the refusals marked for the workflow log (::error::).
type Verdict struct {
	Lines []string
	OK    bool
}

// Judge is a release's verdict over the gate's answer for the commit's tree - the run, or none -
// and the runs of a second workflow on the commit itself (newest first), when there is one: the
// lanes that run on every push to main, whose newest run on the commit is the answer, since a
// re-run replaces a conclusion and a run cancelled for want of a machine is dispatched again.
func Judge(sha, workflow string, answer Run, found bool, also string, alsoRuns []Run) Verdict {
	var v Verdict
	v.OK = true
	switch {
	case !found:
		v.OK = false
		v.Lines = append(v.Lines, "::error::"+workflow+" never finished on "+sha+"'s tree: dispatch it on main (or on the branch it came from)")
	case answer.Conclusion != "success":
		v.OK = false
		v.Lines = append(v.Lines, "::error::"+workflow+" did not pass on "+sha+"'s tree ("+answer.Conclusion+"): "+answer.URL)
	default:
		v.Lines = append(v.Lines, workflow+" passed on "+sha+"'s tree: "+answer.URL)
	}
	if also == "" {
		return v
	}
	switch {
	case len(alsoRuns) == 0:
		v.OK = false
		v.Lines = append(v.Lines, "::error::"+also+" never ran on "+sha+" from a push to main: a release is made from a commit main tested")
	case alsoRuns[0].Passed():
		v.Lines = append(v.Lines, also+" passed on "+sha+": "+alsoRuns[0].URL)
	case alsoRuns[0].Status == "completed":
		v.OK = false
		v.Lines = append(v.Lines, "::error::"+also+" did not pass on "+sha+" ("+alsoRuns[0].Conclusion+"): "+alsoRuns[0].URL)
	default:
		v.OK = false
		v.Lines = append(v.Lines, "::error::"+also+" is still running on "+sha+" ("+alsoRuns[0].Status+"): release once it has passed: "+alsoRuns[0].URL)
	}
	return v
}

// took is go test's line for a test that ended, with how long it took.
var took = regexp.MustCompile(`--- (?:PASS|FAIL): (\S+) \(([0-9.]+)s\)`)

// Times is each test's time in a job's log, as "<seconds> <test>" lines in the order the tests
// ended - what a split of the tests by measured time reads. A subtest's line is one too, as go
// test prints it indented under its parent.
func Times(log string) []string {
	var out []string
	for _, m := range took.FindAllStringSubmatch(log, -1) { // mutate-exempt: any n < 0 is every match
		out = append(out, m[2]+" "+strings.TrimSpace(m[1]))
	}
	return out
}
