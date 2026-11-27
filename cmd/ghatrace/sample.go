package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// sample starts sampling the machine into the job's span file and returns: a step's process ends
// with the step, and a process it leaves running lives on to the job's end, when the runner ends
// every process the job started. Sampling never fails the step that starts it: what it cannot do,
// it says, and the job goes on.
func sample(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ghatrace sample", flag.ContinueOnError)
	every := fs.Duration("every", time.Second, "the time between two samples")
	limit := fs.Duration("for", 6*time.Hour, "the longest it samples, should nothing end it: a job on GitHub's own runners")
	foreground := fs.Bool("foreground", false, "sample in this process, until -for has passed, rather than in one of its own")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *every <= 0 || *limit <= 0 {
		return errors.New("sample: -every and -for are lengths of time, more than none")
	}
	path := ghatrace.File()
	if path == "" {
		fmt.Fprintln(os.Stderr, "ghatrace: sample: not on a runner and no $GHATRACE_SPANS: nothing sampled")
		return nil
	}
	if *foreground {
		return sampleUntil(ctx, "/", path, *every, *limit)
	}
	said, ok := announce(machine("/", time.Now()), *every, path)
	if ok {
		if err := background(ctx, path, "sample", "-foreground", "-every", every.String(), "-for", limit.String()); err != nil {
			said = fmt.Sprintf("ghatrace: sample: not sampling: %v", err)
		}
	}
	fmt.Fprintln(os.Stderr, said)
	return nil
}

// announce is what sample says it samples on a machine whose sample is s, and whether there is
// anything to: what the machine has, and what it has not.
func announce(s ghatrace.Sample, every time.Duration, path string) (string, bool) {
	var has, hasNot []string
	add := func(ok bool, name string) {
		if ok {
			has = append(has, name)
		} else {
			hasNot = append(hasNot, name)
		}
	}
	cpus := "/proc/stat"
	if s.CPU != nil {
		cpus = fmt.Sprintf("/proc/stat (%d CPUs)", s.CPU.CPUs)
	}
	add(s.CPU != nil, cpus)
	add(s.Pressure != nil, "/proc/pressure")
	disks := "/proc/diskstats' disks"
	if len(s.Disks) > 0 {
		names := make([]string, len(s.Disks))
		for i, d := range s.Disks {
			names[i] = d.Name
		}
		disks = "/proc/diskstats (" + strings.Join(names, ", ") + ")"
	}
	add(len(s.Disks) > 0, disks)
	if len(has) == 0 {
		return "ghatrace: sample: nothing here to sample: no /proc", false
	}
	said := fmt.Sprintf("ghatrace: sampling %s every %s into %s", strings.Join(has, ", "), every, path)
	if len(hasNot) > 0 {
		said += "; no " + strings.Join(hasNot, ", ") + " here"
	}
	return said, true
}

// sampleUntil records the machine under root every so often until limit has passed, ctx is done,
// or a sample cannot be written. One sampler holds path's lock: a job that starts a second has it
// sample nothing.
func sampleUntil(ctx context.Context, root, path string, every, limit time.Duration) error {
	unlock, err := lock(path + ".sampling")
	if errors.Is(err, errSampled) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	end := time.NewTimer(limit)
	defer end.Stop()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if err := ghatrace.RecordSample(machine(root, time.Now())); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-end.C:
			return nil
		case <-tick.C:
		}
	}
}
