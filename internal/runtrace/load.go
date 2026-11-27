package runtrace

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spin-stack/go-tools/ghatrace"
)

// Load is what a job's machine did between two of its samples: each series' value over that time,
// by its name (machineSeries). A series the machine does not have is absent.
type Load struct {
	Start time.Time          `json:"start"`
	End   time.Time          `json:"end"`
	Of    map[string]float64 `json:"of"`
}

// series is one series of a machine's load: its name, the Perfetto counter it is drawn on (none:
// it is only read), and its value between two samples that both have it.
type series struct {
	name, counter string
	of            func(a, b ghatrace.Sample, secs float64) (v float64, ok bool)
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
	{cpus, "", func(_, b ghatrace.Sample, _ float64) (v float64, ok bool) {
		if b.CPU != nil {
			v, ok = float64(b.CPU.CPUs), true
		}
		return v, ok
	}},
	// The share of all CPUs' time that ran something: neither idle, nor idle with IO outstanding
	// (iowait), nor taken by the hypervisor (steal).
	{busy, "CPU time %", cpuShare(func(c *ghatrace.CPU) []uint64 { return []uint64{c.User, c.Nice, c.System, c.IRQ, c.SoftIRQ} })},
	{iowait, "CPU time %", cpuShare(func(c *ghatrace.CPU) []uint64 { return []uint64{c.IOWait} })},
	{steal, "CPU time %", cpuShare(func(c *ghatrace.CPU) []uint64 { return []uint64{c.Steal} })},
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

// cpuShare is a series of the share of all CPUs' time spent in the states part names. Each
// state's ticks are its own difference, and all of them the sum of those: iowait can go back
// (Documentation/filesystems/proc.rst), and a state that went back counts nothing.
func cpuShare(part func(*ghatrace.CPU) []uint64) func(a, b ghatrace.Sample, _ float64) (float64, bool) {
	all := func(c *ghatrace.CPU) []uint64 {
		return []uint64{c.User, c.Nice, c.System, c.IRQ, c.SoftIRQ, c.Idle, c.IOWait, c.Steal}
	}
	return func(a, b ghatrace.Sample, _ float64) (v float64, ok bool) {
		if a.CPU == nil || b.CPU == nil {
			return v, ok
		}
		total := ticks(all(a.CPU), all(b.CPU))
		if total > 0 {
			v, ok = pct(float64(ticks(part(a.CPU), part(b.CPU)))/float64(total)), true
		}
		return v, ok
	}
}

// ticks is the sum of how much each of a's counters grew to b's.
func ticks(a, b []uint64) uint64 {
	var n uint64
	for i := range a {
		n += delta(a[i], b[i])
	}
	return n
}

// stall is a share of the time at least one task (some) or every task (full) waited for a
// resource, from the stall time /proc/pressure counts to the microsecond.
func stall(name string, of func(*ghatrace.Pressure) uint64) series {
	return series{name, "stalled %", func(a, b ghatrace.Sample, secs float64) (v float64, ok bool) {
		if a.Pressure != nil && b.Pressure != nil {
			v, ok = pct(float64(delta(of(a.Pressure), of(b.Pressure)))/(secs*1e6)), true
		}
		return v, ok
	}}
}

// disk is a series of the disks both samples have, each disk's value put together by combine.
func disk(name, counter string, of func(a, b ghatrace.Disk, secs float64) float64, combine func(float64, float64) float64) series {
	return series{name, counter, func(a, b ghatrace.Sample, secs float64) (v float64, ok bool) {
		for _, db := range b.Disks {
			i := slices.IndexFunc(a.Disks, func(da ghatrace.Disk) bool { return da.Name == db.Name })
			if i >= 0 {
				v, ok = combine(v, of(a.Disks[i], db, secs)), true
			}
		}
		return v, ok
	}}
}

func sum(a, b float64) float64   { return a + b }
func maxOf(a, b float64) float64 { return max(a, b) }

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

// cpusBusy is how many CPUs' worth were busy, of how many: 0.3 of 64 is one process working a
// third of the time, not a busy machine.
func cpusBusy(m map[string]float64) string {
	return fmt.Sprintf("%.1f of %.0f", m[busy]/100*m[cpus], m[cpus])
}

// loadCells is a load as the summary's columns: how many CPUs were busy, how long tasks stalled
// for a CPU, for I/O and for memory, and the disks; "-" for what the machine did not say.
func loadCells(m map[string]float64) [3]string {
	cells := [3]string{"-", "-", "-"}
	if _, ok := m[busy]; ok {
		cells[0] = cpusBusy(m)
	}
	if _, ok := m[cpuStall]; ok {
		cells[1] = fmt.Sprintf("%.0f%%, %.0f%%, %.0f%%", m[cpuStall], m[ioStall], m[memoryStall])
	}
	if b, ok := m[diskBusy]; ok {
		cells[2] = fmt.Sprintf("%.1f MB/s, %.0f%% busy", m[read]+m[written], b)
	}
	return cells
}

// loadText is a load in a line: what loadCells says, with the CPUs' time in iowait and steal.
func loadText(m map[string]float64) string {
	var parts []string
	if _, ok := m[busy]; ok {
		parts = append(parts, fmt.Sprintf("%s CPUs busy, iowait %.0f%%, steal %.0f%%", cpusBusy(m), m[iowait], m[steal]))
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
	if u.MaxRSS > 0 { // mutate-exempt: > 1 differs at a resident set of one byte, which no process has
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
