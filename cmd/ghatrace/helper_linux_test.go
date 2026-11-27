package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestMain runs the test binary as one of the commands the tests run, when GHATRACE_TEST_HELPER
// says which: ghatrace itself, a command that burns CPU or sleeps, or one that moves bytes to and
// from a disk.
func TestMain(m *testing.M) {
	switch os.Getenv("GHATRACE_TEST_HELPER") {
	case "":
		os.Exit(m.Run())
	case "main":
		main()
		os.Exit(0)
	case "burn":
		burn(300 * time.Millisecond)
	case "sleep":
		time.Sleep(400 * time.Millisecond)
	case "disk":
		os.Exit(disk(os.Getenv("GHATRACE_TEST_DIR")))
	}
	os.Exit(0)
}

// burn spins until this process has run for d on a CPU, however busy the machine is.
func burn(d time.Duration) {
	for {
		var ru syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
			panic(err)
		}
		if time.Duration(ru.Utime.Nano()+ru.Stime.Nano()) >= d {
			return
		}
		for range 1_000_000 {
			_ = strconv.Itoa(len(os.Args))
		}
	}
}

// diskBytes is what disk writes and reads, and memoryBytes what it keeps resident.
const (
	diskBytes   = 8 << 20
	memoryBytes = 64 << 20
)

// disk holds memoryBytes resident, writes diskBytes to a file in dir and has the disk take them
// (fsync), and reads them back past the page cache (O_DIRECT). 3 is a file system that cannot.
func disk(dir string) int {
	resident := make([]byte, memoryBytes)
	for i := range resident {
		resident[i] = 1
	}
	path := filepath.Join(dir, "bytes")
	if err := os.WriteFile(path, make([]byte, diskBytes), 0o600); err != nil {
		panic(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		panic(err)
	}
	if err := f.Sync(); err != nil {
		panic(err)
	}
	_ = f.Close()
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	defer func() { _ = syscall.Close(fd) }()
	// O_DIRECT reads into memory aligned to the disk's block.
	raw := make([]byte, diskBytes+4096)
	off := 4096 - int(uintptr(unsafe.Pointer(&raw[0]))%4096) //nolint:gosec // an address's remainder
	buf := raw[off%4096 : off%4096+diskBytes]
	for n := 0; n < diskBytes; {
		got, err := syscall.Read(fd, buf[n:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		n += got
	}
	runtime.KeepAlive(resident)
	return 0
}
