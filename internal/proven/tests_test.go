package proven

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every test a log says ended, Go's and Playwright's alike, with its answer and its time: a
// subtest is its parent's, a Playwright test is named by its own title under its describe, a
// retry is one more answer, and what the log puts before a line does not hide it.
func TestEveryTestALogSaysEnded(t *testing.T) {
	log := "2026-10-26T13:00:00.0000000Z === RUN   TestA\n" +
		"2026-10-26T13:00:01.0000000Z     --- PASS: TestA/one (0.10s)\n" +
		"2026-10-26T13:00:01.0000000Z --- PASS: TestA (1.50s)\n" +
		"2026-10-26T13:00:03.0000000Z --- FAIL: TestB (2s)\n" +
		"2026-10-26T13:00:04.0000000Z   ✓  1 [chromium] › auth.spec.ts:30:3 › the CLI's device page stands alone (3.2s)\n" +
		"2026-10-26T13:00:05.0000000Z   ✘  2 [chromium] › admin.spec.ts:40:3 › presets › an admin's preset sizes a workspace (retry #1) (850ms)\n" +
		"2026-10-26T13:00:06.0000000Z   ✓  3 [chromium] › admin.spec.ts:40:3 › presets › an admin's preset sizes a workspace (1.1m)\n" +
		"2026-10-26T13:00:07.0000000Z   -  4 [chromium] › skipped.spec.ts:1:1 › nothing here\n" +
		"2026-10-26T13:00:08.0000000Z   ✓  5 site.spec.ts:12:3 › a person's workspaces (2s)\n"
	assert.Equal(t, []Test{
		{Name: "TestA", Passed: true, Seconds: 1.5},
		{Name: "TestB", Passed: false, Seconds: 2},
		{Name: "the CLI's device page stands alone", Spec: "auth.spec.ts", Passed: true, Seconds: 3.2},
		{Name: "an admin's preset sizes a workspace", Spec: "admin.spec.ts", Passed: false, Seconds: 0.85},
		{Name: "an admin's preset sizes a workspace", Spec: "admin.spec.ts", Passed: true, Seconds: 66},
		{Name: "a person's workspaces", Spec: "site.spec.ts", Passed: true, Seconds: 2},
	}, Tests(log))
	assert.Empty(t, Tests("nothing ended here\n"))
}

// A measurement is a "measure <name>=<value>" line, however the log prefixes it; a name with a
// capital or a value with letters is not one.
func TestEveryMeasurementALogHolds(t *testing.T) {
	log := "2026-10-26T13:00:01.0000000Z     measure_test.go:40: measure boot_to_running_ms=12345\n" +
		"=== RUN TestX\n" +
		"    measure_test.go:52: measure diff_etc_ms=0.5 measure snapshot_disk_ms=-1\n" +
		"not a measure Boot_ms=3 nor measure disk=fast\n"
	assert.Equal(t, []Measure{
		{Name: "boot_to_running_ms", Value: 12345},
		{Name: "diff_etc_ms", Value: 0.5},
		{Name: "snapshot_disk_ms", Value: -1},
	}, Measures(log))
	assert.Empty(t, Measures("--- PASS: TestA (1.50s)\n"))
}
