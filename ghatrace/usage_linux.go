package ghatrace

import (
	"os"
	"syscall"
)

func rusage(p *os.ProcessState, u *Usage) {
	if r, ok := p.SysUsage().(*syscall.Rusage); ok {
		fromRusage(r, u)
	}
}

// fromRusage adds what Linux's wait4 counted beyond CPU time: ru_maxrss in KiB, and ru_inblock and
// ru_oublock in 512-byte blocks. Each is an int32 on a 32-bit Linux.
func fromRusage(r *syscall.Rusage, u *Usage) {
	u.MaxRSS = int64(r.Maxrss) * 1024
	u.Read, u.Written = int64(r.Inblock)*512, int64(r.Oublock)*512
}
