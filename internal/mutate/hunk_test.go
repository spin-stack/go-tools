package mutate

import "testing"

// A hunk header is git's, and this reads the side of it that says where the new file changed.
// Anything else - a line that only starts like one, a header this does not understand - has to
// be passed over rather than read as line zero or reached into for a field that is not there.
func TestAHunkHeaderIsReadFromTheSideThatSaysWhereTheNewFileChanged(t *testing.T) {
	for _, tc := range []struct {
		header      string
		from, count int
		ok          bool
	}{
		{"@@ -1,3 +7,4 @@ func Mark(now int) {", 7, 4, true},
		{"@@ -0,0 +1 @@", 1, 1, true},
		{"@@ -4 +4 @@", 4, 1, true},
		{"@@ -1,3 -7,4 @@", 0, 0, false},
		{"@@ -1,3 +x,4 @@", 0, 0, false},
		{"@@ -1,3 +7,x @@", 0, 0, false},
		{"@@ nothing", 0, 0, false},
		{"@@", 0, 0, false},
	} {
		from, count, ok := hunk(tc.header)
		if ok != tc.ok || from != tc.from || count != tc.count {
			t.Errorf("%q read as (%d, %d, %v), want (%d, %d, %v)", tc.header, from, count, ok, tc.from, tc.count, tc.ok)
		}
	}
}
