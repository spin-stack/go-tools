package interrupt

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The first interrupt cancels the context and the process goes on to clean up; a second one ends
// it, as a person pressing Ctrl+C again over a cleanup that hangs expects.
func TestASecondInterruptEndsTheProcess(t *testing.T) {
	if os.Getenv("INTERRUPT_CHILD") == "1" {
		ctx := Context()
		_, _ = os.Stdout.WriteString("ready\n")
		<-ctx.Done()
		_, _ = os.Stdout.WriteString("cancelled\n")
		// A cleanup that hangs: stdin is a pipe the test never writes to or closes.
		_, _ = io.ReadAll(os.Stdin)
		_, _ = os.Stdout.WriteString("cleanup finished\n")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestASecondInterruptEndsTheProcess$") //nolint:gosec // this test's own binary
	cmd.Env = append(os.Environ(), "INTERRUPT_CHILD=1")
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	lines := make(chan string)
	go func() {
		for s := bufio.NewScanner(stdout); s.Scan(); {
			lines <- s.Text()
		}
		close(lines)
	}()
	next := func() string {
		t.Helper()
		select {
		case l := <-lines:
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("the process said nothing for 10s")
			return ""
		}
	}
	if l := next(); l != "ready" {
		t.Fatalf("the process said %q, not ready", l)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Skipf("this platform sends no interrupt to a process: %v", err)
	}
	if l := next(); l != "cancelled" {
		t.Fatalf("after an interrupt the process said %q, not cancelled", l)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case l, open := <-lines:
		if open {
			t.Errorf("after a second interrupt the process went on: %q", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a second interrupt did not end the process")
	}
	if err := cmd.Wait(); err == nil {
		t.Error("a process ended by its second interrupt exited 0")
	}
}
