package ghatrace

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUsageIsInBytes(t *testing.T) {
	var u Usage
	fromRusage(&syscall.Rusage{Maxrss: 3, Inblock: 2, Oublock: 5}, &u)
	assert.Equal(t, Usage{MaxRSS: 3 * 1024, Read: 2 * 512, Written: 5 * 512}, u)
}
