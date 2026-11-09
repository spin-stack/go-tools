package ghatrace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShort(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0s", 250 * time.Millisecond: "250ms", 7 * time.Second: "7s", 60 * time.Second: "1m00s",
		125 * time.Second: "2m05s", 3600 * time.Second: "1h00m", 3780 * time.Second: "1h03m",
	} {
		assert.Equal(t, want, short(d))
	}
}
