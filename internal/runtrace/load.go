package runtrace

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// Load is what a job's machine did between two of its samples: each measure's value over that
// time, by its name (machineSeries). A measure the machine does not have is absent.
type Load struct {
	Start time.Time          `json:"start"`
	End   time.Time          `json:"end"`
	Of    map[string]float64 `json:"of"`
}

// series is one series of a machine's load: its name, the Perfetto counter it is drawn on (none:
// it is only read), and its value between two samples that both have it.
type series struct {
	name, counter string
	of            func(a, b ghatrace.Sample, secs float64) (float64, bool)
}

// The machineSeries' names, for what reads them by name.
const (
	cpus        = "cpus"
	busy        = "busy"
	iowait      = "iowait"
	steal       = "steal"
	cpuStall    = "cpu some"
	ioStall     = "io some"
	ioFull      = "io full"
	memoryStall = "memory some"
	memoryFull  = "memory full"
	read        = "read"
	written     = "written"
	diskBusy    = "busiest"
)

var machineSeries = []series{
	{cpus, "", func(_, b ghatrace.Sample, _ float64) (float64, bool) {
		if b.CPU == nil {
			return 0, false
		}
		return float64(b.CPU.CPUs), true
	}},
	// How many CPUs' worth ran something: neither idle, nor idle waiting on I/O, nor taken by the
	// hypervisor.
	{busy, "CPUs busy", func(a, b ghatrace.Sample, _ float64) (float64, bool) {
		total, ok := cpuTotal(a, b)
		if !ok {
			return 0, false
		}
		idle := delta(a.CPU.Idle, b.CPU.Idle) + delta(a.CPU.IOWait, b.CPU.IOWait) + delta(a.CPU.Steal, b.CPU.Steal)
		return float64(total-idle) / float64(total) * float64(b.CPU.CPUs), idle <= total
	}},
	{iowait, "CPU time %", func(a, b ghatrace.Sample, _ float64) (float64, bool) {
		total, ok := cpuTotal(a, b)
		return pct(float64(delta(a.CPU.IOWait, b.CPU.IOWait)) / float64(total)), ok
	}},
	{steal, "CPU time %", func(a, b ghatrace.Sample, _ float64) (float64, bool) {
		total, ok := cpuTotal(a, b)
		return pct(float64(delta(a.CPU.Steal, b.CPU.Steal)) / float64(total)), ok
	}},
	stall(cpuStall, func(p *ghatrace.Pressure) uint64 { return p.CPUSome }),
	stall(ioStall, func(p *ghatrace.Pressure) uint64 { return p.IOSome }),
	stall(ioFull, func(p *ghatrace.Pressure) uint64 { return p.IOFull }),
	stall(memoryStall, func(p *ghatrace.Pressure) uint64 { return p.MemorySome }),
	stall(memoryFull, func(p *ghatrace.Pressure) uint64 { return p.MemoryFull }),
	disk(read, "disk MB/s", func(a, b ghatrace.Disk, secs float64) float64 {
		return float64(delta(a.Read, b.Read)) / secs / 1e6
	}, sum),
	disk(written, "disk MB/s", func(a, b ghatrace.Disk, secs float64) float64 {
		return float64(delta(a.Written, b.Written)) / secs / 1e6
	}, sum),
	// The disk with a request in flight the longest: two disks half busy each are not one disk
	// busy all the time.
	disk(diskBusy, "disk busy %", func(a, b ghatrace.Disk, secs float64) float64 {
		return pct(float64(delta(a.BusyMS, b.BusyMS)) / (secs * 1000))
	}, maxOf),
}

// stall is a share of the time at least one task (some) or every task (full) waited for a
// resource, from the stall time /proc/pressure counts to the microsecond.
func stall(name string, of func(*ghatrace.Pressure) uint64) series {
	return series{name, "stalled %", func(a, b ghatrace.Sample, secs float64) (float64, bool) {
		if a.Pressure == nil || b.Pressure == nil || of(b.Pressure) < of(a.Pressure) {
			return 0, false
		}
		return pct(float64(of(b.Pressure)-of(a.Pressure)) / (secs * 1e6)), true
	}}
}

// disk is a measure of the disks both samples have, each disk's value put together by combine.
func disk(name, counter string, of func(a, b ghatrace.Disk, secs float64) float64, combine func(float64, float64) float64) series {
	return series{name, counter, func(a, b ghatrace.Sample, secs float64) (float64, bool) {
		var v float64
		found := false
		for _, db := range b.Disks {
			i := slices.IndexFunc(a.Disks, func(da ghatrace.Disk) bool { return da.Name == db.Name })
			if i < 0 {
				continue
			}
			v = combine(v, of(a.Disks[i], db, secs))
			found = true
		}
		return v, found
	}}
}

func sum(a, b float64) float64   { return a + b }
func maxOf(a, b float64) float64 { return max(a, b) }

// cpuTotal is the ticks all CPUs counted between a and b, when both have them and some passed.
func cpuTotal(a, b ghatrace.Sample) (uint64, bool) {
	if a.CPU == nil || b.CPU == nil {
		return 1, false
	}
	t := func(c *ghatrace.CPU) uint64 {
		return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal
	}
	if t(b.CPU) <= t(a.CPU) {
		return 1, false
	}
	return t(b.CPU) - t(a.CPU), true
}

// delta is how much a counter grew: nothing, when it went back.
func delta(a, b uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

// pct is a share as a percentage, no more than all of it: a sample is read a moment after its
// time is taken, and the moment can put a share a hair over.
func pct(share float64) float64 { return min(100*share, 100) }

// loads is the load of each job's machine between each two of its samples, in order.
func loads(samples []ghatrace.Sample) []Load {
	slices.SortStableFunc(samples, func(a, b ghatrace.Sample) int { return a.At.Compare(b.At) })
	var out []Load
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		secs := b.At.Sub(a.At).Seconds()
		if secs <= 0 {
			continue
		}
		l := Load{Start: a.At, End: b.At, Of: map[string]float64{}}
		for _, m := range machineSeries {
			if v, ok := m.of(a, b, secs); ok {
				l.Of[m.name] = v
			}
		}
		out = append(out, l)
	}
	return out
}

// over is each measure's mean over from..to, each load weighed by how much of that time it covers;
// nil when no load does.
func over(ls []Load, from, to time.Time) map[string]float64 {
	sums, weights := map[string]float64{}, map[string]float64{}
	for _, l := range ls {
		w := earliest(l.End, to).Sub(latest(l.Start, from)).Seconds()
		if w <= 0 {
			continue
		}
		for name, v := range l.Of {
			sums[name] += v * w
			weights[name] += w
		}
	}
	if len(sums) == 0 {
		return nil
	}
	for name := range sums {
		sums[name] /= weights[name]
	}
	return sums
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// loadCells is a load as the summary's columns: how many CPUs were busy, how long tasks stalled
// for a CPU, for I/O and for memory, and the disks; "-" for what the machine did not say.
func loadCells(m map[string]float64) [3]string {
	cells := [3]string{"-", "-", "-"}
	if b, ok := m[busy]; ok {
		cells[0] = fmt.Sprintf("%.1f of %.0f", b, m[cpus])
	}
	if _, ok := m[cpuStall]; ok {
		cells[1] = fmt.Sprintf("%.0f%%, %.0f%%, %.0f%%", m[cpuStall], m[ioStall], m[memoryStall])
	}
	if b, ok := m[diskBusy]; ok {
		cells[2] = fmt.Sprintf("%.1f MB/s, %.0f%% busy", m[read]+m[written], b)
	}
	return cells
}

// loadText is a load in a line: what loadCells says, with the CPUs' time waiting on I/O and taken
// by the hypervisor when either is more than a trace of it.
func loadText(m map[string]float64) string {
	if len(m) == 0 {
		return ""
	}
	var parts []string
	if b, ok := m[busy]; ok {
		parts = append(parts, fmt.Sprintf("%.1f of %.0f CPUs busy", b, m[cpus]))
	}
	for _, c := range []struct {
		name, label string
	}{{iowait, "iowait"}, {steal, "steal"}} {
		if v := m[c.name]; v >= 1 {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", c.label, v))
		}
	}
	if _, ok := m[cpuStall]; ok {
		parts = append(parts, fmt.Sprintf("stalled on CPU %.0f%%, IO %.0f%%, memory %.0f%%", m[cpuStall], m[ioStall], m[memoryStall]))
	}
	if b, ok := m[diskBusy]; ok {
		parts = append(parts, fmt.Sprintf("disk %.1f MB/s read, %.1f MB/s written, %.0f%% busy", m[read], m[written], b))
	}
	return strings.Join(parts, " · ")
}

// usageText is what a span's command used, in a line.
func usageText(u *ghatrace.Usage) string {
	if u == nil {
		return ""
	}
	parts := []string{short(u.CPU()) + " CPU"}
	// A process that ran had a resident set: none is a kernel that does not count one, or the rest.
	if u.MaxRSS > 0 {
		parts = append(parts, megabytes(u.MaxRSS)+" largest process", megabytes(u.Read)+" read", megabytes(u.Written)+" written")
	}
	return strings.Join(parts, ", ")
}

// megabytes is a size in MB, a million bytes.
func megabytes(n int64) string { return fmt.Sprintf("%.0f MB", float64(n)/1e6) }

// counters is the Perfetto counters the machineSeries are drawn on, each with its machineSeries, in order.
func counters() [][]series {
	var out [][]series
	for _, m := range machineSeries {
		if m.counter == "" {
			continue
		}
		i := slices.IndexFunc(out, func(c []series) bool { return c[0].counter == m.counter })
		if i < 0 {
			out = append(out, nil)
			i = len(out) - 1
		}
		out[i] = append(out[i], m)
	}
	return out
}
