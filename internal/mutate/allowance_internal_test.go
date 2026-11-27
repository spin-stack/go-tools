package mutate

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// A run is allowed four times what its tests took with nothing broken, and ten seconds: the tests
// it asks, or every one where it asks them all, as an importer's run took them together where that
// was measured; each test four times its own. -timeout where one of them was not measured, and
// never more, and no bound at all where -timeout is 0. A package is the same by its directory
// however it is named.
func TestARunIsAllowedAMultipleOfWhatItsTestsTook(t *testing.T) {
	unbroken := NewUnbroken()
	unbroken.recordTests("testdata", map[string]time.Duration{"TestA": time.Second, "TestB": 2 * time.Second})
	importer := t.TempDir()
	unbroken.recordTests(importer, map[string]time.Duration{"TestA": time.Second})
	unbroken.recordWhole(importer, 30*time.Second)
	examples := t.TempDir()
	unbroken.recordTests(examples, map[string]time.Duration{})
	measured, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		dir     string
		only    []string
		ceiling time.Duration
		want    time.Duration
	}{
		{"the tests it asks", measured, []string{"TestA"}, 5 * time.Minute, 14 * time.Second},
		{"every test", "testdata", nil, 5 * time.Minute, 22 * time.Second},
		{"a test not measured", measured, []string{"TestA", "TestC"}, 5 * time.Minute, 5 * time.Minute},
		{"an importer's run, as it took", importer, nil, 5 * time.Minute, 130 * time.Second},
		{"an importer's tests it asks", importer, []string{"TestA"}, 5 * time.Minute, 14 * time.Second},
		{"a package not measured", t.TempDir(), nil, 5 * time.Minute, 5 * time.Minute},
		{"past -timeout", "testdata", nil, 15 * time.Second, 15 * time.Second},
		{"no -timeout", "testdata", nil, 0, 22 * time.Second},
		{"not measured, and no -timeout", t.TempDir(), nil, 0, 0},
		{"a package of examples alone, which no test measures", examples, nil, 5 * time.Minute, 5 * time.Minute},
	} {
		if got := unbroken.allowance(c.dir, c.only, c.ceiling); got.Run != c.want {
			t.Errorf("%s: allowed %s, want %s", c.name, got.Run, c.want)
		}
	}
	if each := unbroken.allowance(measured, []string{"TestA"}, time.Minute).Each; each["TestA"] != 14*time.Second || each["TestB"] != 18*time.Second {
		t.Errorf("each test is allowed %v, not four times its own and ten seconds", each)
	}
}

// A test is stopped once it runs past its own allowance, from when -test.v says it starts, and
// only then: not once it has ended or paused to run beside the others, whatever runs after it.
// What a binary says is read as lines, however it comes.
func TestATestIsStoppedPastItsOwnAllowance(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		said []string
		want string
	}{
		{"past its allowance", []string{"=== RUN   TestSlow\n"}, "TestSlow"},
		{"ended in time, before one nobody measured", []string{"=== RUN   TestSlow\n--- PASS: TestSlow (0.00s)\n=== RUN   TestOther\n"}, ""},
		{"failed in time", []string{"=== RUN   TestSlow\n    --- FAIL: TestSlow/sub (0.00s)\n--- FAIL: TestSlow (0.00s)\n"}, ""},
		{"paused to run beside the others", []string{"=== RUN   TestSlow\n=== PAUSE TestSlow\n"}, ""},
		{"a subtest's start, which is its test's time", []string{"=== RUN   TestSlow\n=== RUN   TestSlow/sub\n"}, "TestSlow"},
		{"after an empty line, in pieces", []string{"\n=== RU", "N   TestSlow\r\n"}, "TestSlow"},
		{"after what a test printed with no newline", []string{"waiting=== RUN   TestSlow\n"}, "TestSlow"},
		{"a start a test's output echoes, indented", []string{"=== RUN   TestOuter\n        === RUN   TestSlow\n"}, ""},
		{"a start a test logs, after its file and line", []string{"=== RUN   TestOuter\n    outer_test.go:12: === RUN   TestSlow\n"}, ""},
		{"an end a test's output echoes", []string{"=== RUN   TestSlow\n        --- PASS: TestSlow (0.00s)\n"}, "TestSlow"},
		{"a start a test's output echoes, while it runs", []string{"=== RUN   TestSlow\n        === RUN   TestInner\n"}, "TestSlow"},
		{"a start echoed after one space", []string{"=== RUN   TestOuter\n === RUN   TestSlow\n"}, ""},
		{"a start echoed after a tab", []string{"=== RUN   TestOuter\n\t=== RUN   TestSlow\n"}, ""},
		{"a start after what a test printed, ending in a space", []string{"=== RUN   TestOuter\n", "progress ", "=== RUN   TestSlow\n"}, ""},
		{"an end after what a test printed, ending in a space", []string{"=== RUN   TestSlow\n", "progress ", "--- PASS: TestSlow (0.00s)\n"}, ""},
		{"a pause after what a test printed, ending in a space", []string{"=== RUN   TestSlow\n", "progress ", "=== PAUSE TestSlow\n"}, ""},
		{"after an indented line", []string{"    said first\n=== RUN   TestSlow\n"}, "TestSlow"},
		{"its newline in a write of its own", []string{"=== RUN   TestSlow", "\n"}, "TestSlow"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			stopped := make(chan struct{})
			p := &pacer{each: map[string]time.Duration{"TestSlow": 50 * time.Millisecond}, stop: func() { close(stopped) }}
			for _, piece := range c.said {
				if _, err := p.Write([]byte(piece)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-stopped:
			case <-time.After(500 * time.Millisecond):
			}
			if test, allowed := p.stopped(); test != c.want || (test != "" && allowed != 50*time.Millisecond) {
				t.Errorf("stopped %q at %s, want %q", test, allowed, c.want)
			}
		})
	}
}

// A test that prints progress with no newline, a few dots a write for as long as it runs, costs
// the pacer what each write holds, not all it printed before: the test's writes wait on it. A
// start after the dots is still read.
func TestOutputWithNoNewlineIsReadOnce(t *testing.T) {
	t.Parallel()
	stopped := make(chan struct{})
	p := &pacer{each: map[string]time.Duration{"TestSlow": 50 * time.Millisecond}, stop: func() { close(stopped) }}
	dots := bytes.Repeat([]byte("."), 50)
	started := time.Now()
	held := 0
	for range 200_000 {
		if _, err := p.Write(dots); err != nil {
			t.Fatal(err)
		}
		held = max(held, len(p.partial))
	}
	if held > 2*keep {
		t.Errorf("the pacer held %d bytes of one line, more than twice keep", held)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("10 MB of dots with no newline took %s to read", took)
	}
	if _, err := p.Write([]byte("=== RUN   TestSlow\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
	}
	if test, _ := p.stopped(); test != "TestSlow" {
		t.Errorf("the start after the dots stopped %q, not TestSlow", test)
	}
}
