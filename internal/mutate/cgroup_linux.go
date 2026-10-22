package mutate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// pidsMax is how many tasks - processes and their threads - a test process and everything it
// starts may hold between them. A test binary is tens of threads. An edit that makes a binary
// start itself each time it starts - a supervisor handed "this executable" - is thousands of
// processes of tens of megabytes each, and no measure of a tree from /proc stops it: each
// process leaves the tree when its parent dies, so the tree never holds much. On 2026-10-22 such
// processes took the 30 GiB of a workstation until it hung.
var pidsMax = 1024

// testCgroup is the cgroup this run was delegated, each test process started in a child of it;
// empty where it was delegated none, and a test's memory is measured from /proc. What uses it is
// held by TestATestProcessIsHeldByItsCgroup, in a scope systemd delegated a process of its own,
// whose coverage a mutate run does not see: hence its functions' lane markers.
var testCgroup string

// leadCgroup is the child of a delegated cgroup that the run's own processes move to: a cgroup
// that holds processes cannot give its children controllers of their own.
const leadCgroup = "mutate"

// delegated is the directory of the cgroup this process runs in when systemd delegated it
// (`systemd-run --user --scope -p Delegate=yes`), with its processes moved to a child so its
// children can be bounded; empty for any other, which this run must not rearrange.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func delegated() (string, error) {
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", nil //nolint:nilerr // no /proc: no cgroup to be given
	}
	// cgroup v2 alone is one line, "0::<path>"; v1 or a hybrid is more.
	path, ok := strings.CutPrefix(strings.TrimSpace(string(self)), "0::")
	if !ok || strings.Contains(path, "\n") {
		return "", nil
	}
	said, err := exec.Command("systemctl", "--user", "show", "--property=Delegate", "--value", filepath.Base(path)).Output()
	if err != nil || strings.TrimSpace(string(said)) != "yes" {
		return "", nil //nolint:nilerr // no user manager, or a unit it did not delegate
	}
	dir := filepath.Join("/sys/fs/cgroup", path)
	if err := takeOver(dir); err != nil {
		return "", fmt.Errorf("mutate: the cgroup %s was delegated, and cannot bound its children: %w", dir, err)
	}
	return dir, nil
}

// takeOver moves every process in dir to its lead child and gives dir's children the memory and
// pids controllers.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func takeOver(dir string) error {
	lead := filepath.Join(dir, leadCgroup)
	if err := os.Mkdir(lead, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// Again while it is busy: a process this run's parent starts between the move and the
	// enabling lands in dir, and holds it.
	var err error
	for range 10 {
		procs, readErr := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if readErr != nil {
			return readErr
		}
		for _, pid := range strings.Fields(string(procs)) {
			// ESRCH: gone since it was listed, which leaves nothing to move.
			if err := set(lead, "cgroup.procs", pid); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		err = set(dir, "cgroup.subtree_control", "+memory +pids")
		if !errors.Is(err, syscall.EBUSY) {
			return err
		}
	}
	return err
}

// confinedRuns names each test process's cgroup.
var confinedRuns atomic.Int64

// confined is bounded in a child of testCgroup: the kernel holds cmd and everything it starts to
// memLimit with no swap, and to pidsMax tasks, and what is left when cmd ends is killed with it.
// exceeded says they reached either.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func confined(what string, cmd *exec.Cmd) (out []byte, exceeded bool, err error) {
	dir := filepath.Join(testCgroup, "t"+strconv.FormatInt(confinedRuns.Add(1), 10))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, false, err
	}
	defer release(dir)
	limits := [][2]string{
		{"pids.max", strconv.Itoa(pidsMax)},
		{"memory.swap.max", "0"},
	}
	if memLimit > 0 {
		limits = append(limits, [2]string{"memory.max", strconv.FormatInt(memLimit, 10)})
	}
	for _, l := range limits {
		if err := set(dir, l[0], l[1]); err != nil {
			return nil, false, err
		}
	}
	fd, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = fd.Close() }()

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(fd.Fd())}
	// A process cmd started and left running holds its output open, and Wait would wait for it
	// for ever; release kills it once cmd is done.
	cmd.WaitDelay = time.Second
	started := time.Now()
	if err := cmd.Start(); err != nil {
		account(what, cmd, time.Since(started))
		return nil, false, err
	}
	done, stopped := make(chan struct{}), make(chan bool)
	go func() { stopped <- watchPids(dir, done) }()
	err = cmd.Wait()
	close(done)
	stoppedAtPids := <-stopped
	account(what, cmd, time.Since(started))
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // cmd itself succeeded; what it left is release's
	}
	return buf.Bytes(), stoppedAtPids || event(dir, "memory.events", "oom_kill") > 0, err
}

// watchPids kills dir's processes once a fork was refused there, and says whether it did: what
// starts processes until it cannot otherwise waits on them, holding nothing, until its timeout.
// The memory bound needs no watch: the kernel kills what passes it.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func watchPids(dir string, done <-chan struct{}) bool {
	tick := time.NewTicker(memPoll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return false
		case <-tick.C:
			if event(dir, "pids.events", "max") > 0 {
				_ = set(dir, "cgroup.kill", "1")
				return true
			}
		}
	}
}

// release kills whatever is left in dir and removes it, once it is empty or after a few seconds
// of waiting for that.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func release(dir string) {
	_ = set(dir, "cgroup.kill", "1")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if event(dir, "cgroup.events", "populated") == 0 {
			break
		}
	}
	_ = os.Remove(dir)
}

// set writes value to dir's file, which must be there: a cgroup's files are the kernel's, and
// one written where there is no cgroup is a file nobody reads.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func set(dir, file, value string) error {
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	return errors.Join(err, f.Close())
}

// event is key's count in dir's file of "key value" lines; 0 where it has none.
//
// mutate-lane: TestATestProcessIsHeldByItsCgroup
func event(dir, file, key string) int64 {
	events, _ := os.ReadFile(filepath.Join(dir, file))
	for line := range strings.Lines(string(events)) {
		if rest, ok := strings.CutPrefix(line, key+" "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			return n
		}
	}
	return 0
}
