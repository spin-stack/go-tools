package ghatrace

import (
	"os"
	"syscall"
)

// rusage adds what Linux's wait4 counted beyond CPU time: ru_maxrss in KiB, and ru_inblock and
// ru_oublock in 512-byte blocks.
func rusage(p *os.ProcessState, u *Usage) {
	r, ok := p.SysUsage().(*syscall.Rusage)
	if !ok {
		return
	}
	u.MaxRSS = r.Maxrss * 1024
	u.Read, u.Written = r.Inblock*512, r.Oublock*512
}
