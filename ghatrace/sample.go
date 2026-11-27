package ghatrace

import "time"

// Sample is the machine a job ran on at one moment, as Linux counts it from boot: counters that
// only grow, so what the machine did between two samples is their difference, exact whatever
// happened between them. What the machine does not have is absent.
type Sample struct {
	Runner string    `json:"runner"`
	Job    string    `json:"job"`
	At     time.Time `json:"at"`
	// CPU is /proc/stat's line for all CPUs together.
	CPU *CPU `json:"cpu,omitempty"`
	// Pressure is /proc/pressure's: how long tasks stalled for each resource.
	Pressure *Pressure `json:"pressure,omitempty"`
	// Disks is /proc/diskstats' line for each of the machine's physical disks: a partition, a loop
	// device or a device-mapper one would count the same bytes again.
	Disks []Disk `json:"disks,omitempty"`
}

// CPU is the time all CPUs spent in each state, in USER_HZ ticks (a hundredth of a second on every
// Linux GitHub runs). A guest's time is in User and Nice already.
type CPU struct {
	CPUs    int    `json:"cpus"`
	User    uint64 `json:"user"`
	Nice    uint64 `json:"nice"`
	System  uint64 `json:"system"`
	Idle    uint64 `json:"idle"`
	IOWait  uint64 `json:"iowait"`
	IRQ     uint64 `json:"irq"`
	SoftIRQ uint64 `json:"softirq"`
	Steal   uint64 `json:"steal"`
}

// Pressure is each `total=` of /proc/pressure/cpu, io and memory, in microseconds: how long at
// least one task waited for the resource (some), and how long every task that could run did (full).
// The machine's CPU full is left out: Linux counts it for a cgroup, and for the machine it is 0.
type Pressure struct {
	CPUSome    uint64 `json:"cpu_some_us"`
	IOSome     uint64 `json:"io_some_us"`
	IOFull     uint64 `json:"io_full_us"`
	MemorySome uint64 `json:"memory_some_us"`
	MemoryFull uint64 `json:"memory_full_us"`
}

// Disk is one disk's line of /proc/diskstats: what it read and wrote, and how long it had a
// request in flight.
type Disk struct {
	Name    string `json:"name"`
	Read    uint64 `json:"read_bytes"`
	Written uint64 `json:"written_bytes"`
	BusyMS  uint64 `json:"busy_ms"`
}
