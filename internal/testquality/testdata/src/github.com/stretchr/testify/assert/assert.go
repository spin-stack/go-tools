// Package assert is testify's, as much of it as the fixtures call: the analyzer knows it by its
// import path, and a test that asserts through it can fail even when t is not what it is handed.
package assert

// TestingT is what testify's assertions report to.
type TestingT interface {
	Errorf(format string, args ...any)
}

// True reports to t when v is false.
func True(t TestingT, v bool, _ ...any) bool {
	if !v {
		t.Errorf("not true")
	}
	return v
}
