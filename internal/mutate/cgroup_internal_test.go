//go:build linux

package mutate

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// inScope marks the run of this test that systemd-run started in a delegated scope.
const inScope = "MUTATE_TEST_IN_DELEGATED_SCOPE"

// In a cgroup systemd delegated, a test process is held by the kernel: one that allocates past
// its memory, or forks past its tasks, is stopped and reported so, at once; what one leaves
// running when it ends is killed rather than waited on; and nothing of either is left after.
func TestATestProcessIsHeldByItsCgroup(t *testing.T) {
	if !inDelegatedScope(t) {
		return
	}
	dir, err := Confine()
	if err != nil || dir == "" {
		t.Fatalf("a delegated scope was taken as %q, %v", dir, err)
	}
	t.Cleanup(func() { testCgroup, memLimit, pidsMax = "", 0, 1024 })
	memLimit, pidsMax = 64<<20, 32
	run := func(script string) (string, bool, time.Duration, error) {
		started := time.Now()
		out, exceeded, err := bounded("test", exec.Command("sh", "-c", script))
		return string(out), exceeded, time.Since(started), err
	}

	if out, exceeded, _, err := run("echo held"); exceeded || err != nil || out != "held\n" {
		t.Errorf("a process within its bound answered %q, exceeded %v, %v", out, exceeded, err)
	}
	// tail -n 1 keeps a line until its end, and /dev/zero has none.
	if _, exceeded, _, _ := run("head -c 512M /dev/zero | tail -n 1 >/dev/null"); !exceeded {
		t.Error("a process that held 512 MiB under a bound of 64 was not stopped for it")
	}
	if _, exceeded, took, _ := run("for i in $(seq 100); do sleep 60 & done; wait"); !exceeded || took > 10*time.Second {
		t.Errorf("a process that forked past 32 tasks: exceeded %v after %s", exceeded, took)
	}
	if out, exceeded, took, err := run("sleep 60 & echo left"); exceeded || err != nil || took > 10*time.Second {
		t.Errorf("a process that left a child running: %q, exceeded %v, %v, after %s", out, exceeded, err, took)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != leadCgroup {
			t.Errorf("%s is left in the delegated cgroup", e.Name())
		}
	}
	if procs, _ := os.ReadFile(dir + "/" + leadCgroup + "/cgroup.procs"); len(strings.Fields(string(procs))) != 1 {
		t.Errorf("the run's own cgroup holds %q, want this process alone", procs)
	}
}

// inDelegatedScope is whether this is the run of t in a scope systemd delegated it; when it is
// not, it runs t again in one and reports what that run said, or skips where no user manager
// can start one.
func inDelegatedScope(t *testing.T) bool {
	t.Helper()
	if os.Getenv(inScope) != "" {
		return true
	}
	if err := exec.Command("systemd-run", "--user", "--scope", "--quiet", "true").Run(); err != nil {
		t.Skipf("no systemd user manager to delegate a scope: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "-p", "Delegate=yes",
		self, "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	run.Env = append(os.Environ(), inScope+"=1")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("in a delegated scope: %v\n%s", err, out)
	}
	return false
}
