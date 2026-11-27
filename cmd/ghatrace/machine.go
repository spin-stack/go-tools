package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// machine is the machine whose /proc and /sys are under root at the moment at: what it has of
// /proc/stat, /proc/pressure and /proc/diskstats, and nothing of what it does not.
func machine(root string, at time.Time) ghatrace.Sample {
	return ghatrace.Sample{At: at, CPU: cpuOf(root), Pressure: pressureOf(root), Disks: disksOf(root)}
}

// cpuOf is /proc/stat's line for all CPUs, and how many CPUs have a line of their own.
func cpuOf(root string) *ghatrace.CPU {
	f, err := os.Open(filepath.Join(root, "proc", "stat"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() // read-only
	var c *ghatrace.CPU
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		// cpu, then user, nice, system, idle, iowait, irq, softirq and steal: 9 since Linux 2.6.11,
		// and more since.
		case len(fields) > 8 && fields[0] == "cpu":
			n, ok := numbers(fields[1:9])
			if !ok {
				return nil
			}
			c = &ghatrace.CPU{User: n[0], Nice: n[1], System: n[2], Idle: n[3], IOWait: n[4], IRQ: n[5], SoftIRQ: n[6], Steal: n[7]}
		case c != nil && len(fields) > 0 && strings.HasPrefix(fields[0], "cpu"):
			c.CPUs++
		}
	}
	return c
}

// pressureOf is the total of each of /proc/pressure's lines, or nil when the kernel has no PSI.
func pressureOf(root string) *ghatrace.Pressure {
	var p ghatrace.Pressure
	for _, r := range []struct {
		file       string
		some, full *uint64
	}{
		{"cpu", &p.CPUSome, nil},
		{"io", &p.IOSome, &p.IOFull},
		{"memory", &p.MemorySome, &p.MemoryFull},
	} {
		raw, err := os.ReadFile(filepath.Join(root, "proc", "pressure", r.file)) //nolint:gosec // the kernel's own file
		if err != nil {
			return nil
		}
		for l := range strings.Lines(string(raw)) {
			fields := strings.Fields(l)
			if len(fields) == 0 {
				continue
			}
			total, ok := strings.CutPrefix(fields[len(fields)-1], "total=")
			n, err := strconv.ParseUint(total, 10, 64)
			if !ok || err != nil {
				return nil
			}
			switch {
			case fields[0] == "some":
				*r.some = n
			case fields[0] == "full" && r.full != nil:
				*r.full = n
			}
		}
	}
	return &p
}

// disksOf is /proc/diskstats' line for each physical disk: a block device with a device under
// it in /sys/block, which a partition, a loop device and a device-mapper one do not have.
func disksOf(root string) []ghatrace.Disk {
	raw, err := os.ReadFile(filepath.Join(root, "proc", "diskstats")) //nolint:gosec // the kernel's own file
	if err != nil {
		return nil
	}
	var disks []ghatrace.Disk
	for l := range strings.Lines(string(raw)) {
		// major minor name, then reads, merged, sectors read, ms, writes, merged, sectors written,
		// ms, in flight, ms with a request in flight, weighted ms (Documentation/admin-guide/
		// iostats.rst): 14 since Linux 2.6, and more since.
		fields := strings.Fields(l)
		if len(fields) < 14 {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "sys", "block", fields[2], "device")); err != nil {
			continue
		}
		n, ok := numbers([]string{fields[5], fields[9], fields[12]})
		if !ok {
			continue
		}
		// A sector here is 512 bytes, whatever the disk's own.
		disks = append(disks, ghatrace.Disk{Name: fields[2], Read: n[0] * 512, Written: n[1] * 512, BusyMS: n[2]})
	}
	return disks
}

func numbers(fields []string) ([]uint64, bool) {
	out := make([]uint64, len(fields))
	for i, f := range fields {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
