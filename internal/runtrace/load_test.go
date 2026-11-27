package runtrace_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spin-stack/go-tools/ghatrace"
	"github.com/spin-stack/go-tools/internal/runtrace"
)

// machine is lane slow's machine (r3, its job from 60 to 300, step B from 120) sampled at 100, 101
// and 102: four CPUs, a CPU busy and IO stalled 30% of the first second while a disk read 10 MB and
// wrote 2 MB, three CPUs busy and nothing waiting in the next.
func machine() []ghatrace.Sample {
	sample := func(s float64, user, idle, iowait, ioSome, read, written, busyMS uint64) ghatrace.Sample {
		return ghatrace.Sample{
			Runner: "r3", At: t0.Add(time.Duration(s * float64(time.Second))),
			CPU:      &ghatrace.CPU{CPUs: 4, User: user, System: 20, Idle: idle, IOWait: iowait},
			Pressure: &ghatrace.Pressure{CPUSome: 7, IOSome: ioSome, MemorySome: 9},
			Disks: []ghatrace.Disk{
				{Name: "nvme0n1", Read: read, Written: written, BusyMS: busyMS},
				{Name: "sda", BusyMS: busyMS / 5},
			},
		}
	}
	return []ghatrace.Sample{
		// Out of order, as files from two artifacts are read.
		sample(102, 400, 1380, 20, 1_300_000, 20e6, 3e6, 1500),
		sample(100, 0, 1000, 0, 1_000_000, 10e6, 1e6, 1000),
		sample(101, 100, 1280, 20, 1_300_000, 20e6, 3e6, 1500),
		// A sample of a machine no job of the run ran on, and one of r3's from before its job.
		{Runner: "r9", At: at(101), CPU: &ghatrace.CPU{CPUs: 1}},
		{Runner: "r3", At: at(10), CPU: &ghatrace.CPU{CPUs: 4}},
	}
}

func TestAJobsLoadIsWhatItsMachineDidBetweenTwoSamples(t *testing.T) {
	tr := runtrace.Build(runOf(), nil, machine())
	slow := tr.Lanes[3]
	require.Equal(t, "lane slow", slow.Name)
	require.Len(t, slow.Load, 2, "two samples' worth, none from r9 nor from before the job")
	first, second := slow.Load[0], slow.Load[1]
	assert.Equal(t, [2]time.Time{at(100), at(101)}, [2]time.Time{first.Start, first.End})
	want := map[string]float64{
		"cpus": 4, "busy": 25, "iowait": 5, "steal": 0,
		"cpu some": 0, "io some": 30, "io full": 0, "memory some": 0, "memory full": 0,
		"read": 10, "written": 2, "busiest": 50,
	}
	require.Len(t, first.Of, len(want))
	for k, v := range want {
		assert.InDelta(t, v, first.Of[k], 1e-9, k)
	}
	assert.InDelta(t, 75, second.Of["busy"], 1e-9, "300 of 400 ticks busy")
	assert.InDelta(t, 0, second.Of["io some"], 1e-9)
	for _, l := range tr.Lanes {
		if l.Name != "lane slow" {
			assert.Empty(t, l.Load, l.Name)
		}
	}
}

// A machine without /proc/pressure or disks has no stall and no disk; what it has is drawn.
func TestALoadHasWhatTheMachineHas(t *testing.T) {
	samples := machine()[1:3]
	for i := range samples {
		samples[i].Pressure, samples[i].Disks = nil, nil
	}
	tr := runtrace.Build(runOf(), nil, samples)
	require.Len(t, tr.Lanes[3].Load, 1)
	assert.ElementsMatch(t, []string{"cpus", "busy", "iowait", "steal"}, keys(tr.Lanes[3].Load[0].Of))
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, tr, ""))
	assert.Contains(t, buf.String(), "| stepA | lane slow | 1m00s | 1.0 of 4 | - | - |\n")
	buf.Reset()
	require.NoError(t, runtrace.WriteHTML(&buf, tr))
	assert.Contains(t, buf.String(), `"load":[[100000,101000,0.25,5]]`, "the CPUs' iowait for the time waiting on IO")
}

// Each CPU state's ticks count once in all of them; a state that went back - iowait can - counts
// nothing; a machine whose ticks did not move, or a sample without its CPUs or its pressure, has
// no share of either, and a share is never over all of it.
func TestASeriesIsWhatBothSamplesHave(t *testing.T) {
	cpu := func(user, nice, system, irq, softirq, idle, iowait, steal uint64) *ghatrace.CPU {
		return &ghatrace.CPU{CPUs: 2, User: user, Nice: nice, System: system, IRQ: irq, SoftIRQ: softirq, Idle: idle, IOWait: iowait, Steal: steal}
	}
	stalled := func(memory uint64) *ghatrace.Pressure { return &ghatrace.Pressure{MemorySome: memory} }
	samples := []ghatrace.Sample{
		{At: at(100), CPU: cpu(100, 200, 300, 10, 20, 1000, 500, 50)},
		// 300 ticks: 80 busy, 40 iowait, 80 steal.
		{At: at(101), CPU: cpu(110, 220, 330, 15, 35, 1100, 540, 130)},
		// iowait went back by 100: 150 ticks, 50 of them busy.
		{At: at(102), CPU: cpu(160, 220, 330, 15, 35, 1200, 440, 130)},
		{At: at(103), CPU: cpu(160, 220, 330, 15, 35, 1200, 440, 130), Pressure: stalled(0)},
		{At: at(104), Pressure: stalled(1_000_000)},
		// 1.5 s stalled in a second: a moment between the time and the read.
		{At: at(105), Pressure: stalled(2_500_000)},
	}
	for i := range samples {
		samples[i].Runner = "r3"
	}
	ls := runtrace.Build(runOf(), nil, samples).Lanes[3].Load
	require.Len(t, ls, 5)
	want := []map[string]float64{
		{"cpus": 2, "busy": 100 * 80.0 / 300, "iowait": 100 * 40.0 / 300, "steal": 100 * 80.0 / 300},
		{"cpus": 2, "busy": 100 * 50.0 / 150, "iowait": 0, "steal": 0},
		{"cpus": 2},
		{"memory some": 100, "cpu some": 0, "io some": 0, "io full": 0, "memory full": 0},
		{"memory some": 100, "cpu some": 0, "io some": 0, "io full": 0, "memory full": 0},
	}
	for i, w := range want {
		assert.ElementsMatch(t, keys(w), keys(ls[i].Of), "load %d", i)
		for k, v := range w {
			assert.InDelta(t, v, ls[i].Of[k], 1e-9, "load %d: %s", i, k)
		}
	}
}

func keys(m map[string]float64) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The motivating case: a span that took minutes and used seconds of CPU, on a machine doing next
// to nothing, says so in the summary's slowest list.
func TestTheSummarySaysWhatTheSlowestUsedAndWhatTheMachineDid(t *testing.T) {
	spans := []ghatrace.Span{
		{Name: "mutate", Runner: "r3", Start: at(100), End: at(102), Usage: &ghatrace.Usage{User: time.Second, System: time.Second}},
	}
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, runtrace.Build(runOf(), spans, machine()), ""))
	out := buf.String()
	assert.Contains(t, out, "| slowest | job | took | CPU | CPUs busy | stalled on CPU, IO, memory | disk |\n|---|---|---|---|---|---|---|\n")
	assert.Contains(t, out, "| stepA | lane mid | 3m29s | - | - | - | - |\n", "a job whose machine was not sampled")
	assert.Contains(t, out, "| stepB | lane slow | 3m00s | - | - | - | - |\n", "no sample in its time")
	assert.Contains(t, out, "| mutate (span) | lane slow | 2s | 2s | 2.0 of 4 | 0%, 15%, 0% | 6.0 MB/s, 25% busy |\n", "the mean of the two seconds sampled")
}

func TestASummaryWithoutUsageOrSamplesHasNoColumnsForThem(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, runtrace.WriteSummary(&buf, runtrace.Build(runOf(), nil, nil), ""))
	assert.Contains(t, buf.String(), "| slowest | job | took |\n|---|---|---|\n")
}

// A job's machine is Perfetto counters under the job's process, an event for each at each sample
// but the last, and one at the last; a span's args say what its command used and the machine did.
func TestPerfettoCountersAreTheJobsMachine(t *testing.T) {
	spans := []ghatrace.Span{{Name: "mutate", Runner: "r3", Start: at(100), End: at(102), Usage: &ghatrace.Usage{User: time.Second, MaxRSS: 2e9, Read: 3e6, Written: 4e6}}}
	var buf bytes.Buffer
	require.NoError(t, runtrace.WritePerfetto(&buf, runtrace.Build(runOf(), spans, machine())))
	var doc struct {
		Events []struct {
			Ph   string          `json:"ph"`
			Name string          `json:"name"`
			PID  int             `json:"pid"`
			TS   int64           `json:"ts"`
			Args json.RawMessage `json:"args"`
		} `json:"traceEvents"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, []int64{100e6, 101e6}, countersOf(t, runtrace.Build(runOf(), nil, machine()[1:3]))["CPU time %"],
		"one load: an event at its start, and one at its end")
	pids := map[string]int{}
	counters := map[string][]int64{}
	var args map[string]any
	for _, e := range doc.Events {
		switch {
		case e.Ph == "M" && e.Name == "process_name":
			var a struct {
				Name string `json:"name"`
			}
			require.NoError(t, json.Unmarshal(e.Args, &a))
			pids[a.Name] = e.PID
		case e.Ph == "C":
			assert.Equal(t, pids["lane slow (critical path)"], e.PID, "under lane slow's process: %s", e.Name)
			// Perfetto takes a counter's args as numbers, each its own series.
			var values map[string]float64
			require.NoError(t, json.Unmarshal(e.Args, &values), "%s: %s", e.Name, e.Args)
			counters[e.Name] = append(counters[e.Name], e.TS)
		case e.Ph == "X" && strings.HasPrefix(e.Name, "mutate "):
			require.NoError(t, json.Unmarshal(e.Args, &args))
		}
	}
	us := func(s int) int64 { return int64(s) * 1e6 }
	for _, c := range []string{"CPU time %", "stalled %", "disk MB/s", "disk busy %"} {
		assert.Equal(t, []int64{us(100), us(101), us(102)}, counters[c], c)
	}
	assert.Len(t, counters, 4)
	assert.Equal(t, map[string]any{
		"its command": "1s CPU, 2000 MB largest process, 3 MB read, 4 MB written",
		"machine":     "2.0 of 4 CPUs busy, iowait 2%, steal 0% · stalled on CPU 0%, IO 15%, memory 0% · disk 5.0 MB/s read, 1.0 MB/s written, 25% busy",
	}, args)
}

func TestTheHTMLPageDrawsTheMachineUnderTheJob(t *testing.T) {
	var buf bytes.Buffer
	spans := []ghatrace.Span{{Name: "mutate", Runner: "r3", Start: at(100), End: at(102), Usage: &ghatrace.Usage{User: time.Second}}}
	require.NoError(t, runtrace.WriteHTML(&buf, runtrace.Build(runOf(), spans, machine())))
	page := buf.String()
	assert.Contains(t, page, `"load":[[100000,101000,0.25,30],[101000,102000,0.75,0]]`, "from, to, the share of the CPUs busy, IO stalled")
	assert.Contains(t, page, `"n":"mutate","k":"span","s":100000,"e":102000,"t":"its command: 1s CPU\nmachine: 2.0 of 4 CPUs busy`)
}

// countersOf is when each Perfetto counter of tr has an event.
func countersOf(t *testing.T, tr runtrace.Trace) map[string][]int64 {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runtrace.WritePerfetto(&buf, tr))
	var doc struct {
		Events []struct {
			Ph   string `json:"ph"`
			Name string `json:"name"`
			TS   int64  `json:"ts"`
		} `json:"traceEvents"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	out := map[string][]int64{}
	for _, e := range doc.Events {
		if e.Ph == "C" {
			out[e.Name] = append(out[e.Name], e.TS)
		}
	}
	return out
}
